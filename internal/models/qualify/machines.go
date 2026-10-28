package qualify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	cpprovider "github.com/xraph/ctrlplane/provider"
	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gpusizes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

// Tags and names every qualification resource carries, so a sweep finds
// what a crashed run left.
const (
	// Tag is on every Droplet the job creates.
	Tag = "loomplane-qualify"
	// runTagPrefix + run ID names the run that made a Droplet.
	runTagPrefix = "lpq-run-"
	// expiresTagPrefix + Unix seconds is when a Droplet's run must be over.
	expiresTagPrefix = "lpq-expires-"
	// namePrefix starts every Droplet and SSH key name: "lpq-<run>".
	namePrefix = "lpq-"
)

// Run IDs go into tags and names. A workflow run's is gh-<run id>-<attempt>,
// so the step after it can delete exactly that run's Droplet; one started
// by hand is newRunID's <yyyymmdd>t<hhmm>-<6 hex>.
var (
	runIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	workflowRunPattern = regexp.MustCompile(`^gh-[1-9]\d{0,19}-[1-9]\d{0,3}$`)
	localRunPattern    = regexp.MustCompile(`^\d{8}t\d{4}-[0-9a-f]{6}$`)
)

// CheckRunID refuses a run ID that isn't gh-<run id>-<attempt> or one
// newRunID makes.
func CheckRunID(id string) error {
	if !workflowRunPattern.MatchString(id) && !localRunPattern.MatchString(id) {
		return fmt.Errorf("%q isn't a run ID: use gh-<GitHub run id>-<attempt>, or leave it out to make one", id)
	}
	return nil
}

// KeyName names a run's SSH key: lpq-<run>.<unix seconds it was added>.
// DigitalOcean doesn't say when a key was added, so the name does, and a
// sweep can leave a key alone while its run may still be starting.
func KeyName(runID string, added time.Time) string {
	return namePrefix + runID + "." + strconv.FormatInt(added.Unix(), 10)
}

// keyRun reads a key name back: the run ID and when the key was added,
// zero when the name doesn't say. A name without lpq- has no run.
func keyRun(name string) (runID string, added time.Time) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return "", time.Time{}
	}
	if run, raw, found := strings.Cut(rest, "."); found {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return run, time.Unix(unix, 0)
		}
	}
	return rest, time.Time{}
}

// Machine is a Droplet a run created.
type Machine struct {
	ID   string
	Name string
	IPv4 string
}

// MachineSpec is the Droplet a run asks for. UserData holds the run's
// private host key, so a MachineSpec is never logged.
type MachineSpec struct {
	RunID    string
	Region   string
	Size     string
	Image    string
	UserData string
	UserKey  ssh.PublicKey
	Expires  time.Time
}

func (MachineSpec) String() string { return "[machine spec]" }

// GoString keeps the host key out of %#v.
func (s MachineSpec) GoString() string { return s.String() }

// Machines creates and deletes the run's Droplet.
type Machines interface {
	// Create adds UserKey as the SSH key lpq-<run>.<unix> and creates the
	// Droplet lpq-<run>, tagged loomplane-qualify, lpq-run-<run> and
	// lpq-expires-<unix>, then waits for its public address. A Machine
	// with an ID comes back with any error once the Droplet exists, so
	// the caller can delete it.
	Create(ctx context.Context, spec MachineSpec) (*Machine, error)
	// DeleteRun deletes one run's Droplets (tagged lpq-run-<run>) and its
	// SSH key, and nothing else, so a run's teardown never touches
	// another run.
	DeleteRun(ctx context.Context, runID string) ([]string, error)
	// Sweep deletes Droplets tagged loomplane-qualify (only expired ones
	// when asked) and SSH keys named lpq-* whose run has no Droplet left,
	// and says what it deleted.
	Sweep(ctx context.Context, now time.Time, onlyExpired bool) ([]string, error)
	// Live lists the loomplane-qualify Droplets whose expiry hasn't
	// passed: a run that may still be using them.
	Live(ctx context.Context, now time.Time) ([]string, error)
}

