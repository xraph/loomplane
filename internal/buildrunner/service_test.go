package buildrunner

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// memStore is an in-memory Store.
type memStore struct {
	mu      sync.Mutex
	pools   map[string]Pool
	members map[string]Member
}

func newMemStore() *memStore {
	return &memStore{pools: map[string]Pool{}, members: map[string]Member{}}
}

func (m *memStore) CreatePool(_ context.Context, p *Pool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, q := range m.pools {
		if q.OrgID == p.OrgID && q.Name == p.Name {
			return ErrConflict
		}
	}
	m.pools[p.ID] = *p
	return nil
}
func (m *memStore) GetPool(_ context.Context, id string) (*Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pools[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &p, nil
}
func (m *memStore) PoolByConnection(_ context.Context, org, conn string) (*Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pools {
		if p.OrgID == org && p.ConnectionID == conn && conn != "" {
			return &p, nil
		}
	}
	return nil, ErrNotFound
}
func (m *memStore) ListPools(_ context.Context, org string) ([]Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Pool{}
	for _, p := range m.pools {
		if p.OrgID == org {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (m *memStore) UpdatePool(_ context.Context, p *Pool, expected int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pools[p.ID].Revision != expected {
		return ErrConflict
	}
	m.pools[p.ID] = *p
	return nil
}
func (m *memStore) DeletePool(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pools, id)
	return nil
}
func (m *memStore) AddMember(_ context.Context, mem *Member) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members[mem.NodeID] = *mem
	return nil
}
func (m *memStore) GetMember(_ context.Context, id string) (*Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &mem, nil
}
func (m *memStore) ListMembers(_ context.Context, pool string) ([]Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Member{}
	for _, mem := range m.members {
		if mem.PoolID == pool {
			out = append(out, mem)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out, nil
}
func (m *memStore) UpdateMember(_ context.Context, mem *Member) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members[mem.NodeID] = *mem
	return nil
}
func (m *memStore) DeleteMember(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.members, id)
	return nil
}
func (m *memStore) RecordBuild(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem := m.members[id]
	mem.Builds++
	mem.LastBuildAt = &at
	m.members[id] = mem
	return nil
}

// fakePublisher records each pool connection the service publishes.
type fakePublisher struct {
	mu        sync.Mutex
	published map[string]map[string]string
	deleted   []string
}

func (f *fakePublisher) PublishRunnerPool(_ context.Context, org, id, name, _ string, details map[string]string) (*connections.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.published == nil {
		f.published = map[string]map[string]string{}
	}
	f.published[id] = details
	return &connections.Connection{ID: id, OrgID: org, Name: name, Details: details}, nil
}
func (f *fakePublisher) DeleteRunnerPool(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakePublisher) details(id string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published[id]
}

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// fakeNodes are joined nodes by ID.
type fakeNodes map[string]*nodes.Node

func (f fakeNodes) Get(_ context.Context, org, id string) (*nodes.Node, error) {
	n, ok := f[id]
	if !ok || n.OrgID != org {
		return nil, nodes.ErrNotFound
	}
	return n, nil
}

// fakeStreams answer each runner's Firecracker capabilities with what it
// shows, and record relay targets.
type fakeStreams struct {
	mu        sync.Mutex
	servers   map[string]string
	relayed   []string
	relayFail map[string]error
}

func (f *fakeStreams) serve(t *testing.T, nodeID string, caps fc.Capabilities) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/capabilities" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(caps)
	}))
	t.Cleanup(server.Close)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.servers == nil {
		f.servers = map[string]string{}
	}
	f.servers[nodeID] = strings.TrimPrefix(server.URL, "http://")
}

