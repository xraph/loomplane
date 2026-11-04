package deployments

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/deploy"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// platformDriver runs nothing: every VM it is asked for is up and ready.
type platformDriver struct {
	mu      sync.Mutex
	hostID  string
	started int
	stopped int
	removed int
}

func (d *platformDriver) Check(context.Context) (fc.Capabilities, error) {
	return fc.Capabilities{HostID: d.hostID, Architecture: "amd64", RuntimeIdentity: strings.Repeat("a", 64), Isolation: true, Platform: true, IngressAddress: "203.0.113.9"}, nil
}
func (d *platformDriver) Start(context.Context, fc.VM, fc.Spec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.started++
	return nil
}
func (d *platformDriver) Inspect(context.Context, fc.VM) (bool, error) { return true, nil }
func (d *platformDriver) Configure(context.Context, fc.VM, fc.Request) (fc.GuestStatus, error) {
	return fc.GuestStatus{Ready: true}, nil
}
func (d *platformDriver) Stop(context.Context, fc.VM) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped++
	return nil
}
func (d *platformDriver) Remove(context.Context, fc.VM) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removed++
	return nil
}
func (d *platformDriver) Guest(context.Context, fc.VM, string, io.Reader) (fc.GuestStatus, error) {
	return fc.GuestStatus{Ready: true, Output: "hello"}, nil
}

// platformHost is a fake platform host: the real agent handler over a
// driver that runs nothing, trusting only the platform's client certificate.
type platformHost struct {
	id     string
	server *httptest.Server
	host   *fc.Host
	driver *platformDriver
	ca     string
}

// records lists what the host keeps, by scope.
func (h *platformHost) records(t *testing.T) []fc.Record {
	t.Helper()
	out, err := h.host.Store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// platformIdentity is the client certificate the platform presents.
type platformIdentity struct {
	cert, key, fingerprint string
	der                    []byte
}

func newPlatformIdentity(t *testing.T) platformIdentity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "loomplane-platform"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return platformIdentity{cert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), key: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), fingerprint: hex.EncodeToString(sum[:]), der: der}
}

func newPlatformHost(t *testing.T, name string, who platformIdentity) *platformHost {
	t.Helper()
	store, err := fc.OpenStore(t.TempDir() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &platformHost{id: "hw-" + name, driver: &platformDriver{hostID: "hw-" + name}}
	h.host = &fc.Host{Store: store, Driver: h.driver, Limits: fc.Limits{VMs: 8, VCPUs: 16, MemoryMiB: 32768, DiskMiB: 100000}, Key: []byte(strings.Repeat("k", 32))}
	cert, err := x509.ParseCertificate(who.der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	h.server = httptest.NewUnstartedServer(fc.Handler{Host: h.host, Platform: who.fingerprint})
	h.server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS13}
	h.server.StartTLS()
	t.Cleanup(h.server.Close)
	h.ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.server.Certificate().Raw}))
	return h
}

// fakePlatform keeps placements in memory the way platformcloud does:
// a session reserves, keeping the live placement's host unless moveTo names
// another, and releases the old placement, which keeps its host and
// instance.
type fakePlatform struct {
	mu         sync.Mutex
	who        platformIdentity
	hosts      map[string]*platformHost
	first      string
	moveTo     string
	placements []*platformcloud.Placement
	n          int
}

func (f *fakePlatform) client(hostID string) (*fc.Client, error) {
	h := f.hosts[hostID]
	raw, err := json.Marshal(fc.Credential{CA: h.ca, Certificate: f.who.cert, Key: f.who.key})
	if err != nil {
		return nil, err
	}
	return fc.NewClient(raw, map[string]string{"endpoint": h.server.URL, "host_id": h.id})
}

func (f *fakePlatform) live(org, env, name string) *platformcloud.Placement {
	for _, p := range f.placements {
		if p.OrgID == org && p.EnvironmentID == env && p.Resource == name && p.State != "released" {
			return p
		}
	}
	return nil
}