// Tags are a run's Droplet tags.
func Tags(runID string, expires time.Time) []string {
	return []string{Tag, runTagPrefix + runID, expiresTagPrefix + strconv.FormatInt(expires.Unix(), 10)}
}

// expired reports whether a Droplet's run is over. A Droplet without a
// readable expiry counts as expired: nothing can say it's still needed.
func expired(tags []string, now time.Time) bool {
	for _, t := range tags {
		if raw, ok := strings.CutPrefix(t, expiresTagPrefix); ok {
			unix, err := strconv.ParseInt(raw, 10, 64)
			return err != nil || !now.Before(time.Unix(unix, 0))
		}
	}
	return true
}

// doAPI calls DigitalOcean's API with one token. The token never appears in
// an error or a string.
type doAPI struct {
	endpoint string
	token    string
	http     *http.Client
}

func newDOAPI(endpoint, token string) doAPI {
	a := doAPI{endpoint: digitalocean.DefaultEndpoint, token: strings.TrimSpace(token), http: &http.Client{Timeout: time.Minute}}
	if endpoint != "" {
		a.endpoint = strings.TrimRight(endpoint, "/")
	}
	return a
}

// apiError is a DigitalOcean error answer.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("digitalocean answered %d: %s", e.status, e.message)
}

func notFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == http.StatusNotFound
}

func (a doAPI) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	u := a.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("digitalocean %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("digitalocean: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		msg := oneLine(string(data))
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			msg = oneLine(e.Message)
		}
		return &apiError{status: res.StatusCode, message: redact(msg, a.token)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// pages calls GET path page by page, up to maxPages, and hands each page's
// body to each. It stops when DigitalOcean names no next page.
func (a doAPI) pages(ctx context.Context, path string, query url.Values, each func(raw json.RawMessage) error) error {
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"200"}}
		for k, v := range query {
			q[k] = v
		}
		var raw json.RawMessage
		if err := a.do(ctx, http.MethodGet, path, q, nil, &raw); err != nil {
			return err
		}
		if err := each(raw); err != nil {
			return err
		}
		var links struct {
			Links struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		if err := json.Unmarshal(raw, &links); err != nil {
			return err
		}
		if links.Links.Pages.Next == "" {
			return nil
		}
	}
	return fmt.Errorf("digitalocean listed more than %d pages of %s", maxPages, path)
}

// maxPages bounds every listing.
const maxPages = 50

// DigitalOceanMachines is Machines on DigitalOcean's API. It acts only on
// Droplets tagged loomplane-qualify and SSH keys named lpq-*.
type DigitalOceanMachines struct {
	api  doAPI
	poll time.Duration
	boot time.Duration
	now  func() time.Time
	// KeyGrace is how long after it's added a run's SSH key is safe from
	// Sweep, so a sweep never takes the key of a run whose Droplet doesn't
	// exist yet. Zero is DefaultKeyGrace.
	KeyGrace time.Duration
}

// DefaultKeyGrace is the longest run (4.5 hours) and its expiry margin.
const DefaultKeyGrace = time.Duration(maxRunHours*float64(time.Hour)) + expiryMargin

// NewDigitalOceanMachines talks to endpoint, or DigitalOcean's own API when
// it's empty. The token never appears in an error or a string.
func NewDigitalOceanMachines(endpoint, token string) *DigitalOceanMachines {
	m := &DigitalOceanMachines{api: newDOAPI(endpoint, token), poll: 10 * time.Second, boot: 15 * time.Minute, now: time.Now}
	if endpoint != "" {
		m.poll = 10 * time.Millisecond
	}
	return m
}

func (*DigitalOceanMachines) String() string { return "DigitalOcean machines" }

