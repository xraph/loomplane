package qualify

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

// testRegistry is the registry package's good fixture: a core chat model
// with GGUF weights, a TEI embedding model and a reasoning model.
func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r, err := registry.Load(os.DirFS("../registry/testdata/good"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func testConfig() Config {
	return Config{
		AccountUUID: "acct-qualify", Region: "tor1",
		GPUKinds:      []string{"nvidia-rtx-4000-ada", "nvidia-l40s", "nvidia-h100-80gb"},
		MonthlyCapUSD: "150.00", RunCapUSD: "25.00", MaxRunHours: 4, SamplesPerRun: 8, StaleAfterDays: 30,
	}
}

// week46 rotates to nvidia-l40s (46 % 3 == 1).
const week46 = "2026-W46"

func TestPickRotatesKindsAndPutsCoreFirst(t *testing.T) {
	r := testRegistry(t)
	cfg := testConfig()

	kind, _ := Pick(r, nil, cfg, "2026-W45")
	if kind != cfg.GPUKinds[45%3] {
		t.Fatalf("2026-W45 ran on %s, want %s", kind, cfg.GPUKinds[45%3])
	}
	if kind, _ = Pick(r, nil, cfg, week46); kind != "nvidia-l40s" {
		t.Fatalf("2026-W46 ran on %s, want nvidia-l40s", kind)
	}

	_, samples := Pick(r, nil, cfg, week46)
	if len(samples) == 0 {
		t.Fatal("picked nothing")
	}
	seenNonCore := false
	for _, s := range samples {
		e, _ := r.Entry(s.Entry)
		if !e.Core {
			seenNonCore = true
		} else if seenNonCore {
			t.Fatalf("core sample %s came after a non-core one: %v", s, samples)
		}
	}
	if samples[0].Entry != "acme/Small-1B-Instruct" {
		t.Fatalf("first sample %s, want the core entry", samples[0])
	}
	_, again := Pick(r, nil, cfg, week46)
	if !slices.Equal(samples, again) {
		t.Fatalf("the order changed within a week:\n%v\n%v", samples, again)
	}

	// A current result on that kind takes the core sample out; a stale
	// one, or one for another revision, doesn't.
	core := samples[0]
	e, _ := r.Entry(core.Entry)
	q, _ := e.Quantization(core.Quantization)
	need, err := sampleNeed(e, q, core)
	if err != nil {
		t.Fatal(err)
	}
	_, monday, _ := ParseWeek(week46)
	_, revision := q.WeightsRepo(e)
	result := registry.Result{
		Entry: e.ID, Quantization: q.ID, Revision: revision, GPUKind: core.GPUKind, GPUs: core.GPUs, MaxModelLen: need.MaxModelLen,
		Engine: q.Engine, EngineVersion: engines.Current()[q.Engine], Outcome: registry.OutcomePassed, RanAt: monday.AddDate(0, 0, -3),
	}
	_, fresh := Pick(r, []registry.Result{result}, cfg, week46)
	if slices.Contains(fresh, core) {
		t.Fatalf("a current result still picked %s", core)
	}
	stale := result
	stale.RanAt = monday.AddDate(0, 0, -31)
	repinned := result
	repinned.Revision = strings.Repeat("9", 40)
	oldEngine := result
	oldEngine.EngineVersion = "0.1.0"
	for name, res := range map[string]registry.Result{"stale": stale, "another revision": repinned, "another engine release": oldEngine} {
		if _, got := Pick(r, []registry.Result{res}, cfg, week46); got[0] != core {
			t.Errorf("a %s result kept %s from going first: %v", name, core, got)
		}
	}

	if kind, samples = Pick(r, nil, cfg, "2026-45"); kind != "" || samples != nil {
		t.Fatalf("a bad week picked %s %v", kind, samples)
	}
}

func TestPickOnlyFittingSamples(t *testing.T) {
	r := registry.Default()
	cfg := testConfig()
	cfg.GPUKinds = []string{"nvidia-rtx-4000-ada"}
	cfg.SamplesPerRun, cfg.MaxRunHours = 10000, 10000
	kind, samples := Pick(r, nil, cfg, "2026-W45")
	if kind != "nvidia-rtx-4000-ada" || len(samples) == 0 {
		t.Fatalf("picked %s %d samples", kind, len(samples))
	}
	for _, s := range samples {
		if s.Entry == "deepseek-ai/DeepSeek-R1-Distill-Llama-70B" && s.Quantization == "bf16" {
			t.Fatalf("picked %s", s)
		}
		e, _ := r.Entry(s.Entry)
		q, _ := e.Quantization(s.Quantization)
		if s.GPUKind == registry.CPUKind {
			if _, _, ok := registry.CPUFits(e, q); !ok || s.GPUs != 0 {
				t.Fatalf("picked %s, which doesn't fit on CPU", s)
			}
			continue
		}
		if need, reason, ok := registry.Smallest(e, q, kind, DropletGPUs); !ok || need.GPUs != s.GPUs {
			t.Fatalf("picked %s, which doesn't fit one GPU: %s", s, reason)
		}
	}

	// The default budget keeps to the run's hours and sample count.
	cfg = testConfig()
	_, samples = Pick(r, nil, cfg, "2026-W47")
	if len(samples) > cfg.SamplesPerRun || PlannedHours(r, samples) > cfg.MaxRunHours {
		t.Fatalf("%d samples, %.2f hours", len(samples), PlannedHours(r, samples))
	}
}

// fakeSpend is the account's balance.
type fakeSpend struct {
	mu         sync.Mutex
	account    string
	accountErr error
	mtd        string
	mtdErr     error
	reads      int
}

func (f *fakeSpend) MonthToDateUSD(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.mtd, f.mtdErr
}

func (f *fakeSpend) AccountUUID(context.Context) (string, error) { return f.account, f.accountErr }

func (f *fakeSpend) set(mtd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mtd = mtd
}

// fakeMachines records what the run created and deleted.
type fakeMachines struct {
	mu        sync.Mutex
	created   []MachineSpec
	deleted   []string
	sweeps    int
	createErr error
}

func (f *fakeMachines) Create(_ context.Context, spec MachineSpec) (*Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &Machine{ID: "4242", Name: "lpq-" + spec.RunID, IPv4: "203.0.113.7"}, nil
}

func (f *fakeMachines) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeMachines) Sweep(context.Context, time.Time, bool) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps++
	return nil, nil
}

