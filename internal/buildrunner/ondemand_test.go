package buildrunner

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

// memMachines adds machines to memStore.
type memMachines struct {
	*memStore
	mu       sync.Mutex
	machines map[string]Machine
}

func (m *memMachines) CreateMachine(_ context.Context, x *Machine) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.machines[x.ID] = *x
	return nil
}
func (m *memMachines) UpdateMachine(_ context.Context, x *Machine) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.machines[x.ID]
	x.InFlight, x.Builds = cur.InFlight, cur.Builds
	m.machines[x.ID] = *x
	return nil
}
func (m *memMachines) ListMachines(_ context.Context, pool string) ([]Machine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Machine{}
	for _, x := range m.machines {
		if x.PoolID == pool && x.State != MachineGone {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *memMachines) MachineByNode(_ context.Context, node string) (*Machine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.machines {
		if x.NodeID == node && node != "" {
			return &x, nil
		}
	}
	return nil, ErrNotFound
}
func (m *memMachines) AdjustInFlight(_ context.Context, node string, delta int, finished bool, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, x := range m.machines {
		if x.NodeID == node && node != "" {
			x.InFlight = max(x.InFlight+delta, 0)
			if finished {
				x.Builds++
			}
			x.ActiveAt = at
			m.machines[id] = x
		}
	}
	return nil
}
func (m *memMachines) OnDemandPools(context.Context) ([]Pool, error) {
	var out []Pool
	for _, p := range m.pools {
		if p.Mode == ModeOnDemand {
			out = append(out, p)
		}
	}
	return out, nil
}

// fakeCloud is a cloud account that starts and removes machines.
type fakeCloud struct {
	mu      sync.Mutex
	servers map[string]string // provider ID -> name
	specs   []MachineSpec
	deleted []string
	next    int
	failNew error
}

func (c *fakeCloud) Create(_ context.Context, _, _ string, spec MachineSpec) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.specs = append(c.specs, spec)
	if c.failNew != nil {
		return "", c.failNew
	}
	c.next++
	id := "d" + strings.Repeat("1", c.next)
	c.servers[id] = spec.Name
	return id, nil
}
func (c *fakeCloud) Delete(_ context.Context, _, _, _, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.servers, id)
	c.deleted = append(c.deleted, id)
	return nil
}
func (c *fakeCloud) List(_ context.Context, _, _, _, _ string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for k, v := range c.servers {
		out[k] = v
	}
	return out, nil
}

type fakeMint struct{ machines []string }

func (f *fakeMint) MintRunnerJoin(_ context.Context, p security.Principal, pool, machine string, _ time.Duration) (string, time.Time, error) {
	f.machines = append(f.machines, p.UserID+"/"+pool+"/"+machine)
	return "lpj_live_" + machine, time.Now().Add(time.Hour), nil
}

type fakeRevoker struct{ revoked []string }

func (f *fakeRevoker) RevokeRunner(_ context.Context, _, node string) error {
	f.revoked = append(f.revoked, node)
	return nil
}

type odRig struct {
	*rig
	ms    *memMachines
	cloud *fakeCloud
	mint  *fakeMint
	rev   *fakeRevoker
	now   time.Time
	pool  *Pool
}

func newOnDemand(t *testing.T, settings PoolSettings) *odRig {
	t.Helper()
	r := newRig()
	o := &odRig{rig: r, ms: &memMachines{memStore: r.store, machines: map[string]Machine{}}, cloud: &fakeCloud{servers: map[string]string{}}, mint: &fakeMint{}, rev: &fakeRevoker{}, now: time.Now().UTC()}
	r.svc.Bind(o.ms)
	r.svc.Provisioner, r.svc.Tokens, r.svc.Revoker, r.svc.PublicURL = o.cloud, o.mint, o.rev, "https://lp.example.com"
	r.svc.clock = func() time.Time { return o.now }
	base := PoolSettings{Provider: "digitalocean", CloudConnectionID: "conn_do", Region: "nyc3", Sizes: []string{"s-2vcpu-4gb-amd"}, MaxRunners: 3,
		ToolchainURL: "https://releases.example.com/toolchain.tar.gz", ToolchainSHA256: strings.Repeat("b", 64)}
	base.MinWarm, base.MaxBuilds, base.IdleTimeoutMinutes = settings.MinWarm, settings.MaxBuilds, settings.IdleTimeoutMinutes
	if settings.MaxRunners > 0 {
		base.MaxRunners = settings.MaxRunners
	}
	pool, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "burst", Scope: ScopeOrganization, Mode: ModeOnDemand, Settings: base})
	if err != nil {
		t.Fatal(err)
	}
	o.pool = pool
	return o
}