type droplet struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Tags     []string `json:"tags"`
	Networks struct {
		V4 []struct {
			IP   string `json:"ip_address"`
			Type string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
}

func (d droplet) publicIPv4() string {
	for _, n := range d.Networks.V4 {
		if n.Type == "public" {
			return n.IP
		}
	}
	return ""
}

type sshKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Create implements Machines.
func (m *DigitalOceanMachines) Create(ctx context.Context, spec MachineSpec) (*Machine, error) {
	if !runIDPattern.MatchString(spec.RunID) || spec.Region == "" || spec.Size == "" || spec.Image == "" || spec.UserKey == nil {
		return nil, errors.New("a qualification Droplet needs a run ID, region, size, image and SSH key")
	}
	name := namePrefix + spec.RunID
	var key struct {
		SSHKey sshKey `json:"ssh_key"`
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spec.UserKey)))
	if err := m.api.do(ctx, http.MethodPost, "/v2/account/keys", nil, map[string]string{"name": KeyName(spec.RunID, m.now()), "public_key": public}, &key); err != nil {
		return nil, fmt.Errorf("add the run's SSH key: %w", err)
	}
	in := map[string]any{
		"name": name, "region": spec.Region, "size": spec.Size, "image": spec.Image,
		"ssh_keys": []int64{key.SSHKey.ID}, "user_data": spec.UserData, "tags": Tags(spec.RunID, spec.Expires),
		"backups": false, "ipv6": false, "monitoring": false,
	}
	var out struct {
		Droplet droplet `json:"droplet"`
	}
	if err := m.api.do(ctx, http.MethodPost, "/v2/droplets", nil, in, &out); err != nil {
		return nil, fmt.Errorf("create the Droplet: %w", err)
	}
	if out.Droplet.ID < 1 {
		return nil, errors.New("DigitalOcean created a Droplet without an ID")
	}
	machine := &Machine{ID: strconv.FormatInt(out.Droplet.ID, 10), Name: name}
	wait, cancel := context.WithTimeout(ctx, m.boot)
	defer cancel()
	for {
		var got struct {
			Droplet droplet `json:"droplet"`
		}
		if err := m.api.do(wait, http.MethodGet, "/v2/droplets/"+machine.ID, nil, nil, &got); err != nil && !notFound(err) {
			return machine, fmt.Errorf("read the Droplet: %w", err)
		}
		if ip := got.Droplet.publicIPv4(); got.Droplet.Status == "active" && ip != "" {
			machine.IPv4 = ip
			return machine, nil
		}
		select {
		case <-wait.Done():
			return machine, fmt.Errorf("the Droplet wasn't active with a public address within %s", m.boot)
		case <-time.After(m.poll):
		}
	}
}