func (f *fakePlatform) Session(ctx context.Context, orgID, projectID, envID, resource string, req platformcloud.Request) (*fc.Client, fc.Scope, *platformcloud.Placement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.OrgID != orgID || req.ProjectID != projectID || req.EnvironmentID != envID {
		return nil, fc.Scope{}, nil, fmt.Errorf("request outside its scope")
	}
	host := f.first
	if old := f.live(orgID, envID, req.Resource); old != nil {
		host = *old.HostID
		old.State = "released"
	}
	if f.moveTo != "" {
		host = f.moveTo
	}
	f.n++
	p := &platformcloud.Placement{ID: fmt.Sprintf("plc_%d", f.n), OrgID: orgID, ProjectID: projectID, EnvironmentID: envID, Resource: req.Resource, Kind: "service", HostID: &host,
		VCPUs: req.VCPUs, MemoryMiB: req.MemoryMiB, DiskMiB: req.DiskMiB, State: "reserved", InstanceID: resource}
	f.placements = append(f.placements, p)
	c, err := f.client(host)
	if err == nil {
		// As platformcloud does, which records the host's runtime identity.
		_, err = c.Check(ctx)
	}
	cp := *p
	return c, cp.Scope(), &cp, err
}

func (f *fakePlatform) Attach(ctx context.Context, orgID, projectID, envID, resource, name string) (*fc.Client, fc.Scope, *platformcloud.Placement, error) {
	f.mu.Lock()
	p := f.live(orgID, envID, name)
	f.mu.Unlock()
	if p == nil || p.ProjectID != projectID || p.InstanceID != resource {
		return nil, fc.Scope{}, nil, platformcloud.ErrNotFound
	}
	cp := *p
	c, scope, err := f.Open(ctx, &cp)
	return c, scope, &cp, err
}

func (f *fakePlatform) Open(_ context.Context, p *platformcloud.Placement) (*fc.Client, fc.Scope, error) {
	if p.HostID == nil {
		return nil, p.Scope(), nil
	}
	c, err := f.client(*p.HostID)
	return c, p.Scope(), err
}

