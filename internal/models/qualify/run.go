package qualify

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

// Errors Run refuses with before it spends anything.
var (
	ErrNotConfirmed = errors.New("a qualification run creates a GPU Droplet that costs money; pass --confirm to start one")
	ErrWrongAccount = errors.New("this token isn't the qualification account")
	ErrAnotherRun   = errors.New("another run is live")
)

// TeardownHours is the time a run allows for deleting its Droplet after
// MaxRunHours. Every Guard covers it, since the Droplet bills until then.
const TeardownHours = 0.5

// expiryMargin is how long after MaxRunHours a run's Droplet counts as
// live. Sweeps delete only expired Droplets, so a sweep never kills a run
// still tearing down.
const expiryMargin = time.Hour

// KeyGrace is how long a run's SSH key is kept from sweeps: the run's
// hours and the expiry margin, so a sweep never takes the key of a run
// still starting its Droplet.
func (c Config) KeyGrace() time.Duration {
	return time.Duration(c.MaxRunHours*float64(time.Hour)) + expiryMargin
}

// SpeechClipPath is where the speech probe's recording goes: "Loomplane
// qualification check", 16 kHz mono WAV, under five seconds. Until it's
// there, speech samples don't run.
const SpeechClipPath = "internal/models/qualify/testdata/speech.wav"

// SpendCheckInterval is how often a run re-reads the month's spend.
const SpendCheckInterval = 5 * time.Minute

// teardownTimeout bounds deleting the Droplet once the run is over,
// however it ended.
const teardownTimeout = 10 * time.Minute

// Report is the results JSON a run writes: what passed and failed, and
// what never ran and why. Only Results go into qualification.yaml.
type Report struct {
	RunID     string            `json:"run_id"`
	Kind      string            `json:"kind"`
	Week      string            `json:"week"`
	Run       string            `json:"run"`
	Size      string            `json:"size,omitempty"`
	HourlyUSD float64           `json:"hourly_usd,omitempty"`
	Results   []registry.Result `json:"results"`
	NotRun    []Skipped         `json:"not_run,omitempty"`
	Stopped   string            `json:"stopped,omitempty"` // why the run ended early
}

// Skipped is a sample that never ran, and why. It records nothing in the
// registry: a sample that didn't run proves nothing either way.
type Skipped struct {
	Sample
	Reason string `json:"reason"`
}

// Keys are the run's SSH keys: the client key it signs in with, and the
// host key the Droplet boots with, so the first connection is already
// pinned and nothing is trusted on first use.
type Keys struct {
	User ssh.Signer
	Host ssh.PublicKey
}

// Host runs engine containers on the run's Droplet. The real one is
// Docker over SSH through server.Runtime (see Connect).
type Host interface {
	// Start creates and starts c, and returns once it's running.
	Start(ctx context.Context, c *Container) error
	// Dial opens a TCP connection to one of c's ports over the SSH
	// channel, never over the internet.
	Dial(ctx context.Context, c *Container, port int) (net.Conn, error)
	// State reports whether c is still running, and its exit code if not.
	State(ctx context.Context, c *Container) (running bool, exitCode int, err error)
	// Logs returns the end of c's output.
	Logs(ctx context.Context, c *Container) ([]byte, error)
	// GPUKinds is the kind of every GPU the host's Docker hands out.
	GPUKinds(ctx context.Context) ([]string, error)
	// MetadataBlocked checks a container can't reach the cloud's metadata
	// service, which serves the Droplet's user data.
	MetadataBlocked(ctx context.Context) error
	// Remove deletes c and its weight volume.
	Remove(ctx context.Context, c *Container) error
	Close() error
}

// Container is one sample's engine, exactly as its model kind runs it on a
// server target. Files and Key hold secrets: the Hugging Face token and
// the engine's API key. String prints only the name.
type Container struct {
	Sample       Sample
	Kind         string // the model kind, "model-vllm"
	ID           id.ID
	Name         string
	Spec         cpprovider.ServiceSpec
	Labels       map[string]string
	Files        map[string][]byte
	FileModes    map[string]fs.FileMode
	Key          string // the engine's API key, for the probes
	ServedName   string
	MaxModelLen  int
	StartTimeout time.Duration
}