// droplets lists the Droplets carrying tag, and drops any that don't
// carry loomplane-qualify too: the filter is DigitalOcean's, so it's
// checked here as well.
func (m *DigitalOceanMachines) droplets(ctx context.Context, tag string) ([]droplet, error) {
	var out []droplet
	err := m.api.pages(ctx, "/v2/droplets", url.Values{"tag_name": {tag}}, func(raw json.RawMessage) error {
		var page struct {
			Droplets []droplet `json:"droplets"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, d := range page.Droplets {
			if slices.Contains(d.Tags, Tag) && slices.Contains(d.Tags, tag) {
				out = append(out, d)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the qualification Droplets: %w", err)
	}
	return out, nil
}

// keys lists the SSH keys named lpq-*.
func (m *DigitalOceanMachines) keys(ctx context.Context) ([]sshKey, error) {
	var out []sshKey
	err := m.api.pages(ctx, "/v2/account/keys", nil, func(raw json.RawMessage) error {
		var page struct {
			SSHKeys []sshKey `json:"ssh_keys"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, k := range page.SSHKeys {
			if strings.HasPrefix(k.Name, namePrefix) {
				out = append(out, k)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list SSH keys: %w", err)
	}
	return out, nil
}

func (m *DigitalOceanMachines) deleteDroplet(ctx context.Context, d droplet) (string, error) {
	id := strconv.FormatInt(d.ID, 10)
	if err := m.api.do(ctx, http.MethodDelete, "/v2/droplets/"+id, nil, nil, nil); err != nil && !notFound(err) {
		return "", fmt.Errorf("delete Droplet %s: %w", id, err)
	}
	return fmt.Sprintf("Droplet %s (%s)", id, d.Name), nil
}

func (m *DigitalOceanMachines) deleteKey(ctx context.Context, k sshKey) (string, error) {
	id := strconv.FormatInt(k.ID, 10)
	if err := m.api.do(ctx, http.MethodDelete, "/v2/account/keys/"+id, nil, nil, nil); err != nil && !notFound(err) {
		return "", fmt.Errorf("delete SSH key %s: %w", id, err)
	}
	return fmt.Sprintf("SSH key %s (%s)", id, k.Name), nil
}

// DeleteRun implements Machines: the Droplets tagged lpq-run-<runID> and
// the run's SSH key, and nothing else. It keeps going past a failed
// delete and returns every failure.
func (m *DigitalOceanMachines) DeleteRun(ctx context.Context, runID string) ([]string, error) {
	if !runIDPattern.MatchString(runID) {
		return nil, fmt.Errorf("%q isn't a run ID", runID)
	}
	var deleted []string
	var problems []error
	ds, err := m.droplets(ctx, runTagPrefix+runID)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		what, dErr := m.deleteDroplet(ctx, d)
		if dErr != nil {
			problems = append(problems, dErr)
			continue
		}
		deleted = append(deleted, what)
	}
	ks, err := m.keys(ctx)
	if err != nil {
		return deleted, errors.Join(append(problems, err)...)
	}
	for _, k := range ks {
		if run, _ := keyRun(k.Name); run != runID {
			continue
		}
		what, kErr := m.deleteKey(ctx, k)
		if kErr != nil {
			problems = append(problems, kErr)
			continue
		}
		deleted = append(deleted, what)
	}
	return deleted, errors.Join(problems...)
}

// Sweep implements Machines. It keeps going past a failed delete, so one
// stuck Droplet doesn't keep the rest running, and returns every failure.
// A run's SSH key goes only once its Droplets do, and never within
// KeyGrace of being added, while its Droplet may not exist yet.
func (m *DigitalOceanMachines) Sweep(ctx context.Context, now time.Time, onlyExpired bool) ([]string, error) {
	var deleted []string
	var problems []error
	live := map[string]bool{} // run IDs of Droplets kept
	ds, err := m.droplets(ctx, Tag)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		if onlyExpired && !expired(d.Tags, now) {
			for _, t := range d.Tags {
				if run, ok := strings.CutPrefix(t, runTagPrefix); ok {
					live[run] = true
				}
			}
			continue
		}
		what, dErr := m.deleteDroplet(ctx, d)
		if dErr != nil {
			problems = append(problems, dErr)
			continue
		}
		deleted = append(deleted, what)
	}
	ks, err := m.keys(ctx)
	if err != nil {
		return deleted, errors.Join(append(problems, err)...)
	}
	grace := m.KeyGrace
	if grace <= 0 {
		grace = DefaultKeyGrace
	}
	for _, k := range ks {
		run, added := keyRun(k.Name)
		if live[run] || (!added.IsZero() && now.Sub(added) < grace) {
			continue
		}
		what, kErr := m.deleteKey(ctx, k)
		if kErr != nil {
			problems = append(problems, kErr)
			continue
		}
		deleted = append(deleted, what)
	}
	return deleted, errors.Join(problems...)
}

// Live implements Machines: every loomplane-qualify Droplet whose expiry
// hasn't passed.
func (m *DigitalOceanMachines) Live(ctx context.Context, now time.Time) ([]string, error) {
	ds, err := m.droplets(ctx, Tag)
	if err != nil {
		return nil, err
	}
	var live []string
	for _, d := range ds {
		if !expired(d.Tags, now) {
			live = append(live, fmt.Sprintf("Droplet %d (%s)", d.ID, d.Name))
		}
	}
	return live, nil
}

// Offer is the Droplet a run buys for a GPU kind.
type Offer struct {
	Size      string
	Image     string
	GPUs      int
	HourlyUSD float64
}

// Offers finds the Droplet a run buys.
type Offers interface {
	Offer(ctx context.Context, region, kind string) (Offer, error)
}

// DigitalOceanOffers reads sizes, prices and images from DigitalOcean.
type DigitalOceanOffers struct {
	client  *digitalocean.Client
	token   string
	account string
}

// NewDigitalOceanOffers reads offers as account with token.
func NewDigitalOceanOffers(client *digitalocean.Client, token, account string) *DigitalOceanOffers {
	return &DigitalOceanOffers{client: client, token: token, account: account}
}

func (*DigitalOceanOffers) String() string { return "DigitalOcean offers" }

// Offer implements Offers: the one-GPU size of kind in region with a known
// hourly price, and the first GPU-ready image there that fits its disk.
// The price is the live one or Loomplane's reference price, whichever is
// higher, so a stale table never makes a run look cheaper than it is.
func (o *DigitalOceanOffers) Offer(ctx context.Context, region, kind string) (Offer, error) {
	options, err := o.client.ServerOptions(ctx, provider.InfrastructureCredential{Provider: "digitalocean", AccountID: o.account, Token: o.token})
	if err != nil {
		return Offer{}, err
	}
	return PickOffer(options, region, kind)
}

// PickOffer chooses a run's Droplet from what an account offers.
func PickOffer(options provider.ServerOptions, region, kind string) (Offer, error) {
	var size *provider.ServerSize
	for i, s := range options.Sizes {
		a := s.Accelerator
		if a == nil || a.Kind != kind || a.Count != DropletGPUs || !slices.Contains(s.Regions, region) || !s.HourlyPriceKnown || s.HourlyUSD <= 0 {
			continue
		}
		size = &options.Sizes[i]
		break
	}
	if size == nil {
		return Offer{}, fmt.Errorf("DigitalOcean offers no one-GPU %s Droplet with a known price in %s", kind, region)
	}
	offer := Offer{Size: size.ID, GPUs: DropletGPUs, HourlyUSD: size.HourlyUSD}
	if ref, _, ok := gpusizes.Lookup(size.ID); ok && ref.HourlyPriceKnown && ref.HourlyUSD > offer.HourlyUSD {
		offer.HourlyUSD = ref.HourlyUSD
	}
	for _, im := range options.Images {
		if im.Bootstrap && slices.Contains(im.Regions, region) && slices.Contains(im.Accelerators, provider.VendorNVIDIA) && im.MinDiskGB <= size.DiskGB {
			offer.Image = im.ID
			break
		}
	}
	if offer.Image == "" {
		return Offer{}, fmt.Errorf("DigitalOcean offers no GPU-ready image for %s in %s", size.ID, region)
	}
	return offer, nil
}

// dockerHost is Host on a Droplet's Docker daemon over SSH, through the
// same server.Runtime Loomplane deploys server targets with.
type dockerHost struct {
	cfg     *server.Config
	client  *server.Client
	runtime *server.Runtime

	mu     sync.Mutex
	values map[string]owned.RuntimeValues // by instance ID
}

// ConnectTimeout bounds waiting for a new Droplet's SSH, Docker and GPUs.
const ConnectTimeout = 30 * time.Minute

// Connect waits for m's SSH server, pinned to the run's host key, then for
// Docker to answer and hand out its GPUs (cloud-init installs NVIDIA's
// toolkit first), and returns the Host on it.
func Connect(ctx context.Context, m *Machine, keys Keys) (Host, error) {
	if m == nil || net.ParseIP(m.IPv4) == nil {
		return nil, errors.New("the Droplet has no public address")
	}
	cfg := &server.Config{
		Address: net.JoinHostPort(m.IPv4, "22"), User: "root", Socket: "/var/run/docker.sock",
		HostKey: keys.Host, Signer: keys.User,
		Details: map[string]string{"host": m.IPv4, "port": "22", "user": "root", "docker_socket": "/var/run/docker.sock", "advertise_host": m.IPv4},
	}
	wait, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	var last error
	for {
		client, err := connectOnce(wait, cfg)
		if err == nil {
			h := &dockerHost{cfg: cfg, client: client, values: map[string]owned.RuntimeValues{}}
			h.runtime = &server.Runtime{
				Session: func(ctx context.Context, _ map[string]string) (*server.Client, error) { return server.Dial(ctx, cfg) },
				Values:  h.runtimeValues,
			}
			return h, nil
		}
		last = err
		select {
		case <-wait.Done():
			return nil, fmt.Errorf("the Droplet wasn't ready within %s: %w", ConnectTimeout, last)
		case <-time.After(15 * time.Second):
		}
	}
}

// connectOnce dials once and checks Docker and the GPUs.
func connectOnce(ctx context.Context, cfg *server.Config) (*server.Client, error) {
	client, err := server.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	details, missing, err := client.Check(ctx)
	if err == nil && len(missing) > 0 {
		err = fmt.Errorf("docker isn't ready: %s", strings.Join(missing, ", "))
	}
	if err == nil {
		cfg.Details["docker_id"], cfg.Details["architecture"] = details["docker_id"], details["architecture"]
		var inv provider.GPUInventory
		if inv, err = client.GPUs(ctx); err == nil && inv.Status != provider.GPUStatusReady {
			err = fmt.Errorf("the GPUs aren't ready for Docker: %s", inv.Message)
		}
	}
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (h *dockerHost) runtimeValues(_ context.Context, labels map[string]string) (owned.RuntimeValues, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.values[labels[owned.LabelInstance]]
	if !ok {
		return owned.RuntimeValues{}, errors.New("no rendered files for this container")
	}
	return v, nil
}

func (h *dockerHost) Start(ctx context.Context, c *Container) error {
	h.mu.Lock()
	h.values[c.ID.String()] = c.RuntimeValues()
	h.mu.Unlock()
	_, err := h.runtime.Apply(ctx, c.ID, c.Spec, c.Labels)
	return err
}

func (h *dockerHost) Dial(ctx context.Context, c *Container, port int) (net.Conn, error) {
	return h.client.DialContainerPort(ctx, c.Name, port)
}

func (h *dockerHost) State(ctx context.Context, c *Container) (running bool, exitCode int, err error) {
	res, err := h.client.Request(ctx, http.MethodGet, "/containers/"+url.PathEscape(c.Name)+"/json", nil)
	if err != nil {
		return false, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, 0, fmt.Errorf("docker answered HTTP %d", res.StatusCode)
	}
	var info struct {
		State struct {
			Running  bool
			ExitCode int
		}
	}
	if dErr := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&info); dErr != nil {
		return false, 0, dErr
	}
	return info.State.Running, info.State.ExitCode, nil
}

// Logs reads the end of the container's output, stdout and stderr
// together, unframing Docker's multiplexed stream.
func (h *dockerHost) Logs(ctx context.Context, c *Container) ([]byte, error) {
	res, err := h.client.Request(ctx, http.MethodGet, "/containers/"+url.PathEscape(c.Name)+"/logs?stdout=1&stderr=1&tail=200", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker answered HTTP %d", res.StatusCode)
	}
	return unframe(io.LimitReader(res.Body, 1<<20))
}

// unframe joins a Docker log stream's frames: each has an eight-byte
// header whose first byte names the stream and last four give the length.
// A stream that isn't framed is a terminal's, returned as it is.
func unframe(r io.Reader) ([]byte, error) {
	reader := bufio.NewReader(r)
	var out bytes.Buffer
	for {
		header, err := reader.Peek(8)
		if len(header) == 0 && errors.Is(err, io.EOF) {
			return out.Bytes(), nil
		}
		if err != nil || header[0] > 2 || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			_, cErr := io.Copy(&out, reader)
			return out.Bytes(), cErr
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, dErr := reader.Discard(8); dErr != nil {
			return out.Bytes(), dErr
		}
		if _, cErr := io.CopyN(&out, reader, size); cErr != nil && !errors.Is(cErr, io.EOF) {
			return out.Bytes(), cErr
		}
	}
}

// Remove deletes the container, its network and its weight volume, so the
// next sample downloads its weights afresh.
func (h *dockerHost) Remove(ctx context.Context, c *Container) error {
	err := h.runtime.Remove(ctx, c.ID, c.Labels)
	for _, v := range c.Spec.Volumes {
		res, rErr := h.client.Request(ctx, http.MethodDelete, "/volumes/"+server.VolumeName(c.ID, v.Name)+"?force=1", nil)
		if rErr != nil {
			err = errors.Join(err, rErr)
			continue
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
			err = errors.Join(err, fmt.Errorf("delete volume: docker answered HTTP %d", res.StatusCode))
		}
	}
	h.mu.Lock()
	delete(h.values, c.ID.String())
	h.mu.Unlock()
	return err
}

func (h *dockerHost) Close() error { return h.client.Close() }

// metadataProbe tries DigitalOcean's metadata service from a container
// and says which way it went. It passes only when wget ran and was
// refused: a missing wget or anything unexpected fails the check.
const metadataProbe = `if wget -q -T 5 -O /dev/null http://169.254.169.254/metadata/v1/id 2>/dev/null; then echo LPQ_METADATA_REACHABLE; exit 3; fi
command -v wget >/dev/null 2>&1 || { echo LPQ_NO_WGET; exit 4; }
echo LPQ_METADATA_BLOCKED`

// metadataInstance names the probe's (empty) rendered values.
const metadataInstance = "lpq-metadata-probe"

// MetadataBlocked starts a throwaway container from the guard's pinned
// image, on the network the engines get, and checks it can't reach the
// metadata service. Anything but a clean refusal is an error, so the run
// stops before a model's code could read the user data.
func (h *dockerHost) MetadataBlocked(ctx context.Context) error {
	version := ""
	for _, r := range engines.Releases() {
		if r.Engine == registry.EngineVLLM {
			version = r.Version
		}
	}
	composition, err := recipes.Default().Resolve(recipes.ModelVLLMKind, version, "amd64", "server")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(composition.Components, func(c recipes.ResolvedComponent) bool { return c.Name == recipes.ModelGuard })
	if i < 0 {
		return errors.New("the model kinds have no guard image to probe with")
	}
	h.mu.Lock()
	h.values[metadataInstance] = owned.RuntimeValues{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.values, metadataInstance)
		h.mu.Unlock()
	}()
	labels := map[string]string{
		owned.LabelOrg: labelOrg, owned.LabelProject: labelProject, owned.LabelEnvironment: "metadata-probe",
		owned.LabelConnection: "lpq-metadata-probe", owned.LabelInstance: metadataInstance, owned.LabelRevision: "1",
	}
	b := make([]byte, 6)
	if _, rErr := rand.Read(b); rErr != nil {
		return rErr
	}
	spec := cpprovider.ServiceSpec{
		Name: "metadata-probe", Image: composition.Components[i].Image, Command: []string{"/bin/sh", "-c", metadataProbe},
		Resources: cpprovider.ResourceSpec{CPUMillis: 100, MemoryMB: 64},
	}
	res, err := h.runtime.RunJob(ctx, fmt.Sprintf("lp-job-%x", b), spec, labels, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("couldn't check containers are kept off the metadata service: %w", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "LPQ_METADATA_BLOCKED") {
		return fmt.Errorf("a container on the Droplet can reach the metadata service, or the check couldn't tell (exit %d: %s)", res.ExitCode, oneLine(res.Output))
	}
	return nil
}

// GPUKinds reads the kind of every GPU Docker can hand out on the Droplet,
// from nvidia-smi's own product names.
func (h *dockerHost) GPUKinds(ctx context.Context) ([]string, error) {
	inv, err := h.client.GPUs(ctx)
	if err != nil {
		return nil, err
	}
	if inv.Status != provider.GPUStatusReady {
		return nil, fmt.Errorf("the GPUs aren't ready for Docker: %s", inv.Message)
	}
	kinds := make([]string, 0, len(inv.Devices))
	for _, d := range inv.Devices {
		kinds = append(kinds, d.Kind)
	}
	return kinds, nil
}