func (f *fakeStreams) Open(ctx context.Context, _, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	f.mu.Lock()
	addr := f.servers[nodeID]
	if hdr.Endpoint == nodeproto.EndpointRelay {
		if err := f.relayFail[nodeID]; err != nil {
			f.mu.Unlock()
			return nil, err
		}
		f.relayed = append(f.relayed, nodeID+"->"+hdr.Target)
	}
	f.mu.Unlock()
	if addr == "" {
		return nil, errors.New("offline")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}

func capsOf(node, identity string) fc.Capabilities {
	return fc.Capabilities{Version: fc.ProtocolVersion, HostID: node, Architecture: "amd64", RuntimeIdentity: identity, Isolation: true}
}

type rig struct {
	svc     *Service
	store   *memStore
	pub     *fakePublisher
	nodes   fakeNodes
	streams *fakeStreams
}

var owner = security.Principal{UserID: "usr_1", OrgID: "org_1"}

func newRig() *rig {
	r := &rig{store: newMemStore(), pub: &fakePublisher{}, nodes: fakeNodes{}, streams: &fakeStreams{}}
	r.svc = &Service{Streams: r.streams, Nodes: r.nodes, Connections: r.pub, Authz: allowAll{}}
	r.svc.Bind(r.store)
	return r
}

// runner joins nodeID to pool and marks it online.
func (r *rig) runner(t *testing.T, pool *Pool, nodeID, identity string) {
	t.Helper()
	if _, err := r.svc.JoinRunner(t.Context(), pool.OrgID, pool.ID, nodeID); err != nil {
		t.Fatal(err)
	}
	r.nodes[nodeID] = &nodes.Node{ID: nodeID, OrgID: pool.OrgID, Name: nodeID, Role: "runner", Status: nodes.StatusOnline}
	r.streams.serve(t, nodeID, capsOf(nodeID, identity))
	r.svc.RunnerOnline(t.Context(), r.nodes[nodeID])
}

func TestCreatePoolPublishesAConnectionThatIsNotReadyYet(t *testing.T) {
	r := newRig()
	pool, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pool.ID, "rpool_") || !strings.HasPrefix(pool.ConnectionID, "conn_") || pool.Mode != ModeSelfHosted || pool.Revision != 1 {
		t.Fatalf("pool %+v", pool)
	}
	d := r.pub.details(pool.ConnectionID)
	if d["pool_id"] != pool.ID || d["isolation"] != "firecracker" || d["build_isolation"] != "" || d["runtime_identity"] != "" {
		t.Fatalf("details %v", d)
	}
}

func TestCreatePoolRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   PoolInput
	}{
		{"a name that isn't a DNS label", PoolInput{Name: "Builds!", Scope: ScopeOrganization}},
		{"a project pool without its project", PoolInput{Name: "builds", Scope: ScopeProject}},
		{"a platform pool from an organization", PoolInput{Name: "builds", Scope: ScopePlatform}},
		{"a self-hosted pool with a cloud account", PoolInput{Name: "builds", Scope: ScopeOrganization, Settings: PoolSettings{Provider: "digitalocean"}}},
		{"an on-demand pool without sizes", PoolInput{Name: "builds", Scope: ScopeOrganization, Mode: ModeOnDemand, Settings: PoolSettings{Provider: "digitalocean", CloudConnectionID: "conn_do", Region: "nyc3", MaxRunners: 2}}},
		{"more warm runners than the pool may run", PoolInput{Name: "builds", Scope: ScopeOrganization, Mode: ModeOnDemand, Settings: PoolSettings{Provider: "digitalocean", CloudConnectionID: "conn_do", Region: "nyc3", Sizes: []string{"s-2vcpu-4gb-amd"}, MaxRunners: 1, MinWarm: 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newRig().svc.CreatePool(t.Context(), owner, tc.in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestFirstCheckedRunnerPinsThePool(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	identity := strings.Repeat("a", 64)
	r.runner(t, pool, "node_1", identity)
	pinned, _ := r.store.GetPool(t.Context(), pool.ID)
	if pinned.RuntimeIdentity != identity || pinned.Architecture != "amd64" || pinned.Revision != 2 {
		t.Fatalf("pool %+v", pinned)
	}
	d := r.pub.details(pool.ConnectionID)
	if d["build_isolation"] != "ready" || d["runtime_identity"] != identity || d["pool_revision"] != "2" {
		t.Fatalf("details %v", d)
	}
	runners, err := r.svc.BuildRunners(t.Context(), "org_1", pool.ConnectionID)
	if err != nil || len(runners) != 1 || runners[0] != "node_1" {
		t.Fatalf("runners %v %v", runners, err)
	}
}

func TestRunnerWithAnotherToolchainTakesNoBuilds(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	r.runner(t, pool, "node_1", strings.Repeat("a", 64))
	r.runner(t, pool, "node_2", strings.Repeat("b", 64))
	m, _ := r.store.GetMember(t.Context(), "node_2")
	if m.Reason == "" {
		t.Fatal("a runner with another toolchain has no reason recorded")
	}
	runners, _ := r.svc.BuildRunners(t.Context(), "org_1", pool.ConnectionID)
	if len(runners) != 1 || runners[0] != "node_1" {
		t.Fatalf("runners %v", runners)
	}
	pinned, _ := r.store.GetPool(t.Context(), pool.ID)
	if pinned.RuntimeIdentity != strings.Repeat("a", 64) || pinned.Revision != 2 {
		t.Fatalf("the second runner moved the pin: %+v", pinned)
	}
}

func TestOfflineRunnersTakeNoBuilds(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	r.runner(t, pool, "node_1", strings.Repeat("a", 64))
	r.nodes["node_1"].Status = nodes.StatusOffline
	if runners, _ := r.svc.BuildRunners(t.Context(), "org_1", pool.ConnectionID); len(runners) != 0 {
		t.Fatalf("runners %v", runners)
	}
}

func TestRunnerThatFailsItsCheckIsRecorded(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	if _, err := r.svc.JoinRunner(t.Context(), "org_1", pool.ID, "node_1"); err != nil {
		t.Fatal(err)
	}
	r.nodes["node_1"] = &nodes.Node{ID: "node_1", OrgID: "org_1", Role: "runner", Status: nodes.StatusOnline}
	r.svc.RunnerOnline(t.Context(), r.nodes["node_1"]) // no Firecracker host answers
	m, _ := r.store.GetMember(t.Context(), "node_1")
	if !strings.Contains(m.Reason, "KVM and microVM check") {
		t.Fatalf("reason %q", m.Reason)
	}
	if d := r.pub.details(pool.ConnectionID); d["build_isolation"] == "ready" {
		t.Fatalf("details %v", d)
	}
}

func TestJoinRunnerRefusesAnotherOrganizationsPool(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	if _, err := r.svc.JoinRunner(t.Context(), "org_2", pool.ID, "node_9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDialViaTriesEachOnlineRunner(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	r.runner(t, pool, "node_1", strings.Repeat("a", 64))
	r.runner(t, pool, "node_2", strings.Repeat("a", 64))
	r.streams.relayFail = map[string]error{"node_1": errors.New("this runner may not relay to 10.0.0.5:22")}
	conn, err := r.svc.DialVia(t.Context(), pool.ID, "10.0.0.5:22")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if len(r.streams.relayed) != 1 || r.streams.relayed[0] != "node_2->10.0.0.5:22" {
		t.Fatalf("relayed %v", r.streams.relayed)
	}
	if owner, err := r.svc.PoolOwner(t.Context(), pool.ID); err != nil || owner != "org_1" {
		t.Fatalf("owner %q %v", owner, err)
	}
}

func TestDialViaWithNoRunnerOnline(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	if _, err := r.svc.DialVia(t.Context(), pool.ID, "10.0.0.5:22"); err == nil || !strings.Contains(err.Error(), "no runner of pool builds is online") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeletePoolWaitsForItsRunners(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	r.runner(t, pool, "node_1", strings.Repeat("a", 64))
	if err := r.svc.DeletePool(t.Context(), owner, pool.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("err = %v", err)
	}
	r.nodes["node_1"].Status = nodes.StatusRevoked
	if err := r.svc.DeletePool(t.Context(), owner, pool.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.pub.deleted) != 1 || r.pub.deleted[0] != pool.ConnectionID {
		t.Fatalf("deleted %v", r.pub.deleted)
	}
	if _, err := r.store.GetPool(t.Context(), pool.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pool still there: %v", err)
	}
}

func TestPoolsAreOrganizationScoped(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	other := security.Principal{UserID: "usr_2", OrgID: "org_2"}
	if _, err := r.svc.GetPool(t.Context(), other, pool.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := r.svc.DeletePool(t.Context(), other, pool.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if pools, _ := r.svc.ListPools(t.Context(), other); len(pools) != 0 {
		t.Fatalf("pools %v", pools)
	}
}

func TestBuildDefaultPrefersTheProjectsPool(t *testing.T) {
	r := newRig()
	if d, err := r.svc.BuildDefault(t.Context(), "org_1", "prj_1"); err != nil || d != nil {
		t.Fatalf("no default pool, but %+v %v", d, err)
	}
	org, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "shared", Scope: ScopeOrganization, Settings: PoolSettings{Default: true, Registry: "conn_reg"}})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := r.svc.BuildDefault(t.Context(), "org_1", "prj_1"); d == nil || d.Builder != org.ConnectionID || d.Registry != "conn_reg" {
		t.Fatalf("default %+v", d)
	}
	if _, err = r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "second", Scope: ScopeOrganization, Settings: PoolSettings{Default: true}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second organization default: %v", err)
	}
	proj, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "mine", Scope: ScopeProject, ProjectID: "prj_1", Settings: PoolSettings{Default: true}})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := r.svc.BuildDefault(t.Context(), "org_1", "prj_1"); d == nil || d.Builder != proj.ConnectionID {
		t.Fatalf("the project's own default wasn't preferred: %+v", d)
	}
	if d, _ := r.svc.BuildDefault(t.Context(), "org_1", "prj_2"); d == nil || d.Builder != org.ConnectionID {
		t.Fatalf("another project fell back to %+v", d)
	}
	// Moving the default: the old one gives it up first.
	if _, err = r.svc.UpdateSettings(t.Context(), owner, org.ID, PoolSettings{}); err != nil {
		t.Fatal(err)
	}
	if d, _ := r.svc.BuildDefault(t.Context(), "org_1", "prj_2"); d != nil {
		t.Fatalf("a pool that gave up the default is still it: %+v", d)
	}
}

func nodesOnline(id, org string) *nodes.Node {
	return &nodes.Node{ID: id, OrgID: org, Name: id, Role: "runner", Status: nodes.StatusOnline}
}

func TestRunnerCountsAndSpot(t *testing.T) {
	r := newRig()
	pool, _ := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "builds", Scope: ScopeOrganization})
	counts, err := r.svc.RunnerCounts(t.Context(), "org_1")
	if err != nil || counts[pool.ConnectionID].Online != 0 || counts[pool.ID].Build != 0 {
		t.Fatalf("counts %+v %v", counts, err)
	}
	r.runner(t, pool, "node_1", strings.Repeat("a", 64))
	counts, _ = r.svc.RunnerCounts(t.Context(), "org_1")
	if c := counts[pool.ConnectionID]; c.Online != 1 || c.Build != 1 || c.OnDemand {
		t.Fatalf("counts %+v", c)
	}
	_, err = r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "spotty", Scope: ScopeOrganization, Mode: ModeOnDemand, Settings: PoolSettings{Provider: "digitalocean", CloudConnectionID: "conn_do", Region: "nyc3",
		Sizes: []string{"s-2vcpu-4gb-amd"}, MaxRunners: 1, Spot: true, ToolchainURL: "https://x.example/t.tgz", ToolchainSHA256: strings.Repeat("b", 64)}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no spot Droplets") {
		t.Fatalf("a DigitalOcean spot pool: %v", err)
	}
}