func (c *Container) String() string { return c.Name }

// GoString keeps the secrets out of %#v.
func (c *Container) GoString() string { return c.String() }

// enginePort is where every model kind's engine listens.
const enginePort = 8000

// Labels every qualification container carries. Docker volumes need the
// whole ownership set, and the run ID keeps two runs apart.
const (
	labelOrg     = "loomplane-qualify"
	labelProject = "qualify"
)

// RunDeps are what Run needs. Everything that reaches a cloud is an
// interface, so the tests run on fakes.
type RunDeps struct {
	Config   Config
	Registry *registry.Registry
	Results  []registry.Result // the committed results, for Pick
	Week     string            // ISO week; empty is this week
	Confirm  bool              // --confirm
	RunID    string            // empty generates one
	RunURL   string            // recorded on every result
	HFToken  string            // for gated weights; never logged
	// Speech is the speech probe's recording (SpeechClipPath). Without
	// one, speech samples are recorded as not run.
	Speech []byte

	Spend    Spend
	Machines Machines
	Offers   Offers
	// Connect waits for the Droplet and returns the Host on it.
	Connect func(ctx context.Context, m *Machine, keys Keys) (Host, error)
	// Probe checks a healthy engine; nil is the real probes.
	Probe func(ctx context.Context, client *http.Client, e registry.Entry, c *Container) (string, error)

	Now   func() time.Time
	Ticks <-chan time.Time // nil ticks every SpendCheckInterval
	Poll  time.Duration    // how often /health is tried; 0 is five seconds
	// StartTimeout replaces the kind's start timeout when set; tests
	// shorten it.
	StartTimeout time.Duration
	// DeleteRetryWait is the pause between deletes after a failed Create;
	// zero is thirty seconds.
	DeleteRetryWait time.Duration
	Log             io.Writer // progress lines; nil discards them
	// Report, when set, is filled with the run's results and every sample
	// that didn't run.
	Report *Report
}

func (d *RunDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *RunDeps) logf(format string, args ...any) {
	if d.Log != nil {
		_, _ = fmt.Fprintf(d.Log, format+"\n", args...)
	}
}