type fakeOffers struct{ offer Offer }

func (f fakeOffers) Offer(context.Context, string, string) (Offer, error) { return f.offer, nil }

// fakeHost runs every container as one fake engine: an httptest server
// that answers every probe the way a healthy engine would.
type fakeHost struct {
	t         *testing.T
	mu        sync.Mutex
	engine    *httptest.Server
	current   *Container
	started   []*Container
	removed   []*Container
	exited    bool
	unhealthy bool
	logs      []byte
	onStart   func(n int)
	onRemove  func(n int)
}

func newFakeHost(t *testing.T, r *registry.Registry) *fakeHost {
	h := &fakeHost{t: t}
	h.engine = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h.mu.Lock()
		c, unhealthy := h.current, h.unhealthy
		h.mu.Unlock()
		if req.URL.Path == "/health" {
			if unhealthy {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		if c == nil || req.Header.Get("Authorization") != "Bearer "+c.Key {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		engineAnswer(w, req, r)
	}))
	t.Cleanup(h.engine.Close)
	return h
}

// engineAnswer answers a probe the way a healthy engine of the entry
// does.
func engineAnswer(w http.ResponseWriter, req *http.Request, r *registry.Registry) {
	switch req.URL.Path {
	case "/v1/chat/completions":
		w.Header().Set("Content-Type", "text/event-stream")
		for _, piece := range []string{"rea", "dy"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", piece)
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	case "/v1/embeddings":
		var in struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		e, _ := r.Entry(in.Model)
		out := map[string]any{"data": []any{}}
		for range in.Input {
			out["data"] = append(out["data"].([]any), map[string]any{"embedding": make([]float64, e.Embedding.Dimensions)})
		}
		_ = json.NewEncoder(w).Encode(out)
	case "/rerank":
		fmt.Fprint(w, `[{"index":0,"score":0.98},{"index":1,"score":0.01}]`)
	case "/v1/audio/transcriptions":
		fmt.Fprint(w, `{"text":"Loomplane qualification check."}`)
	default:
		http.NotFound(w, req)
	}
}

func (h *fakeHost) Start(_ context.Context, c *Container) error {
	h.mu.Lock()
	h.current = c
	h.started = append(h.started, c)
	n, hook := len(h.started), h.onStart
	h.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return nil
}

func (h *fakeHost) Dial(ctx context.Context, _ *Container, port int) (net.Conn, error) {
	if port != enginePort {
		h.t.Errorf("dialed port %d, want %d", port, enginePort)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", h.engine.Listener.Addr().String())
}

func (h *fakeHost) State(context.Context, *Container) (running bool, exitCode int, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exited {
		return false, 1, nil
	}
	return true, 0, nil
}

func (h *fakeHost) Logs(context.Context, *Container) ([]byte, error) { return h.logs, nil }

func (h *fakeHost) Remove(_ context.Context, c *Container) error {
	h.mu.Lock()
	h.removed = append(h.removed, c)
	n, hook := len(h.removed), h.onRemove
	h.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return nil
}

func (h *fakeHost) Close() error { return nil }

// runFixture is a run's fakes on the good registry, on nvidia-l40s.
type runFixture struct {
	spend    *fakeSpend
	machines *fakeMachines
	host     *fakeHost
	report   *Report
	deps     RunDeps
}

func newRun(t *testing.T) *runFixture {
	t.Helper()
	r := testRegistry(t)
	f := &runFixture{
		spend:    &fakeSpend{account: "acct-qualify", mtd: "10.00"},
		machines: &fakeMachines{},
		host:     newFakeHost(t, r),
		report:   &Report{},
	}
	f.deps = RunDeps{
		Config: testConfig(), Registry: r, Week: week46, Confirm: true, RunID: "test-run", RunURL: "https://github.com/xraph/loomplane/actions/runs/7",
		Spend: f.spend, Machines: f.machines,
		Offers:  fakeOffers{offer: Offer{Size: "gpu-l40sx1-48gb", Image: "gpu-h100x1-base", GPUs: 1, HourlyUSD: 1.57}},
		Connect: func(context.Context, *Machine, Keys) (Host, error) { return f.host, nil },
		Ticks:   make(chan time.Time, 1), Poll: time.Millisecond, Report: f.report,
	}
	return f
}

func (f *runFixture) samples() []Sample {
	_, s := Pick(f.deps.Registry, f.deps.Results, f.deps.Config, f.deps.Week)
	return s
}

func TestRunQualifiesEverySample(t *testing.T) {
	f := newRun(t)
	samples := f.samples()
	results, err := Run(t.Context(), f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(samples) || len(f.report.NotRun) != 0 {
		t.Fatalf("%d results and %d not run for %d samples: %+v", len(results), len(f.report.NotRun), len(samples), f.report.NotRun)
	}
	for i, res := range results {
		s := samples[i]
		e, _ := f.deps.Registry.Entry(s.Entry)
		q, _ := e.Quantization(s.Quantization)
		_, revision := q.WeightsRepo(e)
		if res.Outcome != registry.OutcomePassed || res.Entry != s.Entry || res.Quantization != s.Quantization || res.GPUKind != s.GPUKind ||
			res.GPUs != s.GPUs || res.Revision != revision || res.EngineVersion != engines.Current()[q.Engine] || res.Run != f.deps.RunURL || res.RanAt.IsZero() {
			t.Errorf("result %d: %+v for %s", i, res, s)
		}
	}
	if len(f.machines.created) != 1 || !slices.Equal(f.machines.deleted, []string{"4242"}) {
		t.Fatalf("created %d, deleted %v", len(f.machines.created), f.machines.deleted)
	}
	spec := f.machines.created[0]
	if spec.Size != "gpu-l40sx1-48gb" || spec.Region != "tor1" || spec.RunID != "test-run" || !strings.Contains(spec.UserData, "nvidia-container-toolkit") {
		t.Fatalf("machine spec %+v", spec.RunID)
	}
	if len(f.host.removed) != len(samples) {
		t.Fatalf("removed %d of %d containers", len(f.host.removed), len(samples))
	}
}

func TestRunRefusesTheWrongAccount(t *testing.T) {
	f := newRun(t)
	f.spend.account = "someone-else"
	_, err := Run(t.Context(), f.deps)
	if !errors.Is(err, ErrWrongAccount) || err.Error() != "this token isn't the qualification account" {
		t.Fatalf("err = %v", err)
	}
	if len(f.machines.created) != 0 || f.machines.sweeps != 0 {
		t.Fatalf("created %d, swept %d times", len(f.machines.created), f.machines.sweeps)
	}
}

func TestRunRefusesWithoutConfirm(t *testing.T) {
	f := newRun(t)
	f.deps.Confirm = false
	if _, err := Run(t.Context(), f.deps); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("err = %v", err)
	}
	if f.spend.reads != 0 || len(f.machines.created) != 0 || f.machines.sweeps != 0 {
		t.Fatal("touched the account without --confirm")
	}
}

func TestRunRefusesOverTheCap(t *testing.T) {
	f := newRun(t)
	f.spend.mtd = "140.00"
	f.deps.Week = "2026-W47" // nvidia-h100-80gb
	f.deps.Offers = fakeOffers{offer: Offer{Size: "gpu-h100x1-80gb", Image: "gpu-h100x1-base", GPUs: 1, HourlyUSD: 3.39}}
	_, err := Run(t.Context(), f.deps)
	var over *OverCapError
	if !errors.Is(err, ErrOverCap) || !errors.As(err, &over) || over.Cap != "monthly" {
		t.Fatalf("err = %v", err)
	}
	// 140 + 3.39 x 4 = 153.56, past 150.
	if !strings.Contains(err.Error(), "$13.56") || !strings.Contains(err.Error(), "$153.56") || !strings.Contains(err.Error(), "$150.00") {
		t.Fatalf("the refusal doesn't give the numbers: %v", err)
	}
	if len(f.machines.created) != 0 {
		t.Fatal("created a Droplet over the cap")
	}
	if len(f.report.NotRun) == 0 || f.report.Stopped == "" {
		t.Fatalf("report: %+v", f.report)
	}

	// The per-run cap holds too: 3.39 x 4 is fine under 25, 7.00 x 4 isn't.
	f = newRun(t)
	f.deps.Offers = fakeOffers{offer: Offer{Size: "gpu-l40sx1-48gb", Image: "gpu-h100x1-base", GPUs: 1, HourlyUSD: 7}}
	if _, err := Run(t.Context(), f.deps); !errors.As(err, &over) || over.Cap != "per-run" || len(f.machines.created) != 0 {
		t.Fatalf("err = %v, created %d", err, len(f.machines.created))
	}
}

func TestRunStopsAtTheCapMidway(t *testing.T) {
	f := newRun(t)
	samples := f.samples()
	if len(samples) < 3 {
		t.Fatalf("need three samples, have %d", len(samples))
	}
	ticks := make(chan time.Time, 1)
	f.deps.Ticks = ticks
	f.host.onRemove = func(n int) {
		if n == 2 {
			f.spend.set("150.00")
			ticks <- time.Now()
		}
	}
	results, err := Run(t.Context(), f.deps)
	if !errors.Is(err, ErrOverCap) {
		t.Fatalf("err = %v", err)
	}
	if len(results) != 2 || len(f.host.started) != 2 {
		t.Fatalf("%d results, %d started; want 2", len(results), len(f.host.started))
	}
	if len(f.report.NotRun) != len(samples)-2 || f.report.Stopped == "" {
		t.Fatalf("not run %d of %d, stopped %q", len(f.report.NotRun), len(samples)-2, f.report.Stopped)
	}
	if !slices.Equal(f.machines.deleted, []string{"4242"}) {
		t.Fatalf("deleted %v", f.machines.deleted)
	}
}

func TestRunStopsWhenTheTickerSeesTheCap(t *testing.T) {
	f := newRun(t)
	ticks := make(chan time.Time)
	f.deps.Ticks = ticks
	stopped := make(chan struct{})
	// The engine never gets healthy; only the ticker can end the sample.
	f.host.unhealthy = true
	f.host.onStart = func(n int) {
		if n == 1 {
			go func() {
				f.spend.set("151.00")
				ticks <- time.Now()
				close(stopped)
			}()
		}
	}
	results, err := Run(t.Context(), f.deps)
	<-stopped
	if !errors.Is(err, ErrOverCap) || len(results) != 0 {
		t.Fatalf("err = %v, %d results", err, len(results))
	}
	if len(f.report.NotRun) != len(f.samples()) {
		t.Fatalf("not run %d, want every sample: a stopped sample is never failed", len(f.report.NotRun))
	}
	if len(f.host.removed) != 1 || !slices.Equal(f.machines.deleted, []string{"4242"}) {
		t.Fatalf("removed %d, deleted %v", len(f.host.removed), f.machines.deleted)
	}
}

func TestBalanceFailureRefuses(t *testing.T) {
	f := newRun(t)
	f.spend.mtdErr = errors.New("digitalocean answered 500")
	_, err := Run(t.Context(), f.deps)
	if !errors.Is(err, ErrSpendUnknown) || len(f.machines.created) != 0 {
		t.Fatalf("err = %v, created %d", err, len(f.machines.created))
	}
	f = newRun(t)
	f.spend.mtd = "not a number"
	if _, err = Run(t.Context(), f.deps); !errors.Is(err, ErrSpendUnknown) || len(f.machines.created) != 0 {
		t.Fatalf("err = %v, created %d", err, len(f.machines.created))
	}
}

func TestRunAlwaysTearsDown(t *testing.T) {
	f := newRun(t)
	calls := 0
	f.deps.Probe = func(ctx context.Context, client *http.Client, e registry.Entry, c *Container) (string, error) {
		calls++
		if calls == 2 {
			panic("probe bug")
		}
		return Probe(ctx, client, e, c)
	}
	results, err := Run(t.Context(), f.deps)
	if err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Fatalf("err = %v", err)
	}
	if len(results) != 1 || results[0].Outcome != registry.OutcomePassed {
		t.Fatalf("results %+v; the finished sample's result should be kept", results)
	}
	if len(f.report.Results) != 1 {
		t.Fatalf("report has %d results", len(f.report.Results))
	}
	if !slices.Equal(f.machines.deleted, []string{"4242"}) || len(f.host.removed) != 2 {
		t.Fatalf("deleted %v, removed %d", f.machines.deleted, len(f.host.removed))
	}

	// A cancel mid-sample tears down too, and records nothing as failed.
	f = newRun(t)
	ctx, cancel := context.WithCancel(t.Context())
	f.host.onStart = func(int) { cancel() }
	results, err = Run(ctx, f.deps)
	if err == nil || len(results) != 0 || !slices.Equal(f.machines.deleted, []string{"4242"}) {
		t.Fatalf("err = %v, results %d, deleted %v", err, len(results), f.machines.deleted)
	}

	// A Create that fails still sweeps for what it may have made.
	f = newRun(t)
	f.machines.createErr = errors.New("digitalocean timed out")
	if _, err = Run(t.Context(), f.deps); err == nil || f.machines.sweeps != 2 {
		t.Fatalf("err = %v, swept %d times", err, f.machines.sweeps)
	}
}

func TestResultsNeverCarrySecrets(t *testing.T) {
	const hfToken = "hf_SecretTokenValue0123456789"
	f := newRun(t)
	f.deps.HFToken = hfToken
	f.host.exited = true
	var keys []string
	f.host.onStart = func(int) {
		f.host.mu.Lock()
		defer f.host.mu.Unlock()
		c := f.host.current
		keys = append(keys, c.Key)
		f.host.logs = []byte("loading weights\nHF_TOKEN=" + hfToken + " key=" + c.Key + "\nAuthorization: Bearer abc.def\nCUDA out of memory\n")
		for _, arg := range append(slices.Clone(c.Spec.Args), c.Spec.Command...) {
			if strings.Contains(arg, hfToken) {
				t.Errorf("the token is in the container's command line")
			}
		}
		for k, v := range c.Spec.Env {
			if strings.Contains(v, hfToken) {
				t.Errorf("the token is in %s", k)
			}
		}
		if strings.Contains(string(c.Files[recipes.ModelArgsPath]), hfToken) {
			t.Errorf("the token is in the args file")
		}
		if fmt.Sprintf("%v %#v %s", c, c, c) != c.Name+" "+c.Name+" "+c.Name {
			t.Errorf("a container prints more than its name")
		}
	}
	results, err := Run(t.Context(), f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	out, _ := json.Marshal(f.report)
	for _, secret := range append(keys, hfToken, "abc.def") {
		if strings.Contains(string(out), secret) {
			t.Fatalf("the report holds a secret:\n%s", out)
		}
	}
	for _, res := range results {
		if res.Outcome != registry.OutcomeFailed || !strings.Contains(res.Detail, "[redacted]") || !strings.Contains(res.Detail, "exited with code 1") ||
			!strings.Contains(res.Detail, "CUDA out of memory") {
			t.Fatalf("detail %q", res.Detail)
		}
	}
}

func TestNeverTestedWithoutAPassingProbe(t *testing.T) {
	f := newRun(t)
	f.deps.Probe = func(context.Context, *http.Client, registry.Entry, *Container) (string, error) {
		return "", errors.New("the chat completion came back empty")
	}
	results, err := Run(t.Context(), f.deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range results {
		if res.Outcome != registry.OutcomeFailed {
			t.Fatalf("%s %s recorded %s without a passing probe", res.Entry, res.Quantization, res.Outcome)
		}
	}

	// A start the host couldn't do isn't the model's failure: not run.
	f = newRun(t)
	f.deps.Connect = func(context.Context, *Machine, Keys) (Host, error) { return failingHost{f.host}, nil }
	results, err = Run(t.Context(), f.deps)
	if err != nil || len(results) != 0 || len(f.report.NotRun) != len(f.samples()) {
		t.Fatalf("err = %v, %d results, %d not run", err, len(results), len(f.report.NotRun))
	}
}

type failingHost struct{ *fakeHost }

func (failingHost) Start(context.Context, *Container) error { return errors.New("pull access denied") }

func TestContainerRunsTheKindAsDeployed(t *testing.T) {
	r := testRegistry(t)
	e, _ := r.Entry("acme/Small-1B-Instruct")
	q, _ := e.Quantization("bf16")
	s := Sample{Entry: e.ID, Quantization: q.ID, GPUKind: "nvidia-l40s", GPUs: 1}
	need, err := sampleNeed(e, q, s)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewContainer(e, q, s, need, "lpm_key", "hf_token_unused", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	composition, err := recipes.Default().Resolve(recipes.ModelVLLMKind, "0.31", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	engine := composition.Components[slices.IndexFunc(composition.Components, func(c recipes.ResolvedComponent) bool { return c.Name == recipes.ModelEngine })]
	if c.Spec.Image != engine.Image || !strings.Contains(c.Spec.Image, "@sha256:") {
		t.Fatalf("image %s, want %s", c.Spec.Image, engine.Image)
	}
	if !slices.Equal(c.Spec.Command, []string{"/bin/sh", "-c", recipes.ModelEngineWrapper, "loomplane-engine"}) || !slices.Equal(c.Spec.Args, []string{"vllm", "serve"}) {
		t.Fatalf("command %q args %q", c.Spec.Command, c.Spec.Args)
	}
	args, err := engines.Args(engines.Spec{Entry: e, Quantization: q, ServedName: e.ID, MaxModelLen: need.MaxModelLen, GPUs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(c.Files[recipes.ModelArgsPath]); got != strings.Join(args, "\n")+"\n" {
		t.Fatalf("args file %q", got)
	}
	if len(c.Files[recipes.ModelTokenPath]) != 0 {
		t.Fatal("an ungated model got the token")
	}
	if c.Spec.Env["VLLM_API_KEY"] != "lpm_key" || c.Spec.Env["VLLM_NO_USAGE_STATS"] != "1" || c.Spec.Env["HF_HOME"] != "/cache/huggingface" {
		t.Fatalf("env %v", c.Spec.Env)
	}
	var placement provider.GPUPlacement
	if jErr := json.Unmarshal([]byte(c.Labels[owned.LabelGPU]), &placement); jErr != nil || placement.Kind != "nvidia-l40s" || placement.Count != 1 {
		t.Fatalf("GPU label %q", c.Labels[owned.LabelGPU])
	}
	if c.Labels[owned.LabelShmMB] != "4096" || c.Spec.Resources.CPUMillis != 2500 || c.Spec.Resources.MemoryMB != 12288 {
		t.Fatalf("shm %s, resources %+v", c.Labels[owned.LabelShmMB], c.Spec.Resources)
	}
	if c.StartTimeout != 2*time.Hour || c.MaxModelLen != need.MaxModelLen {
		t.Fatalf("start timeout %s, max model len %d", c.StartTimeout, c.MaxModelLen)
	}
	if len(c.Spec.Volumes) != 1 || c.Spec.Volumes[0].MountPath != "/cache" || c.FileModes[recipes.ModelTokenPath] != 0o400 {
		t.Fatalf("volumes %+v, modes %v", c.Spec.Volumes, c.FileModes)
	}
	other, _ := NewContainer(e, q, s, need, "lpm_key", "", "run-1")
	if other.ID == c.ID || other.Name == c.Name {
		t.Fatal("two samples share a container, so they'd share a weight cache")
	}

	// A gated entry gets the token in its token file, and nowhere else.
	e.Gated = true
	gated, err := NewContainer(e, q, s, need, "lpm_key", "hf_gated", "run-1")
	if err != nil || string(gated.Files[recipes.ModelTokenPath]) != "hf_gated" {
		t.Fatalf("err = %v, token file %q", err, gated.Files[recipes.ModelTokenPath])
	}

	// TEI runs on CPU with its own kind.
	emb, _ := r.Entry("acme/Embed-Small")
	native, _ := emb.Quantization("native")
	cpu := Sample{Entry: emb.ID, Quantization: native.ID, GPUKind: registry.CPUKind}
	cpuNeed, _ := sampleNeed(emb, native, cpu)
	tei, err := NewContainer(emb, native, cpu, cpuNeed, "lpm_key", "", "run-1")
	if err != nil || tei.Kind != recipes.ModelTEIKind || tei.Labels[owned.LabelGPU] != "" {
		t.Fatalf("err = %v, kind %s, GPU %q", err, tei.Kind, tei.Labels[owned.LabelGPU])
	}
}

func TestCloudInitPinsTheHostKey(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userData, err := CloudInit(private)
	if err != nil {
		t.Fatal(err)
	}
	body, ok := strings.CutPrefix(userData, "#cloud-config\n")
	if !ok {
		t.Fatal("not a cloud-config")
	}
	var doc struct {
		Keys   map[string]string `json:"ssh_keys"`
		Delete bool              `json:"ssh_deletekeys"`
		RunCmd [][]string        `json:"runcmd"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	pub, _ := ssh.NewPublicKey(public)
	if doc.Keys["ed25519_public"] != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) || !doc.Delete {
		t.Fatalf("host key %q", doc.Keys["ed25519_public"])
	}
	if _, err := ssh.ParsePrivateKey([]byte(doc.Keys["ed25519_private"])); err != nil {
		t.Fatal(err)
	}
	if len(doc.RunCmd) == 0 || !slices.Equal(doc.RunCmd[0], []string{"bash", "-c", cloudinfra.NVIDIAToolkitScript}) {
		t.Fatalf("runcmd %q", doc.RunCmd)
	}
}

func TestGuard(t *testing.T) {
	cfg := testConfig()
	spend := &fakeSpend{mtd: "130.00"}
	cases := []struct {
		name          string
		hourly, hours float64
		spent         float64
		want          error
	}{
		{"fits", 4.41, 4, 0, nil},
		{"exactly the monthly cap", 5, 4, 0, nil},
		{"a cent past the monthly cap", 5.0025, 4, 0, ErrOverCap},
		{"exactly the run cap", 1, 5, 20, nil},
		{"past the run cap", 1, 5, 20.01, ErrOverCap},
		{"unknown price", 0, 4, 0, ErrSpendUnknown},
		{"no time left", 1, 0, 0, ErrOverCap},
	}
	for _, c := range cases {
		err := Guard(t.Context(), spend, cfg, c.hourly, c.hours, c.spent)
		if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	bad := cfg
	bad.MonthlyCapUSD = ""
	if err := Guard(t.Context(), spend, bad, 1, 1, 0); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("an empty cap: err = %v", err)
	}
}

func TestConfig(t *testing.T) {
	data, err := os.ReadFile("../../../.github/qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccountUUID != "" {
		t.Fatal("the committed config names an account; it comes from " + AccountEnv)
	}
	if vErr := cfg.Validate(); !errors.Is(vErr, ErrInvalidConfig) {
		t.Fatalf("a config without an account validated: %v", vErr)
	}
	if cfg, err = cfg.WithAccount("acct-qualify"); err != nil || cfg.Validate() != nil {
		t.Fatalf("err = %v, %v", err, cfg.Validate())
	}
	want := testConfig()
	if !slices.Equal(cfg.GPUKinds, want.GPUKinds) || cfg.MonthlyCapUSD != want.MonthlyCapUSD || cfg.RunCapUSD != want.RunCapUSD ||
		cfg.MaxRunHours != want.MaxRunHours || cfg.SamplesPerRun != want.SamplesPerRun || cfg.StaleAfterDays != want.StaleAfterDays || cfg.Region != want.Region {
		t.Fatalf("config %+v", cfg)
	}
	if _, err := cfg.WithAccount("another"); err == nil {
		t.Fatal("two accounts were accepted")
	}
	if _, err := LoadConfig(strings.NewReader("monthly_cap: 1\n")); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	over := want
	over.RunCapUSD = "200"
	if err := over.Validate(); err == nil {
		t.Fatal("a run cap past the monthly cap validated")
	}
}

// fakeDigitalOcean is DigitalOcean's API for Machines.
type fakeDigitalOcean struct {
	t        *testing.T
	mu       sync.Mutex
	droplets []droplet
	keys     []sshKey
	deleted  []string
	created  map[string]any
	keyAdded map[string]string
}

func (f *fakeDigitalOcean) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer do-token" {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/droplets":
		if r.URL.Query().Get("tag_name") != Tag {
			f.t.Errorf("listed Droplets without the tag filter: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"droplets": f.droplets})
	case r.Method == http.MethodGet && r.URL.Path == "/v2/account/keys":
		_ = json.NewEncoder(w).Encode(map[string]any{"ssh_keys": f.keys})
	case r.Method == http.MethodDelete:
		f.deleted = append(f.deleted, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/v2/account/keys":
		_ = json.NewDecoder(r.Body).Decode(&f.keyAdded)
		fmt.Fprint(w, `{"ssh_key":{"id":77,"name":"lpq-run-1"}}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v2/droplets":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		fmt.Fprint(w, `{"droplet":{"id":555,"status":"new"}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v2/droplets/555":
		fmt.Fprint(w, `{"droplet":{"id":555,"status":"active","networks":{"v4":[{"ip_address":"10.0.0.2","type":"private"},{"ip_address":"203.0.113.9","type":"public"}]}}}`)
	default:
		http.NotFound(w, r)
	}
}

func TestSweepDeletesEverythingTagged(t *testing.T) {
	now := time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC)
	past, future := strconv.FormatInt(now.Add(-time.Hour).Unix(), 10), strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	newAPI := func() (*fakeDigitalOcean, *DigitalOceanMachines) {
		f := &fakeDigitalOcean{t: t,
			droplets: []droplet{
				{ID: 1, Name: "lpq-old", Tags: []string{Tag, "lpq-run-old", "lpq-expires-" + past}},
				{ID: 2, Name: "lpq-live", Tags: []string{Tag, "lpq-run-live", "lpq-expires-" + future}},
				{ID: 3, Name: "lpq-noexpiry", Tags: []string{Tag, "lpq-run-noexpiry"}},
				{ID: 4, Name: "someone-elses", Tags: []string{"web"}},
			},
			keys: []sshKey{{ID: 11, Name: "lpq-old"}, {ID: 12, Name: "lpq-live"}, {ID: 13, Name: "rex-laptop"}},
		}
		srv := httptest.NewServer(http.HandlerFunc(f.handler))
		t.Cleanup(srv.Close)
		return f, NewDigitalOceanMachines(srv.URL, "do-token")
	}

	f, m := newAPI()
	deleted, err := m.Sweep(t.Context(), now, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/v2/droplets/1", "/v2/droplets/2", "/v2/droplets/3", "/v2/account/keys/11", "/v2/account/keys/12"}
	if !slices.Equal(f.deleted, want) || len(deleted) != len(want) {
		t.Fatalf("deleted %v (%v), want %v", f.deleted, deleted, want)
	}

	f, m = newAPI()
	if _, err = m.Sweep(t.Context(), now, true); err != nil {
		t.Fatal(err)
	}
	want = []string{"/v2/droplets/1", "/v2/droplets/3", "/v2/account/keys/11"}
	if !slices.Equal(f.deleted, want) {
		t.Fatalf("only expired: deleted %v, want %v", f.deleted, want)
	}

	// A wrong token never deletes anything, and its error holds no token.
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	_, err = NewDigitalOceanMachines(srv.URL, "wrong-token").Sweep(t.Context(), now, false)
	if err == nil || strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateTagsTheDroplet(t *testing.T) {
	f := &fakeDigitalOcean{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	expires := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	m, err := NewDigitalOceanMachines(srv.URL, "do-token").Create(t.Context(), MachineSpec{
		RunID: "run-1", Region: "tor1", Size: "gpu-l40sx1-48gb", Image: "gpu-h100x1-base", UserData: "#cloud-config\n{}", UserKey: signer.PublicKey(), Expires: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "555" || m.IPv4 != "203.0.113.9" || m.Name != "lpq-run-1" {
		t.Fatalf("machine %+v", m)
	}
	if f.keyAdded["name"] != "lpq-run-1" || !strings.HasPrefix(f.keyAdded["public_key"], "ssh-ed25519 ") {
		t.Fatalf("SSH key %v", f.keyAdded)
	}
	tags, _ := f.created["tags"].([]any)
	wantTags := []any{Tag, "lpq-run-run-1", "lpq-expires-" + strconv.FormatInt(expires.Unix(), 10)}
	if !slices.Equal(tags, wantTags) || f.created["name"] != "lpq-run-1" || f.created["user_data"] != "#cloud-config\n{}" {
		t.Fatalf("created %v", f.created)
	}
	if keys, _ := f.created["ssh_keys"].([]any); len(keys) != 1 || keys[0] != float64(77) {
		t.Fatalf("ssh_keys %v", f.created["ssh_keys"])
	}
	if fmt.Sprint(MachineSpec{UserData: "secret host key"}) != "[machine spec]" {
		t.Fatal("a machine spec prints its user data")
	}
}

func TestPickOffer(t *testing.T) {
	options := provider.ServerOptions{
		Sizes: []provider.ServerSize{
			{ID: "gpu-h100x8-640gb", Regions: []string{"tor1"}, HourlyUSD: 30, HourlyPriceKnown: true, DiskGB: 2000, Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Count: 8}},
			{ID: "gpu-h100x1-80gb", Regions: []string{"tor1"}, HourlyUSD: 3.39, HourlyPriceKnown: true, DiskGB: 720, Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Count: 1}},
		},
		Images: []provider.ServerImage{
			{ID: "ubuntu-24-04-x64", Regions: []string{"tor1"}, Bootstrap: true},
			{ID: "gpu-h100x1-base", Regions: []string{"tor1"}, Bootstrap: true, Accelerators: []string{provider.VendorNVIDIA}, MinDiskGB: 100},
		},
	}
	offer, err := PickOffer(options, "tor1", "nvidia-h100-80gb")
	if err != nil {
		t.Fatal(err)
	}
	// The reference price, 4.41, is higher than the live 3.39, so it's used.
	if offer.Size != "gpu-h100x1-80gb" || offer.GPUs != 1 || offer.Image != "gpu-h100x1-base" || offer.HourlyUSD != 4.41 {
		t.Fatalf("offer %+v", offer)
	}
	options.Sizes[1].HourlyPriceKnown = false
	if _, err := PickOffer(options, "tor1", "nvidia-h100-80gb"); err == nil {
		t.Fatal("a size with an unknown price was offered")
	}
	if _, err := PickOffer(options, "nyc2", "nvidia-h100-80gb"); err == nil {
		t.Fatal("a size outside the region was offered")
	}
}

func TestProbesPassAndFail(t *testing.T) {
	r := registry.Default()
	entry := func(id string) registry.Entry {
		e, ok := r.Entry(id)
		if !ok {
			t.Fatalf("no entry %s", id)
		}
		return e
	}
	var chat, embedding, rerank, speech registry.Entry
	for _, e := range r.Entries() {
		switch e.Tasks[0] {
		case registry.TaskChat:
			chat = e
		case registry.TaskEmbedding:
			embedding = e
		case registry.TaskRerank:
			rerank = e
		case registry.TaskSpeech:
			speech = e
		}
	}
	c := &Container{Key: "lpm_probe", Name: "lp-probe"}
	serve := func(h http.HandlerFunc) *http.Client {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("Authorization") != "Bearer lpm_probe" {
				http.Error(w, "no key", http.StatusUnauthorized)
				return
			}
			h(w, req)
		}))
		t.Cleanup(srv.Close)
		addr := srv.Listener.Addr().String()
		return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}}}
	}
	healthy := serve(func(w http.ResponseWriter, req *http.Request) { engineAnswer(w, req, r) })

	for _, e := range []registry.Entry{chat, embedding, rerank, speech} {
		probe := *c
		probe.ServedName = e.ID
		detail, err := Probe(t.Context(), healthy, entry(e.ID), &probe)
		if err != nil || detail == "" {
			t.Errorf("%s (%s): detail %q, err %v", e.ID, e.Tasks[0], detail, err)
		}
	}
	chatC := *c
	chatC.ServedName = chat.ID
	if detail, _ := Probe(t.Context(), healthy, chat, &chatC); !strings.HasPrefix(detail, "first token ") || !strings.HasSuffix(detail, " tokens/s") {
		t.Errorf("chat detail %q", detail)
	}

	failing := map[string]struct {
		e registry.Entry
		h http.HandlerFunc
	}{
		"chat empty": {chat, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\ndata: [DONE]\n\n")
		}},
		"chat 500": {chat, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }},
		"embedding dims": {embedding, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2]},{"embedding":[0.1,0.2]}]}`)
		}},
		"embedding one vector": {embedding, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": make([]float64, embedding.Embedding.Dimensions)}}})
		}},
		"rerank order": {rerank, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{"index":1,"score":0.9},{"index":0,"score":0.2}]`)
		}},
		"speech text": {speech, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"text":"hello there"}`) }},
	}
	for name, f := range failing {
		probe := *c
		probe.ServedName = f.e.ID
		if detail, err := Probe(t.Context(), serve(f.h), f.e, &probe); err == nil {
			t.Errorf("%s passed: %q", name, detail)
		}
	}
}

func TestSpeechRecordingIsShort(t *testing.T) {
	// A 16 kHz mono 16-bit WAV: 32000 bytes a second, plus the header.
	if len(speechWAV) < 1000 || string(speechWAV[:4]) != "RIFF" || string(speechWAV[8:12]) != "WAVE" || len(speechWAV) > 5*32000+1024 {
		t.Fatalf("speech.wav is %d bytes", len(speechWAV))
	}
}

const qualificationFile = `# Qualification results: kept by the qualification job.
results:
  # The first run.
  - entry: acme/Small-1B-Instruct
    quantization: bf16
    revision: 0123456789abcdef0123456789abcdef01234567
    gpu_kind: nvidia-l40s
    gpus: 1
    max_model_len: 32768
    engine: vllm
    engine_version: 0.31.0
    outcome: failed
    detail: the engine exited
    ran_at: 2026-11-02T06:12:00Z
    run: https://github.com/xraph/loomplane/actions/runs/1
