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
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
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
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// platformDriver runs nothing: every VM it is asked for is up and ready,
// and a build VM succeeds once it has its source.
type platformDriver struct {
	mu       sync.Mutex
	hostID   string
	registry [][]byte // what each configured VM was given to pull with
	started  int
	stopped  int
	removed  int
	// smt reports SMT on, which the platform policy refuses.
	smt bool
	// sources counts the source archives build VMs were sent.
	sources int
	// failBuild makes every build fail.
	failBuild bool
	// failStart makes every VM start fail.
	failStart bool
}

func (d *platformDriver) Check(context.Context) (fc.Capabilities, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fc.Capabilities{HostID: d.hostID, Architecture: "amd64", RuntimeIdentity: strings.Repeat("a", 64), Isolation: true, Platform: true, IngressAddress: "203.0.113.9",
		SMTActive: d.smt, DeniedPorts: []int{25, 465, 587}}, nil
}
func (d *platformDriver) Start(context.Context, fc.VM, fc.Spec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failStart {
		return errors.New("the VM did not start")
	}
	d.started++
	return nil
}
func (d *platformDriver) Inspect(context.Context, fc.VM) (bool, error) { return true, nil }
func (d *platformDriver) Configure(_ context.Context, _ fc.VM, q fc.Request) (fc.GuestStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.registry = append(d.registry, q.Values.RegistryConfig)
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
func (d *platformDriver) Guest(_ context.Context, vm fc.VM, action string, body io.Reader) (fc.GuestStatus, error) {
	if vm.Spec.Kind != "build" {
		return fc.GuestStatus{Ready: true, Output: "hello"}, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if action == "source" {
		if _, err := io.Copy(io.Discard, body); err != nil {
			return fc.GuestStatus{}, err
		}
		d.sources++
	}
	switch {
	case d.sources == 0:
		return fc.GuestStatus{State: "awaiting-source"}, nil
	case d.failBuild:
		return fc.GuestStatus{State: "failed", Output: "step 1 failed"}, nil
	}
	return fc.GuestStatus{State: "succeeded", Digest: "sha256:" + strings.Repeat("c", 64), Output: "built"}, nil
}

// platformHost is a fake platform host: the real agent handler over a
// driver that runs nothing, trusting only the platform's client certificate.
type platformHost struct {
	id     string
	server *httptest.Server
	host   *fc.Host
	driver *platformDriver
	ca     string

	mu    sync.Mutex
	paths []string // every agent path the platform called
}

// called reports whether the platform called path on the host.
func (h *platformHost) called(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.paths, path)
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
	h.host = &fc.Host{Store: store, Driver: h.driver, Limits: fc.Limits{VMs: 8, VCPUs: 16, MemoryMiB: 32768, DiskMiB: 100000, PerOrg: fc.OrgLimits{VMs: 4, VCPUs: 8, MemoryMiB: 8192}}, Key: []byte(strings.Repeat("k", 32))}
	cert, err := x509.ParseCertificate(who.der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	agent := fc.Handler{Host: h.host, Platform: who.fingerprint}
	h.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.paths = append(h.paths, r.URL.Path)
		h.mu.Unlock()
		agent.ServeHTTP(w, r)
	}))
	h.server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS13}
	h.server.StartTLS()
	t.Cleanup(h.server.Close)
	h.ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.server.Certificate().Raw}))
	return h
}