// Run qualifies this week's samples on one Droplet, and returns what each
// sample that ran recorded. It refuses unless d.Confirm is set and the
// token belongs to cfg.AccountUUID, sweeps what earlier runs left, and
// guards the Droplet against both caps before creating it and again before
// every sample. A ticker re-reads the month's spend every five minutes and
// stops the run at the cap. The Droplet is deleted however the run ends:
// an error, a cancel, the run's time limit or a panic. A sample is passed
// only when its probe passed on that exact revision and GPU; one the run
// couldn't finish (a cap, a cancel, a pull or SSH failure) is recorded as
// not run, never as failed.
func Run(ctx context.Context, d RunDeps) (results []registry.Result, err error) {
	rep := d.Report
	if rep == nil {
		rep = &Report{}
	}
	rep.Run = d.RunURL
	defer func() { rep.Results = slices.Clone(results) }()
	if !d.Confirm {
		return nil, ErrNotConfirmed
	}
	if d.RunID != "" {
		if rErr := CheckRunID(d.RunID); rErr != nil {
			return nil, rErr
		}
	}
	if vErr := d.Config.Validate(); vErr != nil {
		return nil, vErr
	}
	account, err := d.Spend.AccountUUID(ctx)
	if err != nil {
		return nil, fmt.Errorf("couldn't read which account the token belongs to, so nothing starts: %w", err)
	}
	if account != d.Config.AccountUUID {
		return nil, ErrWrongAccount
	}
	swept, err := d.Machines.Sweep(ctx, d.now(), true)
	if err != nil {
		return nil, fmt.Errorf("couldn't delete what an earlier run left, so nothing starts: %w", err)
	}
	for _, s := range swept {
		d.logf("Deleted %s, left by an earlier run.", s)
	}
	live, err := d.Machines.Live(ctx, d.now())
	if err != nil {
		return nil, fmt.Errorf("couldn't tell whether another run is live, so nothing starts: %w", err)
	}
	if len(live) > 0 {
		return nil, fmt.Errorf("%w: %s hasn't expired, so nothing starts", ErrAnotherRun, strings.Join(live, ", "))
	}
	if bErr := CheckBalanceCurrent(ctx, d.Spend, d.now(), d.Config.BalanceLag()); bErr != nil {
		return nil, bErr
	}

	week := d.Week
	if week == "" {
		week = CurrentWeek(d.now())
	}
	kind, samples := Pick(d.Registry, d.Results, d.Config, week)
	rep.Kind, rep.Week = kind, week
	if kind == "" {
		return nil, fmt.Errorf("%q isn't an ISO week like 2026-W45", week)
	}
	if len(samples) == 0 {
		d.logf("Every sample on %s has a current result. Nothing to run.", kind)
		return nil, nil
	}
	skip := func(from int, reason string) {
		for _, s := range samples[from:] {
			rep.NotRun = append(rep.NotRun, Skipped{Sample: s, Reason: reason})
		}
	}
	offer, err := d.Offers.Offer(ctx, d.Config.Region, kind)
	if err != nil {
		skip(0, "no Droplet: "+err.Error())
		return nil, fmt.Errorf("couldn't find a %s Droplet in %s: %w", kind, d.Config.Region, err)
	}
	if offer.GPUs != DropletGPUs {
		skip(0, "no one-GPU Droplet")
		return nil, fmt.Errorf("the %s Droplet %s has %d GPUs; a run buys one with %d", kind, offer.Size, offer.GPUs, DropletGPUs)
	}
	rep.Size, rep.HourlyUSD = offer.Size, offer.HourlyUSD
	hours := d.Config.MaxRunHours
	billed := hours + TeardownHours // the Droplet bills until it's deleted
	if gErr := Guard(ctx, d.Spend, d.Config, offer.HourlyUSD, billed, 0); gErr != nil {
		skip(0, gErr.Error())
		rep.Stopped = gErr.Error()
		return nil, gErr
	}

	keys, userData, err := runKeys()
	if err != nil {
		return nil, err
	}
	runID := d.RunID
	if runID == "" {
		runID = newRunID(d.now())
	}
	d.RunID, rep.RunID = runID, runID
	start := d.now()
	spec := MachineSpec{
		RunID: runID, Region: d.Config.Region, Size: offer.Size, Image: offer.Image,
		UserData: userData, UserKey: keys.User.PublicKey(), Expires: start.Add(time.Duration(hours*float64(time.Hour)) + expiryMargin),
	}

	// Teardown is registered before the Droplet exists, so a panic, a
	// cancel or an error anywhere after Create still deletes it. It runs
	// on its own context: the run's may already be cancelled. It deletes
	// this run's Droplet and key by the run tag, which also finds a
	// Droplet a failed Create may have made, then sweeps only expired
	// leftovers, so it never touches another run.
	//
	// After a failed Create the Droplet may exist without DigitalOcean
	// listing it under its tag yet, so the delete is tried again for about
	// two minutes. Not on a cancel: the runner kills the job seconds after
	// its SIGINT, and the workflow's next step deletes the run by its ID.
	createFailed := false
	defer func() {
		if p := recover(); p != nil {
			err = errors.Join(err, fmt.Errorf("the qualification run crashed: %s", redact(fmt.Sprint(p), d.HFToken)))
		}
	}()
	defer func() {
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
		defer cancel()
		attempts := 1
		if createFailed && ctx.Err() == nil {
			attempts = deleteAttempts
		}
		gone, tErr := d.deleteRun(tctx, runID, attempts)
		left, sErr := d.Machines.Sweep(tctx, d.now(), true)
		for _, s := range append(gone, left...) {
			d.logf("Deleted %s.", s)
		}
		if tErr = errors.Join(tErr, sErr); tErr != nil {
			d.logf("TEARDOWN FAILED. Delete Droplets tagged %s by hand: %v", Tag, tErr)
			err = errors.Join(err, fmt.Errorf("teardown: %w", tErr))
		}
	}()

	d.logf("Creating a %s Droplet (%s, %s an hour) for up to %g hours.", kind, offer.Size, usd(offer.HourlyUSD), hours)
	machine, err := d.Machines.Create(ctx, spec)
	if err != nil {
		createFailed = true
		skip(0, "the Droplet couldn't be created")
		return nil, fmt.Errorf("couldn't create the Droplet: %w", err)
	}
	host, err := d.Connect(ctx, machine, keys)
	if err != nil {
		skip(0, "the Droplet never became ready")
		return nil, fmt.Errorf("the Droplet never became ready: %w", err)
	}
	defer host.Close()
	if gErr := checkGPUs(ctx, host, kind); gErr != nil {
		skip(0, gErr.Error())
		return nil, gErr
	}
	if mErr := host.MetadataBlocked(ctx); mErr != nil {
		skip(0, "containers aren't kept off the metadata service")
		return nil, mErr
	}

	// The run ends at MaxRunHours from the Droplet's start, or at a cap.
	limit := start.Add(time.Duration(hours * float64(time.Hour))).Sub(d.now())
	runCtx, cancelRun := context.WithTimeoutCause(ctx, limit, fmt.Errorf("%w: the run reached its %g hour limit", ErrOverCap, hours))
	defer cancelRun()
	runCtx, stop := context.WithCancelCause(runCtx)
	var watching sync.WaitGroup
	watching.Go(func() { d.watchSpend(runCtx, stop, offer.HourlyUSD, start) })
	defer watching.Wait()
	defer stop(nil)

	for i, s := range samples {
		if runCtx.Err() != nil {
			why := stopReason(runCtx)
			skip(i, why)
			rep.Stopped = why
			return results, context.Cause(runCtx)
		}
		elapsed := d.now().Sub(start).Hours()
		if gErr := Guard(runCtx, d.Spend, d.Config, offer.HourlyUSD, billed-elapsed, offer.HourlyUSD*elapsed); gErr != nil {
			skip(i, gErr.Error())
			rep.Stopped = gErr.Error()
			return results, gErr
		}
		d.logf("Sample %d of %d: %s.", i+1, len(samples), s)
		res, why := d.sample(runCtx, host, s)
		if why != "" {
			d.logf("  not run: %s", why)
			rep.NotRun = append(rep.NotRun, Skipped{Sample: s, Reason: why})
			continue
		}
		d.logf("  %s: %s", res.Outcome, res.Detail)
		results = append(results, res)
	}
	// A stop during the last sample still ends the run with its cause.
	if runCtx.Err() != nil {
		rep.Stopped = stopReason(runCtx)
		return results, context.Cause(runCtx)
	}
	return results, nil
}