`

func TestMergeKeepsTheNewestResult(t *testing.T) {
	var file struct {
		Results []registry.Result `yaml:"results"`
	}
	if err := yaml.Unmarshal([]byte(qualificationFile), &file); err != nil {
		t.Fatal(err)
	}
	old := file.Results[0]
	newer := old
	newer.Outcome, newer.Detail, newer.RanAt, newer.Run = registry.OutcomePassed, "first token 90 ms, 80 tokens/s", old.RanAt.AddDate(0, 0, 7), "https://github.com/xraph/loomplane/actions/runs/2"
	older := old
	older.Outcome, older.RanAt = registry.OutcomePassed, old.RanAt.AddDate(0, 0, -7)
	repinned := newer
	repinned.Revision = strings.Repeat("a", 40)
	otherKind := newer
	otherKind.GPUKind = "nvidia-h100-80gb"

	out, err := Merge([]byte(qualificationFile), []registry.Result{older, newer, repinned, otherKind})
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# Qualification results: kept by the qualification job.", "# The first run."} {
		if !strings.Contains(string(out), comment) {
			t.Fatalf("lost the comment %q:\n%s", comment, out)
		}
	}
	if err = yaml.Unmarshal(out, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Results) != 3 {
		t.Fatalf("%d results:\n%s", len(file.Results), out)
	}
	if got := file.Results[0]; got.Outcome != registry.OutcomePassed || !got.RanAt.Equal(newer.RanAt) || got.Run != newer.Run {
		t.Fatalf("the newer result didn't replace the old one in place: %+v", got)
	}
	if file.Results[1].Revision == old.Revision && file.Results[2].Revision == old.Revision {
		t.Fatal("the re-pinned result was folded into the old revision's")
	}

	// Merging an older result changes nothing; merging nothing changes nothing.
	again, err := Merge(out, []registry.Result{older})
	if err != nil || string(again) != string(out) {
		t.Fatalf("err = %v\n%s\n---\n%s", err, out, again)
	}

	// The empty file the registry ships merges into a block list that loads.
	empty, err := os.ReadFile("../registry/data/qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge(empty, []registry.Result{newer})
	if err != nil || !strings.Contains(string(merged), "\n  - entry: acme/Small-1B-Instruct\n") || !strings.HasPrefix(string(merged), "# Qualification results:") {
		t.Fatalf("err = %v\n%s", err, merged)
	}
}

func TestSummaryListsPassedAndFailed(t *testing.T) {
	rep := &Report{Kind: "nvidia-l40s", Size: "gpu-l40sx1-48gb", Week: week46, Run: "https://github.com/xraph/loomplane/actions/runs/7",
		Results: []registry.Result{
			{Entry: "a/one", Quantization: "bf16", GPUKind: "nvidia-l40s", GPUs: 1, Outcome: registry.OutcomePassed, Detail: "first token 90 ms, 80 tokens/s"},
			{Entry: "a/two", Quantization: "native", GPUKind: registry.CPUKind, Outcome: registry.OutcomeFailed, Detail: "the engine exited"},
		},
		NotRun: []Skipped{{Sample: Sample{Entry: "a/three", Quantization: "fp8", GPUKind: "nvidia-l40s", GPUs: 1}, Reason: "the run reached its cap"}},
	}
	got := Summary(rep)
	for _, want := range []string{"### Passed (1)", "a/one bf16 on 1 nvidia-l40s: first token 90 ms", "### Failed (1)", "a/two native on CPU", "### Not run (1)", "a/three fp8 on 1 nvidia-l40s: the run reached its cap"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary lacks %q:\n%s", want, got)
		}
	}
	for _, dash := range []string{"\u2014", "\u2013"} {
		if strings.Contains(got, dash) {
			t.Fatal("the summary has a dash it shouldn't")
		}
	}
}

func TestUnframeDockerLogs(t *testing.T) {
	frame := func(stream byte, s string) []byte {
		return append([]byte{stream, 0, 0, 0, 0, 0, 0, byte(len(s))}, s...)
	}
	raw := append(frame(1, "out\n"), frame(2, "err\n")...)
	got, err := unframe(strings.NewReader(string(raw)))
	if err != nil || string(got) != "out\nerr\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = unframe(strings.NewReader("plain terminal output"))
	if err != nil || string(got) != "plain terminal output" {
		t.Fatalf("got %q, %v", got, err)
	}
	_ = io.EOF
}