// fakePlatform keeps placements in memory the way platformcloud does:
// a session reserves, keeping the live placement's host unless moveTo names
// another. A running placement stays running until the new one runs, which
// releases it, keeping its host and instance; any other is released at
// once. A failed apply abandons the new placement.
type fakePlatform struct {
	mu         sync.Mutex
	who        platformIdentity
	hosts      map[string]*platformHost
	first      string
	moveTo     string
	placements []*platformcloud.Placement
	n          int
	// registry is the platform registry builds push to.
	registry *platformRegistry
	// builds are the build VMs metered, by placement.
	builds []string
	// apex, when set, is where Publish names services.
	apex string
	// published are the hosts each Publish pointed a name at.
	published []string
	hostnames map[string]string
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
		if p.OrgID == org && p.EnvironmentID == env && p.Resource == name && p.State != "released" && p.Replaces == "" {
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
	host, replaces := f.first, ""
	for _, p := range f.placements {
		if p.OrgID == orgID && p.EnvironmentID == envID && p.Resource == req.Resource && p.State != "released" && p.Replaces != "" {
			p.State = "released"
		}
	}
	if old := f.live(orgID, envID, req.Resource); old != nil {
		host = *old.HostID
		if old.State == "running" {
			replaces = old.ID
		} else {
			old.State = "released"
		}
	}
	if f.moveTo != "" {
		host = f.moveTo
	}
	f.n++
	p := &platformcloud.Placement{ID: fmt.Sprintf("plc_%d", f.n), OrgID: orgID, ProjectID: projectID, EnvironmentID: envID, Resource: req.Resource, Kind: req.Kind, HostID: &host,
		VCPUs: req.VCPUs, MemoryMiB: req.MemoryMiB, DiskMiB: req.DiskMiB, State: "reserved", InstanceID: resource, Replaces: replaces}
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

func (f *fakePlatform) find(orgID, id string) *platformcloud.Placement {
	for _, p := range f.placements {
		if p.ID == id && p.OrgID == orgID {
			return p
		}
	}
	return nil
}

func (f *fakePlatform) Running(_ context.Context, orgID, placementID, instance string, generation int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(orgID, placementID)
	if p.Replaces != "" {
		f.find(orgID, p.Replaces).State = "released"
	}
	p.State, p.InstanceID, p.Generation, p.RestoreRunning, p.Replaces = "running", instance, generation, false, ""
	return nil
}

func (f *fakePlatform) Abandon(_ context.Context, orgID, placementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.find(orgID, placementID); p != nil && p.State == "reserved" && p.Replaces != "" {
		p.State = "released"
	}
	return nil
}

func (f *fakePlatform) Stopped(_ context.Context, orgID, placementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(orgID, placementID)
	p.State, p.RestoreRunning = "stopped", false
	return nil
}

func (f *fakePlatform) Removed(_ context.Context, orgID, placementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(orgID, placementID)
	p.State, p.InstanceID = "released", ""
	return nil
}

func (f *fakePlatform) Release(_ context.Context, placementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.placements {
		if p.ID == placementID {
			p.State = "released"
			return nil
		}
	}
	return platformcloud.ErrNotFound
}

func (f *fakePlatform) Registry(_ context.Context, orgID string) (*registry.Config, error) {
	if f.registry == nil {
		return nil, errors.New("no platform registry")
	}
	return f.registry.config(orgID), nil
}

func (f *fakePlatform) RecordBuild(_ context.Context, p platformcloud.Placement, started, ended time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Kind != "build" || started.IsZero() || ended.Before(started) {
		return fmt.Errorf("metered %+v from %v to %v", p, started, ended)
	}
	f.builds = append(f.builds, p.ID)
	return nil
}

func (f *fakePlatform) Publish(_ context.Context, orgID, placementID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(orgID, placementID)
	if p == nil || p.State != "running" {
		return "", platformcloud.ErrNotFound
	}
	if f.apex == "" {
		return "", nil
	}
	if f.hostnames == nil {
		f.hostnames = map[string]string{}
	}
	name := f.hostnames[p.Resource]
	if name == "" {
		name = p.Resource + "-abcdefghijkl." + f.apex
		f.hostnames[p.Resource] = name
	}
	f.published = append(f.published, *p.HostID)
	return name, nil
}

func (f *fakePlatform) Unpublish(_ context.Context, _, _, resource string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := f.hostnames[resource]
	delete(f.hostnames, resource)
	return name, nil
}

// countingIssuer authorizes every operation at the next generation. A build
// renewing its authority keeps the generation it has, as the deployment
// store does.
type countingIssuer struct {
	mu     sync.Mutex
	n      int64
	scopes []fc.Scope
	builds map[string]fc.Authority
}

func (i *countingIssuer) IssueFirecracker(_ context.Context, scope fc.Scope, operation, digest string) (fc.Authority, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !scope.Valid() || len(digest) != 64 {
		return fc.Authority{}, fc.ErrConflict
	}
	i.scopes = append(i.scopes, scope)
	key := scope.Key() + "/" + operation + "/" + digest
	if a, ok := i.builds[key]; ok {
		a.Expires = time.Now().Add(time.Minute)
		return a, nil
	}
	i.n++
	a := fc.Authority{Generation: i.n, Operation: fmt.Sprintf("op-%d", i.n), Expires: time.Now().Add(time.Minute)}
	if strings.HasPrefix(operation, "build/") {
		if i.builds == nil {
			i.builds = map[string]fc.Authority{}
		}
		i.builds[key] = a
	}
	return a, nil
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

// workspaceRegistry is web's own registry connection, and the image it
// pulls from there.
var workspaceRegistry = &connections.Connection{ID: "conn_registry", OrgID: "org_a", Cloud: connections.Registry, Status: connections.StatusReady,
	Details: map[string]string{"endpoint": "https://registry.example.com", "repository": "team", "pull": "ready"}}

const workspaceImage = "registry.example.com/team/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// registryJob deploys web from the workspace's registry. Reading any
// connection but that registry fails the test; reads counts the reads.
func (f *platformFixture) registryJob(t *testing.T, conns map[string]*connections.Connection, reads *int) *Job {
	t.Helper()
	job := f.job(t)
	read := buildCredentialFunc(func(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
		*reads++
		c := conns[id]
		if c == nil {
			t.Errorf("a platform deployment read connection %s of %s", id, org)
			return nil, nil, connections.ErrNotFound
		}
		return c, []byte(`{"token":"workspace-pull-token"}`), nil
	})
	job.BuildCredentials = read
	f.target.Credentials = read
	f.target.regions = nil
	f.target.Ctrl.Providers = cpprovider.NewRegistry()
	settings := planning.ReviewedConnectionSettings(connections.Registry, workspaceRegistry.Details)
	job.Action.Service.BuildTarget = &planning.BuildTarget{BuilderCloud: "platform", Builder: "platform", Image: workspaceImage, Registry: "conn_registry", RegistrySettings: settings}
	f.instances.value.Services[0].Image = workspaceImage
	return job
}

// A platform deployment reads no connection of the organization's but the
// registry its image is pulled from, and refuses anything else named as one.
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
	// With a workspace registry, that registry is the only connection read.
	reads := 0
	if _, err = f.target.Apply(t.Context(), f.registryJob(t, map[string]*connections.Connection{"conn_registry": workspaceRegistry}, &reads)); err != nil {
		t.Fatal(err)
	}
	if reads == 0 {
		t.Fatal("the workspace registry was never read")
	}
	if backend, err = f.target.backend("fra"); err != nil {
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

// A workspace's own image pulls with its own registry credential, handed to
// the host with the request.
func TestPlatformDeployPullsFromWorkspaceRegistry(t *testing.T) {
	f := newPlatformFixture(t)
	reads := 0
	if _, err := f.target.Apply(t.Context(), f.registryJob(t, map[string]*connections.Connection{"conn_registry": workspaceRegistry}, &reads)); err != nil {
		t.Fatal(err)
	}
	d := f.hosts["host_a"].driver
	if len(d.registry) != 1 || !strings.Contains(string(d.registry[0]), "registry.example.com") || !strings.Contains(string(d.registry[0]), "workspace-pull-token") {
		t.Fatalf("host was given %q", d.registry)
	}
	if records := f.hosts["host_a"].records(t); len(records) != 1 || records[0].Current == nil || records[0].Current.Spec.Image != workspaceImage {
		t.Fatalf("host keeps %+v", records)
	}
}

// A connection that isn't a registry, or is another organization's, is never
// used as one.
func TestPlatformDeployRefusesNonRegistryConnection(t *testing.T) {
	for name, c := range map[string]*connections.Connection{
		"other cloud": {ID: "conn_registry", OrgID: "org_a", Cloud: connections.Vercel, Status: connections.StatusReady, Details: workspaceRegistry.Details},
		"other org":   {ID: "conn_registry", OrgID: "org_b", Cloud: connections.Registry, Status: connections.StatusReady, Details: workspaceRegistry.Details},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPlatformFixture(t)
			reads := 0
			if _, err := f.target.Apply(t.Context(), f.registryJob(t, map[string]*connections.Connection{"conn_registry": c}, &reads)); err == nil {
				t.Fatal("deployed through a connection that isn't the workspace's registry")
			}
			if len(f.hosts["host_a"].records(t)) != 0 {
				t.Fatal("the host ran something")
			}
		})
	}
}

// Loomplane Cloud runs only images named by a sha256 digest.
func TestPlatformDeployNeedsDigestPinnedImage(t *testing.T) {
	for _, image := range []string{"docker.io/team/web:latest", "docker.io/team/web@sha256:short", "@sha256:" + strings.Repeat("a", 64)} {
		f := newPlatformFixture(t)
		job := f.job(t)
		job.Action.Service.BuildTarget.Image = image
		if _, err := f.target.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "digest-pinned") {
			t.Fatalf("%q: %v", image, err)
		}
	}
	// Only the platform's own builder builds from source.
	f := newPlatformFixture(t)
	job := f.job(t)
	job.Action.Service.BuildTarget = &planning.BuildTarget{BuilderCloud: connections.Firecracker, Builder: "conn_fc"}
	if _, err := f.target.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("another builder: %v", err)
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

// A redeploy whose apply fails on the new host gives the new placement up:
// the old one runs on, and is billed, where it was.
func TestPlatformFailedMoveKeepsTheOldPlacementRunning(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	old := f.platform.live("org_a", "env_1", "web")
	f.platform.moveTo = "host_b"
	f.hosts["host_b"].driver.mu.Lock()
	f.hosts["host_b"].driver.failStart = true
	f.hosts["host_b"].driver.mu.Unlock()
	if _, err := f.target.Apply(t.Context(), f.job(t)); err == nil {
		t.Fatal("the move to a host whose VM can't start succeeded")
	}
	f.platform.mu.Lock()
	defer f.platform.mu.Unlock()
	if live := f.platform.live("org_a", "env_1", "web"); live != old || live.State != "running" || *live.HostID != "host_a" {
		t.Fatalf("live placement %+v, want the old one running", live)
	}
	for _, p := range f.platform.placements {
		if p != old && (p.State != "released" || p.Replaces != old.ID || *p.HostID != "host_b") {
			t.Fatalf("the failed move's placement %+v, want it released", p)
		}
	}
	if records := f.hosts["host_a"].records(t); len(records) != 1 || records[0].State != "ready" {
		t.Fatalf("host a keeps %+v", records)
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

func TestRegistryOnlyCredentials(t *testing.T) {
	conns := map[string]*connections.Connection{
		"conn_registry": workspaceRegistry,
		"conn_vercel":   {ID: "conn_vercel", OrgID: "org_a", Cloud: connections.Vercel},
		"conn_foreign":  {ID: "conn_foreign", OrgID: "org_b", Cloud: connections.Registry},
		"conn_renamed":  {ID: "conn_other", OrgID: "org_a", Cloud: connections.Registry},
	}
	r := registryOnly{buildCredentialFunc(func(_ context.Context, _, id string) (*connections.Connection, []byte, error) {
		return conns[id], []byte("secret"), nil
	})}
	if c, raw, err := r.Credential(t.Context(), "org_a", "conn_registry"); err != nil || c != workspaceRegistry || string(raw) != "secret" {
		t.Fatalf("registry read: %v", err)
	}
	for _, id := range []string{"conn_vercel", "conn_foreign", "conn_renamed", "conn_missing"} {
		if c, raw, err := r.Credential(t.Context(), "org_a", id); err == nil || c != nil || raw != nil {
			t.Fatalf("%s was read through the registry-only credentials", id)
		}
	}
	if _, _, err := (registryOnly{}).Credential(t.Context(), "org_a", "conn_registry"); err == nil {
		t.Fatal("no credential source answered")
	}
}

// A suspension's stop goes through the same lifecycle as a user's but leaves
// the placement for the suspension to record, so a restore starts it again.
func TestSuspendStopKeepsThePlacementForTheRestore(t *testing.T) {
	f := newPlatformFixture(t)
	f.deploy(t)
	f.instances.value.State = cpprovider.StateStopping
	backend, err := f.target.backend("fra")
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.SetRunning(WithPlatformSuspend(t.Context()), f.iid, f.instances.value.Labels, false); err != nil {
		t.Fatal(err)
	}
	p := f.platform.live("org_a", "env_1", "web")
	if p == nil || p.State != "running" {
		t.Fatalf("placement %+v, want it left for the suspension", p)
	}
	if records := f.hosts["host_a"].records(t); len(records) != 1 || records[0].State != "stopped" {
		t.Fatalf("host keeps %+v", records)
	}
}