// deleteAttempts is how many times teardown deletes the run after a failed
// Create: about two minutes with the default wait.
const deleteAttempts = 5

// deleteRun deletes the run's Droplets and key, up to attempts times until
// a Droplet is gone, waiting DeleteRetryWait between tries.
func (d *RunDeps) deleteRun(ctx context.Context, runID string, attempts int) ([]string, error) {
	wait := d.DeleteRetryWait
	if wait <= 0 {
		wait = 30 * time.Second
	}
	var deleted []string
	var err error
	for i := range attempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return deleted, errors.Join(err, ctx.Err())
			case <-time.After(wait):
			}
		}
		var gone []string
		gone, err = d.Machines.DeleteRun(ctx, runID)
		deleted = append(deleted, gone...)
		if err == nil && slices.ContainsFunc(gone, func(s string) bool { return strings.HasPrefix(s, "Droplet ") }) {
			break
		}
	}
	return deleted, err
}

// watchSpend re-reads the month's spend on every tick and stops the run
// when it reaches the monthly cap, when this run's own spend reaches the
// per-run cap, or when the balance can't be read: spend nobody can see is
// spend nobody can cap.
func (d *RunDeps) watchSpend(ctx context.Context, stop context.CancelCauseFunc, hourly float64, start time.Time) {
	ticks := d.Ticks
	if ticks == nil {
		t := time.NewTicker(SpendCheckInterval)
		defer t.Stop()
		ticks = t.C
	}
	_, runCap, err := d.Config.caps()
	if err != nil {
		stop(err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		if spent := hourly * d.now().Sub(start).Hours(); cents(spent) >= cents(runCap) {
			stop(fmt.Errorf("%w: this run has spent about %s, its %s cap", ErrOverCap, usd(spent), usd(runCap)))
			return
		}
		mtd, mErr := MonthToDate(ctx, d.Spend)
		if mErr != nil {
			if ctx.Err() == nil {
				stop(mErr)
			}
			return
		}
		// The balance never holds the run in progress, so add it.
		if spent := mtd + hourly*d.now().Sub(start).Hours(); AtCap(spent, d.Config) {
			stop(fmt.Errorf("%w: this month's qualification spend reached about %s, the $%s cap", ErrOverCap, usd(spent), d.Config.MonthlyCapUSD))
			return
		}
	}
}

// stopReason says why ctx ended.
func stopReason(ctx context.Context) string {
	if cause := context.Cause(ctx); cause != nil {
		return cause.Error()
	}
	return "the run was cancelled"
}

// sample runs one sample: start its engine, wait for /health up to the
// kind's start timeout, probe it, and remove it. It returns the result,
// or why the sample didn't run. The container is removed however the
// sample ends, so the next one starts on an empty cache.
func (d *RunDeps) sample(ctx context.Context, host Host, s Sample) (res registry.Result, notRun string) {
	e, ok := d.Registry.Entry(s.Entry)
	if !ok {
		return res, "the registry has no such entry"
	}
	q, ok := e.Quantization(s.Quantization)
	if !ok {
		return res, "the entry has no such quantization"
	}
	need, err := sampleNeed(e, q, s)
	if err != nil {
		return res, err.Error()
	}
	if e.Gated && d.HFToken == "" {
		return res, "the weights are gated and the run has no Hugging Face token"
	}
	if len(e.Tasks) > 0 && e.Tasks[0] == registry.TaskSpeech && len(d.Speech) == 0 {
		return res, "no speech clip to probe with: add a recorded speech clip at " + SpeechClipPath + " (\"Loomplane qualification check\", 16 kHz mono WAV, under five seconds)"
	}
	key, err := apiKey()
	if err != nil {
		return res, err.Error()
	}
	c, err := NewContainer(e, q, s, need, key, d.HFToken, d.RunID)
	if err != nil {
		return res, err.Error()
	}
	if d.StartTimeout > 0 {
		c.StartTimeout = d.StartTimeout
	}
	secrets := []string{d.HFToken, key}
	defer func() {
		// Once the run is stopping, the Droplet's deletion removes the
		// container and its volume. Cleaning up over SSH first could use
		// the seconds a cancelled job has before it's killed.
		if ctx.Err() != nil {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if rErr := host.Remove(rctx, c); rErr != nil {
			d.logf("  couldn't remove %s: %s (the Droplet's deletion removes it)", c, redact(rErr.Error(), secrets...))
		}
	}()

	res = registry.Result{
		Entry: e.ID, Quantization: q.ID, GPUKind: s.GPUKind, GPUs: s.GPUs, MaxModelLen: need.MaxModelLen,
		Engine: q.Engine, EngineVersion: engines.Current()[q.Engine], Run: d.RunURL,
	}
	_, res.Revision = q.WeightsRepo(e)
	// failed records what the engine itself did wrong, with its log. A
	// log the job can't read means the connection to the Droplet is in
	// doubt, so the sample is not run instead.
	failed := func(why string) (registry.Result, string) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		logs, lErr := host.Logs(lctx, c)
		if lErr != nil {
			return res, "the engine looked broken (" + redact(why, secrets...) + "), but its log couldn't be read: " + redact(lErr.Error(), secrets...)
		}
		res.Outcome, res.Detail, res.RanAt = registry.OutcomeFailed, failureDetail(why, logs, secrets...), d.now().UTC().Truncate(time.Second)
		return res, ""
	}
	lost := func(err error) (registry.Result, string) {
		return res, "lost the connection to the engine: " + redact(err.Error(), secrets...)
	}

	if sErr := host.Start(ctx, c); sErr != nil {
		if ctx.Err() != nil {
			return res, stopReason(ctx)
		}
		return res, "the engine couldn't start: " + redact(sErr.Error(), secrets...)
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return host.Dial(ctx, c, enginePort) },
		DisableKeepAlives: true,
	}}
	why, stateErr := d.waitHealthy(ctx, host, client, c)
	switch {
	case why == "":
	case ctx.Err() != nil:
		return res, stopReason(ctx)
	case stateErr != nil:
		return lost(stateErr)
	default:
		return failed(why)
	}
	probe := d.Probe
	if probe == nil {
		probe = func(ctx context.Context, client *http.Client, e registry.Entry, c *Container) (string, error) {
			return Probe(ctx, client, e, c, d.Speech)
		}
	}
	detail, err := probe(ctx, client, e, c)
	switch {
	case ctx.Err() != nil:
		return res, stopReason(ctx)
	case err != nil && IsTransport(err):
		return lost(err)
	case err != nil:
		return failed(err.Error())
	}
	res.Outcome, res.Detail, res.RanAt = registry.OutcomePassed, redact(detail, secrets...), d.now().UTC().Truncate(time.Second)
	return res, ""
}