func (f *fakePlatform) Leftovers(_ context.Context, orgID, envID, name string) ([]platformcloud.Placement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []platformcloud.Placement
	for _, p := range f.placements {
		if p.OrgID == orgID && p.EnvironmentID == envID && p.Resource == name && p.State == "released" && p.HostID != nil && p.InstanceID != "" {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakePlatform) find(id string) *platformcloud.Placement {
	for _, p := range f.placements {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (f *fakePlatform) Running(_ context.Context, placementID, instance string, generation int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(placementID)
	p.State, p.InstanceID, p.Generation, p.RestoreRunning = "running", instance, generation, false
	return nil
}

func (f *fakePlatform) Stopped(_ context.Context, placementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(placementID)
	p.State, p.RestoreRunning = "stopped", false
	return nil
}

func (f *fakePlatform) Removed(_ context.Context, placementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(placementID)
	p.State, p.InstanceID = "released", ""
	return nil
}

// countingIssuer authorizes every operation at the next generation.
type countingIssuer struct {
	mu     sync.Mutex
	n      int64
	scopes []fc.Scope
}

func (i *countingIssuer) IssueFirecracker(_ context.Context, scope fc.Scope, _, digest string) (fc.Authority, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !scope.Valid() || len(digest) != 64 {
		return fc.Authority{}, fc.ErrConflict
	}
	i.n++
	i.scopes = append(i.scopes, scope)
	return fc.Authority{Generation: i.n, Operation: fmt.Sprintf("op-%d", i.n), Expires: time.Now().Add(time.Minute)}, nil
}

// platformInstances is Ctrl Plane's instance store as far as a deployment
// on an existing instance goes: deleting one runs its provider's Remove,
// as Ctrl Plane does.
type platformInstances struct {
	ownedInstances
	remove func(context.Context, *instance.Instance) error
}

func (s *platformInstances) Delete(ctx context.Context, _ id.ID) error {
	if s.value != nil && s.remove != nil {
		if err := s.remove(ctx, s.value); err != nil {
			return err
		}
	}
	s.value = nil
	return nil
}

// platformDeploys rolls a release out the way Ctrl Plane does: through the
// instance's provider.
type platformDeploys struct {
	deploy.Service
	apply func(context.Context) error
}

func (d *platformDeploys) Deploy(ctx context.Context, _ deploy.DeployRequest) (*deploy.Deployment, error) {
	return &deploy.Deployment{State: deploy.DeploySucceeded}, d.apply(ctx)
}

type platformFixture struct {
	target    *PlatformTarget
	platform  *fakePlatform
	hosts     map[string]*platformHost
	instances *platformInstances
	issuer    *countingIssuer
	iid       id.ID
}

const platformImage = "docker.io/team/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newPlatformFixture(t *testing.T) *platformFixture {
	t.Helper()
	who := newPlatformIdentity(t)
	f := &platformFixture{hosts: map[string]*platformHost{}, issuer: &countingIssuer{}, iid: id.New(id.PrefixInstance)}
	for _, name := range []string{"a", "b"} {
		f.hosts["host_"+name] = newPlatformHost(t, name, who)
	}
	f.platform = &fakePlatform{who: who, hosts: f.hosts, first: "host_a"}
	f.instances = &platformInstances{ownedInstances: ownedInstances{value: &instance.Instance{Entity: ctrlplane.Entity{ID: f.iid}, Name: "web", TenantID: "org_a", ProviderName: "platform.fra",
		Services: []cpprovider.ServiceSpec{{Name: mainService, Image: platformImage}}, Labels: map[string]string{owned.LabelOrg: "org_a", owned.LabelProject: "prj_1", owned.LabelEnvironment: "env_1", owned.LabelConnection: fc.PlatformConnection}}}}
	deploys := &platformDeploys{}
	f.target = &PlatformTarget{Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: f.instances, Deploys: deploys}, Platform: f.platform, Authority: f.issuer, Secrets: &memSecrets{m: map[string][]byte{}}}
	deploys.apply = func(ctx context.Context) error {
		backend, err := f.target.backend("fra")
		if err != nil {
			return err
		}
		_, err = backend.Apply(ctx, f.iid, f.instances.value.Services[0], f.instances.value.Labels)
		return err
	}
	f.instances.remove = func(ctx context.Context, inst *instance.Instance) error {
		backend, err := f.target.backend("fra")
		if err != nil {
			return err
		}
		return backend.Remove(ctx, inst.ID, inst.Labels)
	}
	return f
}

// job deploys web, a digest-pinned image, onto the Loomplane Cloud target
// fra. orgs fails the test if anything reads an organization's connection.
func (f *platformFixture) job(t *testing.T) *Job {
	t.Helper()
	orgs := buildCredentialFunc(func(_ context.Context, org, conn string) (*connections.Connection, []byte, error) {
		t.Errorf("a platform deployment read connection %s of %s", conn, org)
		return nil, nil, connections.ErrNotFound
	})
	cfg := &planning.ServiceConfig{Provider: "platform", Region: "fra", Service: "web", Public: false, HealthPath: "/healthz",
		BuildTarget:    &planning.BuildTarget{BuilderCloud: "platform", Builder: "platform", Image: platformImage},
		CloudPlacement: &planning.CloudPlacement{CloudID: "cloud_platform", TargetID: "target_fra", TargetName: "fra", Provider: "platform", Region: "fra", Platform: &platformcloud.Ref{Region: "fra", VCPUs: 1, MemoryMiB: 512, DiskMiB: 4096}}}
	job := &Job{BuildCredentials: orgs, Deployment: &Deployment{ID: "dep_1", OrgID: "org_a", ProjectID: "prj_1", EnvironmentID: "env_1", PlanID: "plan_1"}, Plan: &planning.Plan{ID: "plan_1"},
		Action:  planning.Action{ID: "service:web", Name: "web", Provider: "platform", Region: "fra", Service: cfg},
		Binding: &planning.Binding{OrgID: "org_a", ProjectID: "prj_1", EnvironmentID: "env_1", Name: "web", Provider: "platform", Region: "fra", ExternalID: f.iid.String()},
		Env:     map[string]string{"TOKEN": "secret"}, Secrets: map[string]bool{"TOKEN": true}, Progress: func(string, string) {}}
	job.Checkpoint = func([]byte) error { return nil }
	return job
}

func (f *platformFixture) deploy(t *testing.T) *Outcome {
	t.Helper()
	out, err := f.target.Apply(t.Context(), f.job(t))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlatformDeployUsesPlatformScope(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	records := f.hosts["host_a"].records(t)
	if len(records) != 1 {
		t.Fatalf("host a keeps %d records", len(records))
	}
	want := fc.Scope{Org: "org_a", Connection: fc.PlatformConnection, Project: "prj_1", Environment: "env_1", Resource: f.iid.String()}
	if got := records[0]; got.Scope != want || got.State != "ready" || got.Current == nil || got.Current.Spec.Image != platformImage {
		t.Fatalf("host saw %+v", got)
	}
	if len(f.hosts["host_b"].records(t)) != 0 {
		t.Fatal("host b ran something")
	}
	if labels := f.instances.value.Labels; labels[owned.LabelOrg] != "org_a" || labels[owned.LabelConnection] != fc.PlatformConnection || labels[owned.LabelProject] != "prj_1" || labels[owned.LabelEnvironment] != "env_1" {
		t.Fatalf("instance labels %v", labels)
	}
	p := f.platform.live("org_a", "env_1", "web")
	if p == nil || p.State != "running" || p.InstanceID != f.iid.String() || p.Generation == 0 || *p.HostID != "host_a" {
		t.Fatalf("placement %+v", p)
	}
	if p.VCPUs != 1 || p.MemoryMiB != 512 || p.DiskMiB != 4096 {
		t.Fatalf("reserved %d vCPU %d MiB %d MiB disk, want what was previewed", p.VCPUs, p.MemoryMiB, p.DiskMiB)
	}
	for _, s := range f.issuer.scopes {
		if s != want {
			t.Fatalf("authority for %+v", s)
		}
	}
}

func TestPlatformDeployNeverTouchesOrgConnections(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t) // the job's credentials fail the test if read
	backend, err := f.target.backend("fra")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Status(t.Context(), f.iid, f.instances.value.Labels); err != nil {
		t.Fatal(err)
	}
	// An action that names a connection isn't a Loomplane Cloud action.
	job := f.job(t)
	job.Action.ConnectionID = "conn_org"
	if _, err = f.target.Apply(t.Context(), job); err == nil {
		t.Fatal("a platform action through an organization's connection was applied")
	}
	// Labels naming another connection are refused before any host is reached.
	labels := maps2(f.instances.value.Labels, owned.LabelConnection, "conn_org")
	if _, err = backend.Status(t.Context(), f.iid, labels); err == nil {
		t.Fatal("status through an organization's connection label")
	}
}

func maps2(in map[string]string, key, value string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	out[key] = value
	return out
}

func TestPlatformRemoveReleasesPlacement(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	job := f.job(t)
	b := job.Binding
	job.Binding = nil
	if err := f.target.Remove(t.Context(), job, b); err != nil {
		t.Fatal(err)
	}
	records := f.hosts["host_a"].records(t)
	if len(records) != 1 || records[0].State != "removed" {
		t.Fatalf("host a keeps %+v", records)
	}
	for _, p := range f.platform.placements {
		if p.State != "released" || p.InstanceID != "" {
			t.Fatalf("placement left %+v", p)
		}
	}
	if f.instances.value != nil {
		t.Fatal("instance kept")
	}
}

func TestUserStopIsNotRestored(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	// As if a suspension had remembered to start it again.
	f.platform.live("org_a", "env_1", "web").RestoreRunning = true
	f.instances.value.State = cpprovider.StateStopping
	backend, err := f.target.backend("fra")
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.SetRunning(t.Context(), f.iid, f.instances.value.Labels, false); err != nil {
		t.Fatal(err)
	}
	p := f.platform.live("org_a", "env_1", "web")
	if p == nil || p.State != "stopped" || p.RestoreRunning {
		t.Fatalf("placement %+v", p)
	}
	if records := f.hosts["host_a"].records(t); len(records) != 1 || records[0].State != "stopped" {
		t.Fatalf("host keeps %+v", records)
	}
}

// A redeploy that Loomplane Cloud places on another host removes the VM on
// the old one once the new one is up, so its room isn't stranded.
func TestPlatformMoveRemovesOldVM(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	f.platform.moveTo = "host_b"
	f.deploy(t)
	if records := f.hosts["host_b"].records(t); len(records) != 1 || records[0].State != "ready" {
		t.Fatalf("host b keeps %+v", records)
	}
	if records := f.hosts["host_a"].records(t); len(records) != 1 || records[0].State != "removed" {
		t.Fatalf("host a keeps %+v", records)
	}
	live := f.platform.live("org_a", "env_1", "web")
	if live == nil || *live.HostID != "host_b" || live.State != "running" {
		t.Fatalf("live placement %+v", live)
	}
	for _, p := range f.platform.placements {
		if p != live && (p.State != "released" || p.InstanceID != "") {
			t.Fatalf("old placement %+v", p)
		}
	}
}

// When the old host can't be reached, the deploy still succeeds and the old
// placement keeps its instance, which is what the cleanup sweep looks for.
func TestPlatformMoveLeavesUnreachableOldVMForCleanup(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	f.hosts["host_a"].server.Close()
	f.platform.moveTo = "host_b"
	f.deploy(t)
	var marked []*platformcloud.Placement
	for _, p := range f.platform.placements {
		if p.State == "released" && p.InstanceID != "" {
			marked = append(marked, p)
		}
	}
	if len(marked) != 1 || *marked[0].HostID != "host_a" || marked[0].InstanceID != f.iid.String() {
		t.Fatalf("cleanup markers %+v", marked)
	}
}

// What a workspace reads of a failure never names the host: not its ID,
// endpoint or address.
func TestPlatformErrorsNameNoHost(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	h := f.hosts["host_a"]
	h.server.Close()
	backend, err := f.target.backend("fra")
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Status(t.Context(), f.iid, f.instances.value.Labels)
	if err == nil {
		t.Fatal("status of an unreachable host")
	}
	_, logsErr := backend.(*firecrackerBackend).Logs(t.Context(), f.iid, f.instances.value.Labels, cpprovider.LogOptions{})
	stopErr := backend.SetRunning(t.Context(), f.iid, f.instances.value.Labels, false)
	for _, e := range []error{err, logsErr, stopErr} {
		if e == nil {
			t.Fatal("an unreachable host answered")
		}
		for _, leak := range []string{h.server.URL, strings.TrimPrefix(h.server.URL, "https://"), "127.0.0.1", h.id, "host_a"} {
			if strings.Contains(e.Error(), leak) {
				t.Fatalf("%q names %q", e.Error(), leak)
			}
		}
	}
	if !slices.ContainsFunc(f.platform.placements, func(p *platformcloud.Placement) bool { return p.State == "running" }) {
		t.Fatal("a failed stop changed the placement")
	}
}

// failingCredentials fails the test if anything reads a connection.
type failingCredentials struct{ t *testing.T }

func (c failingCredentials) Credential(_ context.Context, org, conn string) (*connections.Connection, []byte, error) {
	c.t.Errorf("read connection %q of %s", conn, org)
	return nil, nil, connections.ErrNotFound
}

// platformRunner deploys api onto the Loomplane Cloud target fra.
func platformRunner(t *testing.T) (*Runner, *planning.Plan, *fakePlans, *recorder, *cloudResolver) {
	t.Helper()
	plan := webAndAPI()
	plan.Actions = plan.Actions[:1]
	a := &plan.Actions[0]
	a.Provider, a.Region, a.ConnectionID = "platform", "fra", ""
	a.Service.Provider, a.Service.Region, a.Service.Public = "platform", "fra", false
	a.Service.BuildTarget = &planning.BuildTarget{BuilderCloud: "platform", Builder: "platform", Image: platformImage}
	a.Service.CloudPlacement = &planning.CloudPlacement{GrantRevision: 1, CloudID: "cloud_platform", CloudRevision: 1, AssignmentRevision: 1, TargetID: "target_fra", TargetRevision: 1, TargetName: "fra",
		Provider: "platform", Region: "fra", RegionSource: "default", Platform: &platformcloud.Ref{Region: "fra", VCPUs: 1, MemoryMiB: 512, DiskMiB: 4096}}
	exec := &recorder{}
	r, _, plans := newRunner(t, plan, exec, nil)
	r.executors["platform"] = exec
	r.credentials = failingCredentials{t}
	resolved := &cloudResolver{assignment: &cloudprofiles.Assignment{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, GrantRevision: 1, CloudID: "cloud_platform", CloudRevision: 1, Revision: 1,
		Targets: []cloudprofiles.Target{{ID: "target_fra", OrgID: plan.OrgID, CloudID: "cloud_platform", Name: "fra", Runtime: "platform", Provider: "platform", Region: "fra", Revision: 1}}}}
	r.cloudProfiles = resolved
	return r, plan, plans, exec, resolved
}

// The runner hands a Loomplane Cloud action to its executor with no
// connection, reading none, and binds it to none.
func TestRunnerDeploysPlatformWithoutConnection(t *testing.T) {
	r, plan, plans, exec, _ := platformRunner(t)
	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if len(exec.jobs) != 1 || exec.jobs[0].Connection != nil || exec.jobs[0].Credential != nil {
		t.Fatalf("jobs %+v", exec.jobs)
	}
	b := plans.bindings["service:api"]
	if b == nil || b.Provider != "platform" || b.Region != "fra" || b.ConnectionID != "" {
		t.Fatalf("binding %+v", b)
	}
}

func TestRunnerRevalidatesPlatformTarget(t *testing.T) {
	for name, change := range map[string]func(*planning.Plan, *cloudResolver){
		"target revision":   func(_ *planning.Plan, c *cloudResolver) { c.assignment.Targets[0].Revision++ },
		"target runtime":    func(_ *planning.Plan, c *cloudResolver) { c.assignment.Targets[0].Runtime = "managed" },
		"target region":     func(_ *planning.Plan, c *cloudResolver) { c.assignment.Targets[0].Region = "ams" },
		"assignment":        func(_ *planning.Plan, c *cloudResolver) { c.assignment.Revision++ },
		"named connection":  func(p *planning.Plan, _ *cloudResolver) { p.Actions[0].ConnectionID = "conn_v" },
		"room in elsewhere": func(p *planning.Plan, _ *cloudResolver) { p.Actions[0].Service.CloudPlacement.Platform.Region = "ams" },
	} {
		t.Run(name, func(t *testing.T) {
			r, plan, plans, exec, c := platformRunner(t)
			if name == "named connection" {
				r.credentials = targetCredentials{}
			}
			change(plan, c)
			if _, err := runAll(t, r, plan); err == nil {
				t.Fatal("stale Loomplane Cloud placement deployed")
			}
			if len(exec.jobs) != 0 || len(plans.bindings) != 0 {
				t.Fatal("stale placement reached the executor")
			}
		})
	}
}