// join makes machine m's runner join, check in and come online.
func (o *odRig) join(t *testing.T, m Machine) string {
	t.Helper()
	node := "node_" + m.ID
	if _, err := o.svc.JoinRunner(t.Context(), o.pool.OrgID, o.pool.ID, node); err != nil {
		t.Fatal(err)
	}
	o.nodes[node] = &nodes.Node{ID: node, OrgID: o.pool.OrgID, Name: m.Name, Role: "runner", Status: nodes.StatusOnline, Labels: map[string]string{MachineLabel: m.ID}}
	o.streams.serve(t, node, capsOf(node, strings.Repeat("a", 64)))
	o.svc.RunnerOnline(t.Context(), o.nodes[node])
	return node
}

func (o *odRig) machines(t *testing.T) []Machine {
	t.Helper()
	ms, err := o.ms.ListMachines(t.Context(), o.pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestOnDemandPoolNeedsAToolchain(t *testing.T) {
	r := newRig()
	_, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "burst", Scope: ScopeOrganization, Mode: ModeOnDemand,
		Settings: PoolSettings{Provider: "digitalocean", CloudConnectionID: "conn_do", Region: "nyc3", Sizes: []string{"s-2vcpu-4gb-amd"}, MaxRunners: 2}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestDemandStartsOneColdRunnerThatTakesOneBuild(t *testing.T) {
	o := newOnDemand(t, PoolSettings{})
	if err := o.svc.demand(t.Context(), o.pool); err != nil {
		t.Fatal(err)
	}
	// A second waiting build doesn't start another while one is booting.
	if err := o.svc.demand(t.Context(), o.pool); err != nil {
		t.Fatal(err)
	}
	ms := o.machines(t)
	if len(ms) != 1 || ms[0].State != MachineBooting || ms[0].Warm || ms[0].ProviderID == "" {
		t.Fatalf("machines %+v", ms)
	}
	spec := o.cloud.specs[0]
	if spec.ID != ms[0].ID || !strings.Contains(spec.UserData, "LOOMPLANE_ROLE=runner") || !strings.Contains(spec.UserData, "--toolchain-sha256 '"+strings.Repeat("b", 64)+"'") ||
		!strings.Contains(spec.UserData, "passwd -l root") || spec.Tags[1] != poolTag(o.pool.ID) {
		t.Fatalf("spec %+v", spec)
	}
	if len(o.mint.machines) != 1 || o.mint.machines[0] != "usr_1/"+o.pool.ID+"/"+ms[0].ID {
		t.Fatalf("minted %v: the token acts as the pool's creator, for that machine", o.mint.machines)
	}
	node := o.join(t, ms[0])
	runners, err := o.svc.BuildRunners(t.Context(), "org_1", o.pool.ConnectionID)
	if err != nil || len(runners) != 1 || runners[0] != node {
		t.Fatalf("runners %v %v", runners, err)
	}
	_ = o.svc.BuildStarted(t.Context(), "org_1", node)
	if runners, _ = o.svc.BuildRunners(t.Context(), "org_1", o.pool.ConnectionID); len(runners) != 0 {
		t.Fatalf("a cold runner took a second build: %v", runners)
	}
	_ = o.svc.BuildEnded(t.Context(), "org_1", node)
	if err = o.svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ms = o.machines(t); len(ms) != 0 {
		t.Fatalf("the cold runner stayed after its build: %+v", ms)
	}
	if len(o.cloud.deleted) != 1 || len(o.rev.revoked) != 1 || o.rev.revoked[0] != node {
		t.Fatalf("deleted %v, revoked %v", o.cloud.deleted, o.rev.revoked)
	}
}

func TestWarmRunnersAreKeptAndReplaced(t *testing.T) {
	o := newOnDemand(t, PoolSettings{MinWarm: 1, MaxBuilds: 2})
	if err := o.svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	ms := o.machines(t)
	if len(ms) != 1 || !ms[0].Warm {
		t.Fatalf("machines %+v", ms)
	}
	node := o.join(t, ms[0])
	for range 2 {
		if runners, _ := o.svc.BuildRunners(t.Context(), "org_1", o.pool.ConnectionID); len(runners) != 1 {
			t.Fatalf("the warm runner didn't take a build: %v", runners)
		}
		_ = o.svc.BuildStarted(t.Context(), "org_1", node)
		_ = o.svc.BuildEnded(t.Context(), "org_1", node)
	}
	if err := o.svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	ms = o.machines(t)
	if len(ms) != 1 || ms[0].NodeID != "" || !ms[0].Warm {
		t.Fatalf("after max_builds the warm runner should be replaced by a fresh one: %+v", ms)
	}
	if len(o.rev.revoked) != 1 {
		t.Fatalf("revoked %v", o.rev.revoked)
	}
}

func TestMachinesThatNeverJoinAreRemoved(t *testing.T) {
	o := newOnDemand(t, PoolSettings{})
	_ = o.svc.demand(t.Context(), o.pool)
	o.now = o.now.Add(bootDeadline + time.Minute)
	if err := o.svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ms := o.machines(t); len(ms) != 0 {
		t.Fatalf("machines %+v", ms)
	}
	if len(o.cloud.deleted) != 1 {
		t.Fatalf("deleted %v", o.cloud.deleted)
	}
}

func TestACreateThatAnsweredLateIsFound(t *testing.T) {
	o := newOnDemand(t, PoolSettings{})
	o.cloud.failNew = errors.New("timeout")
	_ = o.svc.demand(t.Context(), o.pool)
	ms := o.machines(t)
	if len(ms) != 1 || ms[0].State != MachineCreating {
		t.Fatalf("machines %+v", ms)
	}
	// The create had run: the cloud lists the machine by its name.
	o.cloud.servers["d999"] = ms[0].Name
	if err := o.svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	ms = o.machines(t)
	if ms[0].State != MachineBooting || ms[0].ProviderID != "d999" {
		t.Fatalf("machines %+v", ms)
	}
}

func TestOrphansAreDeleted(t *testing.T) {
	o := newOnDemand(t, PoolSettings{})
	o.cloud.servers["d777"] = "runner-01m4vf1ff2e9pvk24sm9h8vrxx"
	o.cloud.servers["d888"] = "someone-elses-droplet"
	if err := o.svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(o.cloud.deleted) != 1 || o.cloud.deleted[0] != "d777" {
		t.Fatalf("deleted %v: only a runner machine Loomplane didn't record goes", o.cloud.deleted)
	}
}

func TestDemandStopsAtMaxRunners(t *testing.T) {
	o := newOnDemand(t, PoolSettings{MaxRunners: 1})
	_ = o.svc.demand(t.Context(), o.pool)
	ms := o.machines(t)
	o.join(t, ms[0])
	_ = o.svc.BuildStarted(t.Context(), "org_1", ms[0].NodeID)
	_ = o.svc.BuildStarted(t.Context(), "org_1", "node_"+ms[0].ID)
	if err := o.svc.demand(t.Context(), o.pool); err != nil {
		t.Fatal(err)
	}
	if got := len(o.machines(t)); got != 1 {
		t.Fatalf("machines %d: the pool may run one", got)
	}
}

func TestEC2PoolPinsItsAMI(t *testing.T) {
	r := newRig()
	s := PoolSettings{Provider: "aws", CloudConnectionID: "conn_aws", Region: "us-east-1", Sizes: []string{"c8i.large"}, MaxRunners: 2,
		ToolchainURL: "https://releases.example.com/toolchain.tar.gz", ToolchainSHA256: strings.Repeat("b", 64)}
	if _, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "ec2", Scope: ScopeOrganization, Mode: ModeOnDemand, Settings: s}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an EC2 pool without an AMI: %v", err)
	}
	s.Image = "ami-0123456789abcdef0"
	if _, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "ec2", Scope: ScopeOrganization, Mode: ModeOnDemand, Settings: s}); err != nil {
		t.Fatal(err)
	}
}