// waitHealthy polls /health until it answers 200, the container exits, or
// the start timeout passes. It returns "" once healthy, else why not, and
// the last error reading the container's state: an engine that never got
// healthy is its own failure only while the job could still read its
// state.
func (d *RunDeps) waitHealthy(ctx context.Context, host Host, client *http.Client, c *Container) (string, error) {
	poll := d.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	wctx, cancel := context.WithTimeout(ctx, c.StartTimeout)
	defer cancel()
	var stateErr error
	for {
		running, code, err := host.State(wctx, c)
		switch {
		case err != nil:
			stateErr = err
		case !running:
			return fmt.Sprintf("the engine exited with code %d before /health answered", code), nil
		default:
			stateErr = nil
		}
		if stateErr == nil && healthy(wctx, client) {
			return "", nil
		}
		select {
		case <-wctx.Done():
			return fmt.Sprintf("the engine didn't answer /health within %s, the kind's start timeout", c.StartTimeout), stateErr
		case <-time.After(poll):
		}
	}
}

// checkGPUs refuses a Droplet whose GPUs aren't one of kind, as nvidia-smi
// names them. A result is only tested on the GPU it names.
func checkGPUs(ctx context.Context, host Host, kind string) error {
	kinds, err := host.GPUKinds(ctx)
	if err != nil {
		return fmt.Errorf("couldn't read the Droplet's GPUs: %w", err)
	}
	if len(kinds) != DropletGPUs || slices.ContainsFunc(kinds, func(k string) bool { return k != kind }) {
		return fmt.Errorf("the Droplet's GPUs read as %q, not one %s, so nothing it ran would be a %s result", kinds, kind, kind)
	}
	return nil
}

