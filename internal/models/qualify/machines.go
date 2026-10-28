package qualify

import (
	"bufio"
	"bytes"
	"context"
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

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gpusizes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
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

// runIDPattern is what a run ID may be: it goes into tags and names.
var runIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

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
	// Create adds UserKey as the SSH key lpq-<run> and creates the
	// Droplet lpq-<run>, tagged loomplane-qualify, lpq-run-<run> and
	// lpq-expires-<unix>, then waits for its public address. A Machine
	// with an ID comes back with any error once the Droplet exists, so
	// the caller can delete it.
	Create(ctx context.Context, spec MachineSpec) (*Machine, error)
	Delete(ctx context.Context, id string) error
	// Sweep deletes Droplets tagged loomplane-qualify (only expired ones
	// when asked) and SSH keys named lpq-*, and says what it deleted.
	Sweep(ctx context.Context, now time.Time, onlyExpired bool) ([]string, error)
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

// DigitalOceanMachines is Machines on DigitalOcean's API. It acts only on
// Droplets tagged loomplane-qualify and SSH keys named lpq-*.
type DigitalOceanMachines struct {
	endpoint string
	token    string
	http     *http.Client
	poll     time.Duration
	boot     time.Duration
}

// NewDigitalOceanMachines talks to endpoint, or DigitalOcean's own API when
// it's empty. The token never appears in an error or a string.
func NewDigitalOceanMachines(endpoint, token string) *DigitalOceanMachines {
	m := &DigitalOceanMachines{endpoint: digitalocean.DefaultEndpoint, token: strings.TrimSpace(token), http: &http.Client{Timeout: time.Minute}, poll: 10 * time.Second, boot: 15 * time.Minute}
	if endpoint != "" {
		m.endpoint, m.poll = strings.TrimRight(endpoint, "/"), 10*time.Millisecond
	}
	return m
}

func (*DigitalOceanMachines) String() string { return "DigitalOcean machines" }

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

func (m *DigitalOceanMachines) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	u := m.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := m.http.Do(req)
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
		return &apiError{status: res.StatusCode, message: redact(msg, m.token)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

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
	if err := m.do(ctx, http.MethodPost, "/v2/account/keys", nil, map[string]string{"name": name, "public_key": public}, &key); err != nil {
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
	if err := m.do(ctx, http.MethodPost, "/v2/droplets", nil, in, &out); err != nil {
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
		if err := m.do(wait, http.MethodGet, "/v2/droplets/"+machine.ID, nil, nil, &got); err != nil && !notFound(err) {
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

// Delete implements Machines. A Droplet that's already gone is deleted.
func (m *DigitalOceanMachines) Delete(ctx context.Context, id string) error {
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n < 1 || strconv.FormatInt(n, 10) != id {
		return fmt.Errorf("%q isn't a Droplet ID", id)
	}
	err := m.do(ctx, http.MethodDelete, "/v2/droplets/"+id, nil, nil, nil)
	if err != nil && !notFound(err) {
		return fmt.Errorf("delete Droplet %s: %w", id, err)
	}
	return nil
}

// Sweep implements Machines. It keeps going past a failed delete, so one
// stuck Droplet doesn't keep the rest running, and returns every failure.
func (m *DigitalOceanMachines) Sweep(ctx context.Context, now time.Time, onlyExpired bool) ([]string, error) {
	var deleted []string
	var problems []error
	live := map[string]bool{} // run IDs of Droplets kept
	for page := 1; ; page++ {
		var out struct {
			Droplets []droplet `json:"droplets"`
			Links    struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		q := url.Values{"tag_name": {Tag}, "page": {strconv.Itoa(page)}, "per_page": {"200"}}
		if err := m.do(ctx, http.MethodGet, "/v2/droplets", q, nil, &out); err != nil {
			return deleted, fmt.Errorf("list the qualification Droplets: %w", err)
		}
		for _, d := range out.Droplets {
			// The tag filter is DigitalOcean's; check it here too.
			if !slices.Contains(d.Tags, Tag) {
				continue
			}
			if onlyExpired && !expired(d.Tags, now) {
				for _, t := range d.Tags {
					if run, ok := strings.CutPrefix(t, runTagPrefix); ok {
						live[run] = true
					}
				}
				continue
			}
			id := strconv.FormatInt(d.ID, 10)
			if err := m.Delete(ctx, id); err != nil {
				problems = append(problems, err)
				continue
			}
			deleted = append(deleted, fmt.Sprintf("Droplet %s (%s)", id, d.Name))
		}
		if out.Links.Pages.Next == "" || page >= 100 {
			break
		}
	}
	for page := 1; ; page++ {
		var out struct {
			SSHKeys []sshKey `json:"ssh_keys"`
			Links   struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"200"}}
		if err := m.do(ctx, http.MethodGet, "/v2/account/keys", q, nil, &out); err != nil {
			return deleted, errors.Join(append(problems, fmt.Errorf("list SSH keys: %w", err))...)
		}
		for _, k := range out.SSHKeys {
			run, ok := strings.CutPrefix(k.Name, namePrefix)
			if !ok || live[run] {
				continue
			}
			id := strconv.FormatInt(k.ID, 10)
			if err := m.do(ctx, http.MethodDelete, "/v2/account/keys/"+id, nil, nil, nil); err != nil && !notFound(err) {
				problems = append(problems, fmt.Errorf("delete SSH key %s: %w", id, err))
				continue
			}
			deleted = append(deleted, fmt.Sprintf("SSH key %s (%s)", id, k.Name))
		}
		if out.Links.Pages.Next == "" || page >= 100 {
			break
		}
	}
	return deleted, errors.Join(problems...)
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