func healthy(ctx context.Context, client *http.Client) bool {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, "http://engine/health", http.NoBody)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16)); err != nil {
		return false
	}
	return res.StatusCode == http.StatusOK
}

// NewContainer builds the engine container for s exactly as its model
// kind runs it on a server target: the kind's pinned amd64 image digest,
// its wrapper command and engine binary, engines.Args in the args file,
// its environment with key as the model's API key, its GPU request, shared
// memory and start timeout, and a fresh weight volume. The Hugging Face
// token goes in the token file only for gated weights, as the kinds' hooks
// do; every other model gets an empty file.
func NewContainer(e registry.Entry, q registry.Quantization, s Sample, need registry.Need, key, token, runID string) (*Container, error) {
	onGPU := s.GPUKind != registry.CPUKind
	kind, ok := recipes.ModelKindFor(string(q.Engine), onGPU)
	if !ok {
		return nil, fmt.Errorf("no model kind runs %s %s", q.Engine, s.where())
	}
	version := ""
	for _, r := range engines.Releases() {
		if r.Engine == q.Engine {
			version = r.Version
		}
	}
	composition, err := recipes.Default().Resolve(kind, version, "amd64", "server")
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(composition.Components, func(c recipes.ResolvedComponent) bool { return c.Name == recipes.ModelEngine })
	if i < 0 {
		return nil, fmt.Errorf("%s has no %s component", kind, recipes.ModelEngine)
	}
	comp := composition.Components[i]

	served := e.ID
	spec := engines.Spec{Entry: e, Quantization: q, ServedName: served, MaxModelLen: need.MaxModelLen, GPUs: s.GPUs, OnGPU: onGPU && q.Engine == registry.EngineLlamaCPP}
	args, err := engines.Args(spec)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, v := range comp.Env {
		switch {
		case v.Secret == recipes.ModelAPIKey && v.Value == "" && v.From == nil && v.Template == "":
			env[v.Name] = key
		case v.Secret == "" && v.From == nil && v.Template == "":
			env[v.Name] = v.Value
		default:
			return nil, fmt.Errorf("%s's engine sets %s in a way the qualification job can't", kind, v.Name)
		}
	}
	files := map[string][]byte{
		recipes.ModelArgsPath:  []byte(strings.Join(args, "\n") + "\n"),
		recipes.ModelTokenPath: {},
		// The job downloads from huggingface.co itself.
		recipes.ModelEndpointPath: {},
	}
	if e.Gated {
		files[recipes.ModelTokenPath] = []byte(token)
	}
	modes := map[string]fs.FileMode{}
	for _, f := range comp.Files {
		if _, ok := files[f.Path]; !ok {
			return nil, fmt.Errorf("%s's engine needs %s, which the qualification job doesn't render", kind, f.Path)
		}
		modes[f.Path] = f.Mode
	}

	resources := comp.Resources
	if onGPU {
		resources = resources.ForGPUs(s.GPUs)
	}
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{
		owned.LabelOrg: labelOrg, owned.LabelProject: labelProject, owned.LabelEnvironment: "run-" + runID,
		owned.LabelConnection: "lpq-" + runID, owned.LabelInstance: iid.String(), owned.LabelRevision: composition.Revision,
		owned.LabelCatalogKind: kind, owned.LabelCatalogResource: "qualify", owned.LabelCatalogComponent: comp.Name,
	}
	sp := cpprovider.ServiceSpec{
		Name: comp.Name, Image: comp.Image, Command: slices.Clone(comp.Command), Args: slices.Clone(comp.Args), Env: env,
		Resources: cpprovider.ResourceSpec{CPUMillis: resources.CPUMillis, MemoryMB: resources.MemoryMB},
	}
	for _, p := range comp.Ports {
		sp.Ports = append(sp.Ports, cpprovider.PortSpec{Container: p.Container, Protocol: "tcp"})
	}
	switch {
	case comp.WeightCache != nil:
		sp.Volumes = []cpprovider.VolumeSpec{{Name: "weights", Type: "persistent", MountPath: comp.WeightCache.MountPath}}
	case comp.Volume != nil:
		sp.Volumes = []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: comp.Volume.MountPath}}
	}
	if onGPU {
		placement := provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: s.GPUKind, Count: s.GPUs}}
		raw, mErr := json.Marshal(placement)
		if mErr != nil {
			return nil, mErr
		}
		labels[owned.LabelGPU] = string(raw)
		sp.Resources.GPU = s.GPUKind + ":" + strconv.Itoa(s.GPUs)
	}
	if resources.ShmMB > 0 {
		labels[owned.LabelShmMB] = strconv.Itoa(resources.ShmMB)
	}
	return &Container{
		Sample: s, Kind: kind, ID: iid, Name: "lp-" + strings.ReplaceAll(iid.String(), "_", "-"),
		Spec: sp, Labels: labels, Files: files, FileModes: modes, Key: key, ServedName: served,
		MaxModelLen: need.MaxModelLen, StartTimeout: comp.StartTimeout,
	}, nil
}

// RuntimeValues are what server.Runtime writes into c before it starts.
func (c *Container) RuntimeValues() owned.RuntimeValues {
	return owned.RuntimeValues{Files: maps.Clone(c.Files), FileModes: maps.Clone(c.FileModes)}
}

// runKeys makes the run's client key and the Droplet's host key, and the
// cloud-config that boots the Droplet with that host key and NVIDIA's
// container toolkit.
func runKeys() (Keys, string, error) {
	_, userPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, "", err
	}
	user, err := ssh.NewSignerFromKey(userPrivate)
	if err != nil {
		return Keys{}, "", err
	}
	hostPublic, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, "", err
	}
	host, err := ssh.NewPublicKey(hostPublic)
	if err != nil {
		return Keys{}, "", err
	}
	userData, err := CloudInit(hostPrivate)
	if err != nil {
		return Keys{}, "", err
	}
	return Keys{User: user, Host: host}, userData, nil
}

// CloudInit is the Droplet's cloud-config. It replaces the image's host
// keys with the run's ED25519 key, so the job knows the key before the
// Droplet boots, blocks containers from the metadata service that serves
// this document, then runs P1's NVIDIA toolkit script and points Docker at
// the toolkit, as Loomplane's own GPU servers do. It holds a private key:
// it's sent to DigitalOcean and never logged.
func CloudInit(hostKey ed25519.PrivateKey) (string, error) {
	block, err := ssh.MarshalPrivateKey(hostKey, "")
	if err != nil {
		return "", err
	}
	public, err := ssh.NewPublicKey(hostKey.Public())
	if err != nil {
		return "", err
	}
	document := map[string]any{
		"ssh_deletekeys":  true,
		"ssh_genkeytypes": []string{},
		"ssh_keys": map[string]string{
			"ed25519_private": string(pem.EncodeToMemory(block)),
			"ed25519_public":  strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))),
		},
		"package_update": true,
		"packages":       []string{"ca-certificates", "curl", "gnupg"},
		"runcmd": []any{
			[]string{"bash", "-c", blockMetadata},
			[]string{"bash", "-c", cloudinfra.NVIDIAToolkitScript},
			[]string{"bash", "-c", blockMetadata},
			[]string{"systemctl", "enable", "--now", "docker"},
			[]string{"nvidia-ctk", "runtime", "configure", "--runtime=docker"},
			[]string{"systemctl", "restart", "docker"},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	// JSON is valid YAML.
	return "#cloud-config\n" + string(encoded), nil
}

// blockMetadata keeps containers off DigitalOcean's metadata service,
// which serves the user data, host key and all. DOCKER-USER is the chain
// Docker leaves alone and checks before forwarding any container's
// traffic, so the rule holds through Docker's restarts. It runs before
// Docker is installed or started, and again after the toolkit script in
// case that installed Docker and its first start rebuilt the chains. The
// job starts no container before cloud-init is done.
const blockMetadata = `iptables -N DOCKER-USER 2>/dev/null || true
iptables -C DOCKER-USER -d 169.254.169.254 -j REJECT 2>/dev/null || iptables -I DOCKER-USER -d 169.254.169.254 -j REJECT`

// apiKey is a fresh key for one sample's engine.
func apiKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "lpm_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// newRunID names a run by when it started and a random suffix:
// "20261102t0600-3f9a1c".
func newRunID(t time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%x", strings.ToLower(t.UTC().Format("20060102T1504")), b)
}
