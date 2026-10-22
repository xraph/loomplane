package deployments

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cpauth "github.com/xraph/ctrlplane/auth"
	"github.com/xraph/ctrlplane/event"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/ctrlplane/store/memory"
	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	nativemysql "github.com/xraph/loomplane/internal/clouds/mysql"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type dialCredentials map[string]*connections.Connection

func (c dialCredentials) Credential(_ context.Context, orgID, id string) (*connections.Connection, []byte, error) {
	conn, ok := c[id]
	if !ok || conn.OrgID != orgID {
		return nil, nil, connections.ErrNotFound
	}
	return conn, []byte(conn.Details["key"]), nil
}

func sshKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(raw)
}

func TestTargetDialFromTheTargetsNetwork(t *testing.T) {
	host, _ := sshKey(t)
	_, user := sshKey(t)
	creds := dialCredentials{
		"conn_server": {ID: "conn_server", OrgID: "org", Cloud: connections.Server, Status: connections.StatusReady, Details: map[string]string{
			"key": user, "host": "server.example", "user": "deploy", "docker_socket": "/var/run/docker.sock",
			"host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))),
		}},
		"conn_cluster": {ID: "conn_cluster", OrgID: "org", Cloud: connections.Kubernetes, Status: connections.StatusReady, Details: map[string]string{}},
		"conn_broken":  {ID: "conn_broken", OrgID: "org", Cloud: connections.Server, Status: connections.StatusFailed, Details: map[string]string{}},
	}

	// A server dials over its pinned SSH connection, opened the way a
	// deployment opens it.
	var dialed *server.Config
	servers := &OwnedTarget{Cloud: connections.Server, Credentials: creds, ServerDial: func(_ context.Context, cfg *server.Config) (*server.Client, error) {
		dialed = cfg
		return nil, errors.New("ssh: handshake refused")
	}}
	if _, _, err := servers.TargetDial(t.Context(), "org", "conn_server"); err == nil || !strings.Contains(err.Error(), "handshake refused") {
		t.Fatalf("server dial: %v", err)
	}
	if dialed == nil || dialed.Address != "server.example:22" || string(dialed.HostKey.Marshal()) != string(host.PublicKey().Marshal()) {
		t.Fatalf("SSH config: %+v", dialed)
	}

	// A cluster's checks dial from Loomplane.
	upstream, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		if conn, acceptErr := upstream.Accept(); acceptErr == nil {
			_, _ = io.WriteString(conn, "ok")
			_ = conn.Close()
		}
	}()
	clusters := &OwnedTarget{Cloud: connections.Kubernetes, Credentials: creds}
	dial, closer, err := clusters.TargetDial(t.Context(), "org", "conn_cluster")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dial(t.Context(), "tcp", upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(conn)
	_ = conn.Close()
	if string(got) != "ok" || closer.Close() != nil {
		t.Fatalf("cluster dial read %q", got)
	}

	for name, tc := range map[string]struct {
		target    *OwnedTarget
		org, conn string
	}{
		"not ready":          {servers, "org", "conn_broken"},
		"another org":        {servers, "other", "conn_server"},
		"another cloud":      {servers, "org", "conn_cluster"},
		"unknown connection": {clusters, "org", "conn_gone"},
		"unowned provider":   {&OwnedTarget{Cloud: "gcp", Credentials: creds}, "org", "conn_server"},
		"no credentials":     {&OwnedTarget{Cloud: connections.Server}, "org", "conn_server"},
	} {
		if dial, closer, err := tc.target.TargetDial(t.Context(), tc.org, tc.conn); err == nil || dial != nil || closer != nil {
			t.Fatalf("%s: dialed (%v)", name, err)
		}
	}
}

// componentBackend stands in for a server's Docker runtime: it records
// the image each component container runs.
type componentBackend struct {
	owned.Backend
	mu      sync.Mutex
	specs   map[id.ID]cpprovider.ServiceSpec
	applied int
}

func (b *componentBackend) Apply(_ context.Context, iid id.ID, spec cpprovider.ServiceSpec, _ map[string]string) (*cpprovider.ProvisionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.specs == nil {
		b.specs = map[id.ID]cpprovider.ServiceSpec{}
	}
	b.specs[iid] = spec
	b.applied++
	return &cpprovider.ProvisionResult{ProviderRef: server.ContainerName(iid), ServiceRefs: map[string]string{spec.Name: "container"}}, nil
}

func (b *componentBackend) Remove(_ context.Context, iid id.ID, _ map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.specs, iid)
	return nil
}

func (b *componentBackend) Status(_ context.Context, iid id.ID, _ map[string]string) (*cpprovider.InstanceStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &cpprovider.InstanceStatus{State: cpprovider.StateRunning, Ready: true, Metadata: map[string]string{"exists": "true", "revision": b.specs[iid].Image}}, nil
}

// databaseComponentBackend also offers the native database lifecycle,
// which a component must never use.
type databaseComponentBackend struct {
	*componentBackend
	t *testing.T
}

func (b databaseComponentBackend) ConfigureDatabase(context.Context, id.ID, map[string]string, string) error {
	b.t.Error("a stateless component was configured as a database")
	return nil
}
func (b databaseComponentBackend) BackupDatabase(context.Context, id.ID, map[string]string, *backupfiles.Store, string) (backupfiles.Object, error) {
	b.t.Error("a stateless component was backed up")
	return backupfiles.Object{}, nil
}
func (b databaseComponentBackend) RestoreDatabase(context.Context, id.ID, map[string]string, *backupfiles.Store, backupfiles.Object) error {
	b.t.Error("a stateless component was restored")
	return nil
}
func (b databaseComponentBackend) DatabaseProbe(context.Context, id.ID, map[string]string) error {
	b.t.Error("a stateless component was probed as a database")
	return nil
}
func (b databaseComponentBackend) PurgeRecovery(context.Context, id.ID, map[string]string) error {
	b.t.Error("a stateless component had recovery purged")
	return nil
}

// countedInstances counts what changes runtime state.
type countedInstances struct {
	instance.Service
	creates, updates int
}

func (s *countedInstances) Create(ctx context.Context, r instance.CreateRequest) (*instance.Instance, error) {
	s.creates++
	return s.Service.Create(ctx, r)
}
func (s *countedInstances) Update(ctx context.Context, iid id.ID, r instance.UpdateRequest) (*instance.Instance, error) {
	s.updates++
	return s.Service.Update(ctx, iid, r)
}

type recordedHooks struct {
	NoHooks
	ready   []ComponentRun
	removed []ComposedResource
}

func (h *recordedHooks) Ready(_ context.Context, run ComponentRun) error {
	h.ready = append(h.ready, run)
	return nil
}
func (h *recordedHooks) Removed(_ context.Context, res ComposedResource) error {
	h.removed = append(h.removed, res)
	return nil
}

type fakeEndpoints struct {
	endpoint *catalogconnect.Endpoint
	dsn      string
}

func (f fakeEndpoints) Resolve(_ context.Context, orgID, endpointID string) (*catalogconnect.Endpoint, string, error) {
	if f.endpoint == nil || orgID != f.endpoint.OrgID || endpointID != f.endpoint.ID {
		return nil, "", catalogconnect.ErrNotFound
	}
	return f.endpoint, f.dsn, nil
}

type fakeChecker struct {
	report *catalogconnect.Report
	checks []string
	reqs   []recipes.Requirement
	dialed bool
}

func (f *fakeChecker) Check(ctx context.Context, orgID, endpointID string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	f.checks = append(f.checks, orgID+"/"+endpointID)
	f.reqs = append(f.reqs, req)
	if _, err := dial(ctx, "tcp", "db.internal:5432"); err == nil {
		f.dialed = true
	}
	return f.report, nil
}

// sampleKinds is the sample kind, changed by edit, in a fresh registry.
func sampleKinds(t *testing.T, edit func(*recipes.Kind)) *recipes.Registry {
	t.Helper()
	k := recipestest.Sample()
	if edit != nil {
		edit(&k)
	}
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	return r
}

type componentFixture struct {
	target    *OwnedTarget
	backend   *componentBackend
	instances *countedInstances
	secrets   *memSecrets
	hooks     *recordedHooks
	kinds     *recipes.Registry
}

func newComponentFixture(t *testing.T, kinds *recipes.Registry) *componentFixture {
	t.Helper()
	providers := cpprovider.NewRegistry()
	store := memory.New()
	instances := &countedInstances{Service: instance.NewService(store, providers, event.NewInMemoryBus(), &cpauth.NoopProvider{}, nil)}
	f := &componentFixture{backend: &componentBackend{}, instances: instances, secrets: &memSecrets{m: map[string][]byte{}}, hooks: &recordedHooks{}, kinds: kinds}
	f.target = &OwnedTarget{
		Cloud: connections.Server, Ctrl: CtrlPlane{Providers: providers, Instances: instances, Deploys: &ownedDeploys{}},
		Secrets: f.secrets, Backend: func(string, string) owned.Backend { return f.backend },
		Kinds: kinds, Hooks: map[string]CatalogHooks{"sample": f.hooks},
	}
	return f
}

var newPostgres = map[string]planning.DependencyChoice{
	"postgres": {Mode: planning.DependencyNew, Resource: "app--postgres", Kind: "postgres"},
	"cache":    {Mode: planning.DependencyOff},
}

// componentJob is the plan's action for one of app's components.
func (f *componentFixture) componentJob(t *testing.T, name string, deps map[string]planning.DependencyChoice) *Job {
	t.Helper()
	composition, err := f.kinds.Resolve("sample", "1", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(composition.Components, func(c recipes.ResolvedComponent) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no component %s", name)
	}
	c := composition.Components[i]
	cfg := &planning.ResourceConfig{
		Provider: "server", Target: "conn", TargetSettings: map[string]string{"architecture": "amd64"}, Kind: "sample", Version: "1",
		Component: &planning.ComponentConfig{Kind: "sample", Resource: "app", Revision: composition.Revision, Component: c, Dependencies: deps, Protection: c.Protection},
	}
	return f.job(planning.ComponentActionName("app", name), cfg)
}

func (f *componentFixture) job(name string, cfg *planning.ResourceConfig) *Job {
	job := &Job{
		Deployment: &Deployment{ID: "dep", StartedBy: "usr", PlanID: "plan", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production"},
		Plan:       &planning.Plan{ID: "plan"},
		Connection: &connections.Connection{ID: "conn", OrgID: "org", Cloud: connections.Server, Status: connections.StatusReady},
		Action:     planning.Action{ID: "resource:" + name, Subject: planning.ResourceSubject, Name: name, Provider: "server", ConnectionID: "conn", Resource: cfg},
		Progress:   func(string, string) {}, Checkpoint: func([]byte) error { return nil },
	}
	job.Keep = func(ctx context.Context, field string, value []byte) error {
		return f.secrets.Put(ctx, resourceKey("org", "env", name, field), value)
	}
	job.Recall = func(ctx context.Context, field string) ([]byte, error) {
		return f.secrets.Get(ctx, resourceKey("org", "env", name, field))
	}
	return job
}

// bound is the binding an applied job leaves.
func bound(job *Job, out *Outcome) *planning.Binding {
	raw, _ := json.Marshal(job.Action.Resource)
	return &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: job.Action.Name, Provider: "server", ConnectionID: "conn", ExternalID: out.ExternalID, Config: raw}
}

// started is the spec and labels Ctrl Plane keeps for an applied
// component, and what its container gets: the spec's variables and its
// runtime values, secrets included.
func (f *componentFixture) started(t *testing.T, out *Outcome) (cpprovider.ServiceSpec, map[string]string, map[string]string) {
	t.Helper()
	iid, err := id.Parse(out.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := f.instances.Get(infrastructure.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}), iid)
	if err != nil || len(inst.Services) != 1 {
		t.Fatalf("instance: %+v %v", inst, err)
	}
	spec, labels := inst.Services[0], inst.Labels
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	var values map[string]string
	if err := json.Unmarshal(f.secrets.m[labels[owned.LabelRuntime]], &values); err != nil {
		t.Fatalf("runtime values: %v", err)
	}
	maps.Copy(env, values)
	return spec, labels, env
}

const postgresURL = "postgresql://loomplane:pg-Secret-1@lp-inst-db:5432/app__postgres?sslmode=disable"

func TestStatelessComponentHasNoVolumeOrStatefulLabel(t *testing.T) {
	f := newComponentFixture(t, recipestest.Registry(t))
	job := f.componentJob(t, "api", newPostgres)
	svc := componentService(job.Action.Resource)
	if svc == nil {
		t.Fatal("no service for the api component")
	}
	if svc.Public || svc.Port != 8080 || svc.HealthPath != "/readyz" || svc.Workload != provider.HTTPService || svc.BuildTarget == nil || svc.BuildTarget.Image != job.Action.Resource.Component.Component.Image {
		t.Fatalf("component service: %+v", svc)
	}
	copyJob := *job
	copyJob.Action.Resource, copyJob.Action.Service = nil, svc
	sp, labels := ownedSpec(&copyJob, svc.BuildTarget.Image, ownedState{})
	if len(sp.Volumes) != 0 {
		t.Fatalf("stateless component has volumes: %+v", sp.Volumes)
	}
	if _, ok := labels[owned.LabelStateful]; ok {
		t.Fatal("stateless component is labelled stateful")
	}
	for key, want := range map[string]string{
		owned.LabelHealth: "/readyz", owned.LabelHealthPort: "8080", owned.LabelPublic: "false",
		owned.LabelCatalogKind: "sample", owned.LabelCatalogResource: "app", owned.LabelCatalogComponent: "api",
	} {
		if labels[key] != want {
			t.Fatalf("label %s = %q, want %q", key, labels[key], want)
		}
	}
	if want := []cpprovider.PortSpec{{Container: 8080, Protocol: "tcp"}, {Container: 9090, Protocol: "tcp"}}; !reflect.DeepEqual(sp.Ports, want) {
		t.Fatalf("ports: %+v", sp.Ports)
	}
	if !slices.Equal(sp.Command, []string{"sample", "serve"}) || sp.Resources.CPUMillis != 500 || sp.Resources.MemoryMB != 256 {
		t.Fatalf("command or resources: %+v", sp)
	}

	// A plan made from another recipe isn't applied, and a target that
	// can't run jobs doesn't run one.
	changed := f.componentJob(t, "api", newPostgres)
	changed.Action.Resource.Component.Component.Image = "registry.example/other@sha256:" + strings.Repeat("c", 64)
	if _, err := f.target.Apply(t.Context(), changed); err == nil || !strings.Contains(err.Error(), "recipe changed") {
		t.Fatalf("changed recipe: %v", err)
	}
	if _, err := f.target.Apply(t.Context(), f.componentJob(t, "migrate", newPostgres)); err == nil || err.Error() != "app--migrate is a job, and this server target can't run catalog jobs" {
		t.Fatalf("job component: %v", err)
	}
	if f.instances.creates != 0 {
		t.Fatal("a refused component created an instance")
	}
}

func TestComponentGeneratedSecretsAreKeptOncePerComposition(t *testing.T) {
	kinds := sampleKinds(t, func(k *recipes.Kind) {
		k.Secrets[0].Encoding = "hex"
		k.Secrets[0].Bytes = 16
		k.Components[2].Env = append(k.Components[2].Env, recipes.EnvVar{Name: "ADMIN_PASSWORD", Secret: "admin-password"})
	})
	f := newComponentFixture(t, kinds)
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	key := resourceKey("org", "env", "app", "secret:admin-password")

	api := f.componentJob(t, "api", newPostgres)
	out, err := f.target.Apply(t.Context(), api)
	if err != nil {
		t.Fatal(err)
	}
	stored := string(f.secrets.m[key])
	if len(stored) != 32 || strings.Trim(stored, "0123456789abcdef") != "" {
		t.Fatalf("generated secret %q isn't 16 hex-encoded bytes", stored)
	}
	spec, _, env := f.started(t, out)
	if env["ADMIN_PASSWORD"] != stored || spec.Env["ADMIN_PASSWORD"] != "" {
		t.Fatalf("api secret: env %q, spec %q", env["ADMIN_PASSWORD"], spec.Env["ADMIN_PASSWORD"])
	}

	worker, err := f.target.Apply(t.Context(), f.componentJob(t, "worker", newPostgres))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, env := f.started(t, worker); env["ADMIN_PASSWORD"] != stored {
		t.Fatalf("worker got another secret: %q", env["ADMIN_PASSWORD"])
	}

	again := f.componentJob(t, "api", newPostgres)
	again.Binding = bound(api, out)
	out2, err := f.target.Apply(t.Context(), again)
	if err != nil {
		t.Fatal(err)
	}
	if string(f.secrets.m[key]) != stored {
		t.Fatal("a second apply generated the secret again")
	}
	if _, _, env := f.started(t, out2); env["ADMIN_PASSWORD"] != stored {
		t.Fatal("a second apply changed the api's secret")
	}

	delete(f.secrets.m, key)
	lost := f.componentJob(t, "api", newPostgres)
	lost.Binding = bound(api, out2)
	if _, err := f.target.Apply(t.Context(), lost); err == nil || err.Error() != "the generated secret admin-password is missing; recover it before changing the service" {
		t.Fatalf("missing secret: %v", err)
	}
	if _, ok := f.secrets.m[key]; ok {
		t.Fatal("a missing secret of a bound component was generated again")
	}
}

func TestComponentRevisionAddingASecretGeneratesIt(t *testing.T) {
	f := newComponentFixture(t, recipestest.Registry(t))
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	first := f.componentJob(t, "api", newPostgres)
	out, err := f.target.Apply(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}

	// Revision 2 adds a secret the deployed api now reads.
	f.kinds = sampleKinds(t, func(k *recipes.Kind) {
		k.Releases[0].Revision = "2"
		k.Secrets = append(k.Secrets, recipes.GeneratedSecret{Name: "api-token"})
		k.Components[1].Env = append(k.Components[1].Env, recipes.EnvVar{Name: "API_TOKEN", Secret: "api-token"})
	})
	f.target.Kinds = f.kinds
	upgrade := f.componentJob(t, "api", newPostgres)
	upgrade.Binding = bound(first, out)
	out, err = f.target.Apply(t.Context(), upgrade)
	if err != nil {
		t.Fatalf("a secret the new revision adds: %v", err)
	}
	token := string(f.secrets.m[resourceKey("org", "env", "app", "secret:api-token")])
	if _, _, env := f.started(t, out); token == "" || env["API_TOKEN"] != token {
		t.Fatalf("API_TOKEN = %q, kept %q", env["API_TOKEN"], token)
	}

	// A secret the deployed component already read is lost, not new.
	delete(f.secrets.m, resourceKey("org", "env", "app", "secret:admin-password"))
	again := f.componentJob(t, "api", newPostgres)
	again.Binding = bound(first, out)
	if _, err = f.target.Apply(t.Context(), again); err == nil || err.Error() != "the generated secret admin-password is missing; recover it before changing the service" {
		t.Fatalf("lost secret across revisions: %v", err)
	}
}

func TestComponentEnvResolvesDependencyFields(t *testing.T) {
	kinds := sampleKinds(t, func(k *recipes.Kind) {
		for field, name := range map[string]string{
			recipes.FieldHost: "PGHOST", recipes.FieldPort: "PGPORT", recipes.FieldHostPort: "PGHOSTPORT",
			recipes.FieldUsername: "PGUSER", recipes.FieldPassword: "PGPASSWORD", recipes.FieldDatabase: "PGDATABASE",
		} {
			k.Components[1].Env = append(k.Components[1].Env, recipes.EnvVar{Name: name, From: &recipes.Ref{Dependency: "postgres", Field: field}})
		}
	})
	f := newComponentFixture(t, kinds)
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	out, err := f.target.Apply(t.Context(), f.componentJob(t, "api", newPostgres))
	if err != nil {
		t.Fatal(err)
	}
	spec, _, env := f.started(t, out)
	want := map[string]string{
		"DATABASE_URL": postgresURL, "PGHOST": "lp-inst-db", "PGPORT": "5432", "PGHOSTPORT": "lp-inst-db:5432",
		"PGUSER": "loomplane", "PGPASSWORD": "pg-Secret-1", "PGDATABASE": "app__postgres",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("%s = %q, want %q", key, env[key], value)
		}
		if _, plain := spec.Env[key]; plain {
			t.Fatalf("%s entered the Ctrl Plane spec", key)
		}
	}
	if _, ok := env["REDIS_URL"]; ok {
		t.Fatal("an optional dependency that's off still set its variable")
	}
	raw, _ := json.Marshal(spec)
	if strings.Contains(string(raw), "pg-Secret-1") {
		t.Fatal("the dependency password entered the Ctrl Plane spec")
	}

	// Ready gets the resolved run once the process passes readiness.
	if len(f.hooks.ready) != 1 {
		t.Fatalf("ready hook ran %d times", len(f.hooks.ready))
	}
	run := f.hooks.ready[0]
	if run.Resource != "app" || run.Kind != "sample" || run.Provider != "server" || run.Component.Name != "api" || run.OrgID != "org" || run.EnvironmentID != "env" || run.Environment != "production" || run.DeploymentID != "dep" || run.PlanID != "plan" {
		t.Fatalf("ready run: %+v", run)
	}
	if run.Env["PGPASSWORD"] != "pg-Secret-1" || string(run.Secrets["admin-password"]) != env["ADMIN_PASSWORD"] {
		t.Fatalf("ready run env or secrets: %+v %+v", run.Env, run.Secrets)
	}

	// Without the dependency's address the component doesn't start.
	fresh := newComponentFixture(t, kinds)
	if _, err := fresh.target.Apply(t.Context(), fresh.componentJob(t, "api", newPostgres)); err == nil || fresh.instances.creates != 0 {
		t.Fatalf("missing dependency: %v", err)
	}
}

func TestComponentEnvResolvesSiblingAddress(t *testing.T) {
	f := newComponentFixture(t, recipestest.Registry(t))
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	worker := f.componentJob(t, "worker", newPostgres)
	if _, err := f.target.Apply(t.Context(), worker); err == nil || f.instances.creates != 0 {
		t.Fatalf("worker started before the api: %v", err)
	}
	api, err := f.target.Apply(t.Context(), f.componentJob(t, "api", newPostgres))
	if err != nil {
		t.Fatal(err)
	}
	iid, _ := id.Parse(api.ExternalID)
	host := server.ContainerName(iid)
	var address map[string]string
	if err = json.Unmarshal(f.secrets.m[resourceKey("org", "env", "app--api", "address")], &address); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"http": "http://" + host + ":8080", "admin": "http://" + host + ":9090"}; !maps.Equal(address, want) {
		t.Fatalf("api address: %v", address)
	}
	out, err := f.target.Apply(t.Context(), f.componentJob(t, "worker", newPostgres))
	if err != nil {
		t.Fatal(err)
	}
	spec, _, env := f.started(t, out)
	if env["API_URL"] != "http://"+host+":8080" || spec.Env["API_URL"] != env["API_URL"] {
		t.Fatalf("worker API_URL = %q", env["API_URL"])
	}
	// The worker reads no secret, and its hooks still get the composition's.
	run := f.hooks.ready[len(f.hooks.ready)-1]
	if _, ok := env["ADMIN_PASSWORD"]; ok || run.Component.Name != "worker" || len(run.Secrets) != 1 || string(run.Secrets["admin-password"]) != string(f.secrets.m[resourceKey("org", "env", "app", "secret:admin-password")]) {
		t.Fatalf("worker run secrets: %v", run.Secrets)
	}

	// Removing a component forgets its address with its runtime values.
	apiJob := f.componentJob(t, "api", newPostgres)
	if err = f.target.Remove(t.Context(), apiJob, bound(apiJob, api)); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.secrets.m[resourceKey("org", "env", "app--api", "address")]; ok {
		t.Fatal("a removed component's address survived")
	}
}

func TestComponentRecheckFailsBeforeRuntimeChanges(t *testing.T) {
	const dsn = "postgres://app:hunter2-Secret@db.internal:5432/app?sslmode=require"
	external := map[string]planning.DependencyChoice{
		"postgres": {Mode: planning.DependencyExternal, External: "endpoint_01k0000000000000000000000a"},
		"cache":    {Mode: planning.DependencyOff},
	}
	f := newComponentFixture(t, recipestest.Registry(t))
	checker := &fakeChecker{report: &catalogconnect.Report{Problems: []string{"db.internal:5432 isn't reachable from the target: dial tcp 10.0.0.5:5432: connect: connection refused"}}}
	f.target.Endpoints = fakeEndpoints{endpoint: &catalogconnect.Endpoint{ID: "endpoint_01k0000000000000000000000a", OrgID: "org", Name: "prod-pg", Engine: "postgres", Host: "db.internal", Port: 5432}, dsn: dsn}
	f.target.Checker = checker
	var dialedFrom string
	f.target.endpointDial = func(_ context.Context, orgID, connectionID string) (catalogconnect.DialFunc, io.Closer, error) {
		dialedFrom = orgID + "/" + connectionID
		return func(context.Context, string, string) (net.Conn, error) {
			client, peer := net.Pipe()
			_ = peer.Close()
			return client, nil
		}, noClose{}, nil
	}
	_, err := f.target.Apply(t.Context(), f.componentJob(t, "api", external))
	if err == nil || !strings.Contains(err.Error(), "endpoint prod-pg isn't reachable from the target") {
		t.Fatalf("unreachable endpoint: %v", err)
	}
	if strings.Contains(err.Error(), dsn) || strings.Contains(err.Error(), "hunter2-Secret") {
		t.Fatalf("the error shows the DSN: %v", err)
	}
	if f.instances.creates != 0 || f.instances.updates != 0 || f.backend.applied != 0 {
		t.Fatal("runtime changed before the endpoint was rechecked")
	}
	if dialedFrom != "org/conn" || !checker.dialed || !slices.Equal(checker.checks, []string{"org/endpoint_01k0000000000000000000000a"}) || checker.reqs[0].Engine != "postgres" || checker.reqs[0].MinVersion != "16" {
		t.Fatalf("check: from %q, %+v", dialedFrom, checker)
	}

	// A passing check starts the component with the DSN as a secret.
	checker.report = &catalogconnect.Report{Version: "17.2"}
	out, err := f.target.Apply(t.Context(), f.componentJob(t, "api", external))
	if err != nil {
		t.Fatal(err)
	}
	spec, _, env := f.started(t, out)
	if env["DATABASE_URL"] != dsn || spec.Env["DATABASE_URL"] != "" {
		t.Fatalf("external DATABASE_URL: env %q spec %q", env["DATABASE_URL"], spec.Env["DATABASE_URL"])
	}
}

func TestCompositionPublishesOutputs(t *testing.T) {
	kinds := recipestest.Registry(t)
	f := newComponentFixture(t, kinds)
	f.secrets.m[resourceKey("org", "env", "app--api", "address")] = []byte(`{"http":"http://lp-inst-api:8080","admin":"http://lp-inst-api:9090"}`)
	kind, _ := kinds.Kind("sample")
	cfg := &planning.ResourceConfig{
		Provider: "server", Target: "conn", TargetSettings: map[string]string{"architecture": "amd64"}, Kind: "sample", Version: "1",
		Composition: &planning.CompositionConfig{Kind: "sample", Version: "1", Revision: "1", Dependencies: newPostgres, Outputs: kind.Outputs, Secrets: kind.Secrets},
	}
	job := f.job("app", cfg)
	out, err := f.target.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExternalID != "catalog/app" {
		t.Fatalf("external ID %q", out.ExternalID)
	}
	if got := string(f.secrets.m[resourceKey("org", "env", "app", "url")]); got != "http://lp-inst-api:8080" {
		t.Fatalf("url = %q", got)
	}
	if got := string(f.secrets.m[resourceKey("org", "env", "app", "output:admin")]); got != "http://lp-inst-api:9090" {
		t.Fatalf("output:admin = %q", got)
	}
	if f.instances.creates != 0 {
		t.Fatal("the composed resource itself started something")
	}

	b := bound(job, out)
	observed, err := f.target.Observe(t.Context(), job.Connection, nil, b)
	if err != nil || !observed.Exists {
		t.Fatalf("observe: %+v %v", observed, err)
	}

	f.secrets.m[resourceKey("org", "env", "app", "secret:admin-password")] = []byte("generated")
	f.secrets.m[resourceKey("org", "env", "app--api", "url")] = []byte("kept")
	if err := f.target.Remove(t.Context(), job, b); err != nil {
		t.Fatal(err)
	}
	if len(f.hooks.removed) != 1 || f.hooks.removed[0] != (ComposedResource{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "app", Kind: "sample"}) {
		t.Fatalf("removed hook: %+v", f.hooks.removed)
	}
	for _, field := range []string{"url", "output:admin", "secret:admin-password"} {
		if _, ok := f.secrets.m[resourceKey("org", "env", "app", field)]; ok {
			t.Fatalf("%s survived removing the composed resource", field)
		}
	}
	if _, ok := f.secrets.m[resourceKey("org", "env", "app--api", "url")]; !ok {
		t.Fatal("removing the composed resource deleted a component's value")
	}
}

func TestRunnerEnvReadsLinkField(t *testing.T) {
	secrets := &memSecrets{m: map[string][]byte{
		resourceKey("org", "env", "app", "url"):          []byte("http://lp-inst-api:8080"),
		resourceKey("org", "env", "app", "output:admin"): []byte("http://lp-inst-api:9090"),
		resourceKey("org", "env", "db", "url"):           []byte(postgresURL),
	}}
	r := &Runner{resources: secrets, variables: fakeVariables{}}
	d := &Deployment{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production"}
	a := planning.Action{Name: "web", Service: &planning.ServiceConfig{Service: "web", Env: []planning.EnvLink{
		{Key: "SAMPLE_URL", From: "app", Subject: planning.ResourceSubject, Field: "url"},
		{Key: "SAMPLE_ADMIN_URL", From: "app", Subject: planning.ResourceSubject, Field: "output:admin"},
		{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject},
	}}}
	resources := map[string]*planning.Binding{"app": {Name: "app"}, "db": {Name: "db"}}
	env, secret, err := r.env(t.Context(), d, a, nil, resources)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"SAMPLE_URL": "http://lp-inst-api:8080", "SAMPLE_ADMIN_URL": "http://lp-inst-api:9090", "DATABASE_URL": postgresURL}
	if !maps.Equal(env, want) || len(secret) != 3 {
		t.Fatalf("env %v, secret %v", env, secret)
	}

	a.Service.Env = []planning.EnvLink{{Key: "SAMPLE_TOKEN", From: "app", Subject: planning.ResourceSubject, Field: "consumer:x"}}
	if _, _, err = r.env(t.Context(), d, a, nil, resources); err == nil || !strings.Contains(err.Error(), "can't give it yet") {
		t.Fatalf("unsupported field: %v", err)
	}
}

func TestStatelessComponentRecoveryLabelsAbsent(t *testing.T) {
	f := newComponentFixture(t, recipestest.Registry(t))
	f.target.Backend = func(string, string) owned.Backend { return databaseComponentBackend{componentBackend: f.backend, t: t} }
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	out, err := f.target.Apply(t.Context(), f.componentJob(t, "api", newPostgres))
	if err != nil {
		t.Fatal(err)
	}
	_, labels, _ := f.started(t, out)
	for _, key := range []string{owned.LabelStateful, server.LabelDatabase, server.LabelRecovery, nativemysql.LabelDatabase, nativemysql.LabelEngine, nativecache.LabelEngine} {
		if _, ok := labels[key]; ok {
			t.Fatalf("stateless component carries %s", key)
		}
	}
	if _, ok := f.secrets.m[resourceKey("org", "env", "app--api", "url")]; ok {
		t.Fatal("a component kept a database address")
	}
}

// jobRun is one RunJob call, with what the job's container got: the
// spec's variables and its runtime values, secrets included.
type jobRun struct {
	name    string
	spec    cpprovider.ServiceSpec
	labels  map[string]string
	env     map[string]string
	timeout time.Duration
}

// jobComponentBackend also runs jobs, which end as result says.
type jobComponentBackend struct {
	*componentBackend
	secrets *memSecrets
	result  owned.JobResult
	runs    []jobRun
}

func (b *jobComponentBackend) RunJob(ctx context.Context, name string, spec cpprovider.ServiceSpec, labels map[string]string, timeout time.Duration) (owned.JobResult, error) {
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	raw, err := b.secrets.Get(ctx, labels[owned.LabelRuntime])
	if err != nil {
		return owned.JobResult{}, err
	}
	var values map[string]string
	if err = json.Unmarshal(raw, &values); err != nil {
		return owned.JobResult{}, err
	}
	maps.Copy(env, values)
	b.runs = append(b.runs, jobRun{name: name, spec: spec, labels: labels, env: env, timeout: timeout})
	return b.result, nil
}

func newJobFixture(t *testing.T, result owned.JobResult) (*componentFixture, *jobComponentBackend) {
	t.Helper()
	f := newComponentFixture(t, recipestest.Registry(t))
	jobs := &jobComponentBackend{componentBackend: f.backend, secrets: f.secrets, result: result}
	f.target.Backend = func(string, string) owned.Backend { return jobs }
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	return f, jobs
}

func TestRunJobFailureStopsLaterComponents(t *testing.T) {
	f, jobs := newJobFixture(t, owned.JobResult{ExitCode: 3, Output: "migration 0003 failed: relation \"users\" exists"})
	migrate := f.componentJob(t, "migrate", newPostgres).Action
	api := f.componentJob(t, "api", newPostgres).Action
	migrate.Op, migrate.Step = planning.Create, 2
	api.Op, api.Step = planning.Create, 3
	plan := &planning.Plan{
		ID: "plan", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production",
		Status: planning.StatusApproved, Executable: true, Connections: map[string]string{"server": "conn"},
		Actions: []planning.Action{migrate, api},
	}
	store := &memStore{d: map[string]*Deployment{}}
	r := NewRunner(RunnerDeps{
		Store: store, Plans: &fakePlans{plan: plan, bindings: map[string]*planning.Binding{}}, Variables: fakeVariables{}, Sources: &fakeSources{},
		Credentials: dialCredentials{"conn": {ID: "conn", OrgID: "org", Cloud: connections.Server, Status: connections.StatusReady}},
		Providers:   providers.Registry(), Executors: map[string]Executor{"server": f.target}, Resources: f.secrets,
	})
	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org", id)
	if d.Status != StatusFailed || len(d.Steps) != 2 {
		t.Fatalf("deployment %+v", d)
	}
	if want := "migrate exited with code 3: migration 0003 failed: relation \"users\" exists"; d.Steps[0].Status != StepFailed || d.Steps[0].Message != want {
		t.Fatalf("migrate step: %+v", d.Steps[0])
	}
	if d.Steps[1].ActionID != "resource:app--api" || d.Steps[1].Status != StepSkipped {
		t.Fatalf("api step: %+v", d.Steps[1])
	}
	if len(jobs.runs) != 1 || f.backend.applied != 0 || f.instances.creates != 0 {
		t.Fatalf("after a failed job: %d runs, %d applies, %d instances", len(jobs.runs), f.backend.applied, f.instances.creates)
	}
}

func TestRunJobSucceededIsNotRerunOnResume(t *testing.T) {
	f, jobs := newJobFixture(t, owned.JobResult{Output: "migrated"})
	job := f.componentJob(t, "migrate", newPostgres)
	var saved [][]byte
	job.Checkpoint = func(raw []byte) error { saved = append(saved, raw); return nil }
	out, err := f.target.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("dep/resource:app--migrate"))
	name := "lp-job-" + hex.EncodeToString(sum[:])[:12]
	image := job.Action.Resource.Component.Component.Image
	if out.ExternalID != "job/"+name || out.Revision != image {
		t.Fatalf("outcome: %+v", out)
	}
	if len(jobs.runs) != 1 {
		t.Fatalf("ran %d times", len(jobs.runs))
	}
	run := jobs.runs[0]
	if run.name != name || run.spec.Image != image || !slices.Equal(run.spec.Command, []string{"sample", "migrate"}) || run.timeout != 10*time.Minute {
		t.Fatalf("run: %+v", run)
	}
	if run.env["DATABASE_URL"] != postgresURL || run.spec.Env["DATABASE_URL"] != "" {
		t.Fatalf("job DATABASE_URL: env %q, spec %q", run.env["DATABASE_URL"], run.spec.Env["DATABASE_URL"])
	}
	for key, want := range map[string]string{owned.LabelCatalogComponent: "migrate", owned.LabelCatalogResource: "app", owned.LabelArchitecture: "amd64", owned.LabelOrg: "org", owned.LabelEnvironment: "env"} {
		if run.labels[key] != want {
			t.Fatalf("label %s = %q, want %q", key, run.labels[key], want)
		}
	}
	if _, kept := f.secrets.m[run.labels[owned.LabelRuntime]]; kept {
		t.Fatal("the job's runtime values outlived it")
	}
	if len(saved) != 2 || string(saved[0]) != `{"job":"`+name+`","state":"running"}` || string(saved[1]) != `{"job":"`+name+`","state":"succeeded"}` {
		t.Fatalf("checkpoints: %q", saved)
	}

	// A retry of the step that finds the job succeeded doesn't run it again.
	resumed := f.componentJob(t, "migrate", newPostgres)
	resumed.Resume = saved[1]
	again, err := f.target.Apply(t.Context(), resumed)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs.runs) != 1 || *again != *out {
		t.Fatalf("resumed: %d runs, %+v", len(jobs.runs), again)
	}
	// One that was still running runs it again.
	running := f.componentJob(t, "migrate", newPostgres)
	running.Resume = saved[0]
	if _, err = f.target.Apply(t.Context(), running); err != nil || len(jobs.runs) != 2 {
		t.Fatalf("a running job's retry: %d runs, %v", len(jobs.runs), err)
	}

	// Nothing runs as a job once it's done, so there's nothing to remove
	// and nothing to observe.
	b := bound(job, out)
	if err = f.target.Remove(t.Context(), job, b); err != nil || len(jobs.runs) != 2 {
		t.Fatalf("remove: %v", err)
	}
	if observed, err := f.target.Observe(t.Context(), job.Connection, nil, b); err != nil || !observed.Exists {
		t.Fatalf("observe: %+v %v", observed, err)
	}
}

func TestKubernetesComponentAddressIsItsServiceHost(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	c := recipestest.Sample().Components[1]
	cfg := &planning.ResourceConfig{Provider: "kubernetes", TargetSettings: map[string]string{"namespace": "apps"}, Component: &planning.ComponentConfig{Component: recipes.ResolvedComponent{Component: c}}}
	raw, err := componentAddress(cfg, iid)
	if err != nil {
		t.Fatal(err)
	}
	host := kubernetes.WorkloadName(iid) + ".apps.svc"
	var got map[string]string
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"http": "http://" + host + ":8080", "admin": "http://" + host + ":9090"}; !maps.Equal(got, want) {
		t.Fatalf("component addresses: %v, want %v", got, want)
	}
	cfg.TargetSettings["namespace"] = "Not_A_Label"
	if _, err = componentAddress(cfg, iid); err == nil {
		t.Fatal("address in an invalid namespace")
	}
}

// fileHooks render what files says, and record each run they render for.
type fileHooks struct {
	recordedHooks
	files    map[string][]byte
	rendered []ComponentRun
}

func (h *fileHooks) Files(_ context.Context, run ComponentRun) (map[string][]byte, error) {
	h.rendered = append(h.rendered, run)
	return maps.Clone(h.files), nil
}

const sampleConfig = "/etc/sample/config.yaml"

// sampleWithFiles is the sample kind whose api and migrate declare a
// rendered config file.
func sampleWithFiles(t *testing.T) *recipes.Registry {
	t.Helper()
	return sampleKinds(t, func(k *recipes.Kind) {
		for i := range k.Components {
			if k.Components[i].Name == "api" || k.Components[i].Name == "migrate" {
				k.Components[i].Files = []recipes.File{{Path: sampleConfig}}
			}
		}
	})
}

func TestComponentFilesComeFromHooksAndStayInVault(t *testing.T) {
	const content = "listen: 8080\nadmin_password: file-Secret-1\n"
	f := newComponentFixture(t, sampleWithFiles(t))
	hooks := &fileHooks{files: map[string][]byte{sampleConfig: []byte(content)}}
	f.target.Hooks["sample"] = hooks
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)

	api := f.componentJob(t, "api", newPostgres)
	out, err := f.target.Apply(t.Context(), api)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks.rendered) != 1 || hooks.rendered[0].Component.Name != "api" || hooks.rendered[0].Env["DATABASE_URL"] != postgresURL {
		t.Fatalf("files rendered for %+v", hooks.rendered)
	}
	spec, labels, _ := f.started(t, out)
	if labels[owned.LabelRuntimeFiles] != "true" {
		t.Fatalf("label %s = %q", owned.LabelRuntimeFiles, labels[owned.LabelRuntimeFiles])
	}
	kept, ok := f.secrets.m[labels[owned.LabelRuntime]+"/files"]
	if !ok || !strings.Contains(string(kept), sampleConfig) {
		t.Fatalf("Vault keeps no files at %s/files: %q", labels[owned.LabelRuntime], kept)
	}
	values, err := f.target.values(t.Context(), labels)
	if err != nil {
		t.Fatal(err)
	}
	if string(values.Files[sampleConfig]) != content || values.FileModes[sampleConfig] != 0o444 || len(values.Files) != 1 {
		t.Fatalf("runtime files: %q, modes %v", values.Files, values.FileModes)
	}
	if len(spec.ConfigFiles) != 0 {
		t.Fatalf("the Ctrl Plane spec has config files: %+v", spec.ConfigFiles)
	}
	rawSpec, _ := json.Marshal(spec)
	rawLabels, _ := json.Marshal(labels)
	for _, raw := range [][]byte{rawSpec, rawLabels} {
		if strings.Contains(string(raw), "file-Secret-1") || strings.Contains(string(raw), "listen: 8080") {
			t.Fatalf("file bytes entered Ctrl Plane: %s", raw)
		}
	}

	// The rendered bytes are part of the revision: the same deployment
	// rendering another file starts another container.
	first := labels[owned.LabelRevision]
	if !strings.HasPrefix(first, "dep/resource:app--api/") {
		t.Fatalf("revision %q", first)
	}
	revision := func(mode fs.FileMode, data string) string {
		copyJob := *api
		copyJob.Action.Resource, copyJob.Action.Service = nil, componentService(api.Action.Resource)
		copyJob.files = []runtimeFile{{Path: sampleConfig, Mode: mode, Data: []byte(data)}}
		_, l := ownedSpec(&copyJob, "", ownedState{})
		return l[owned.LabelRevision]
	}
	if revision(0o444, content) != first || revision(0o444, content+" ") == first || revision(0o440, content) == first {
		t.Fatal("the revision doesn't follow the rendered bytes and modes")
	}
	hooks.files[sampleConfig] = []byte(content + "debug: true\n")
	changed := f.componentJob(t, "api", newPostgres)
	changed.Binding = bound(api, out)
	out2, err := f.target.Apply(t.Context(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, labels2, _ := f.started(t, out2); labels2[owned.LabelRevision] == first || labels2[owned.LabelRuntime] == labels[owned.LabelRuntime] {
		t.Fatalf("a changed file kept revision %q", labels2[owned.LabelRevision])
	}
	if _, ok := f.secrets.m[labels[owned.LabelRuntime]+"/files"]; ok {
		t.Fatal("the replaced runtime's files outlived it")
	}

	// A component without files keeps its revision, and nothing of it is
	// kept as files.
	worker, err := f.target.Apply(t.Context(), f.componentJob(t, "worker", newPostgres))
	if err != nil {
		t.Fatal(err)
	}
	_, workerLabels, _ := f.started(t, worker)
	if workerLabels[owned.LabelRevision] != "dep/resource:app--worker" {
		t.Fatalf("worker revision %q", workerLabels[owned.LabelRevision])
	}
	if _, ok := workerLabels[owned.LabelRuntimeFiles]; ok {
		t.Fatal("a component without files is labelled with them")
	}
	if _, ok := f.secrets.m[workerLabels[owned.LabelRuntime]+"/files"]; ok || len(hooks.rendered) != 2 {
		t.Fatalf("files rendered for a component without any: %d renders", len(hooks.rendered))
	}

	// What the hook leaves out, or adds, stops the component before it
	// starts.
	creates := f.instances.creates
	hooks.files = map[string][]byte{}
	fresh := newComponentFixture(t, f.kinds)
	fresh.target.Hooks["sample"] = hooks
	fresh.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	if _, err = fresh.target.Apply(t.Context(), fresh.componentJob(t, "api", newPostgres)); err == nil || err.Error() != "sample did not render /etc/sample/config.yaml" {
		t.Fatalf("missing file: %v", err)
	}
	hooks.files = map[string][]byte{sampleConfig: []byte(content), "/etc/sample/extra.yaml": []byte("x")}
	if _, err = fresh.target.Apply(t.Context(), fresh.componentJob(t, "api", newPostgres)); err == nil || err.Error() != "sample rendered /etc/sample/extra.yaml, which api doesn't declare" {
		t.Fatalf("undeclared file: %v", err)
	}
	if fresh.instances.creates != 0 || f.instances.creates != creates {
		t.Fatal("a component started without its files")
	}

	// Removing the component forgets its files with its runtime values.
	final := f.componentJob(t, "api", newPostgres)
	_, finalLabels, _ := f.started(t, out2)
	if err = f.target.Remove(t.Context(), final, bound(final, out2)); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.secrets.m[finalLabels[owned.LabelRuntime]+"/files"]; ok {
		t.Fatal("a removed component's files survived")
	}
}

func TestComponentJobFilesAreKeptWhileItRuns(t *testing.T) {
	f := newComponentFixture(t, sampleWithFiles(t))
	hooks := &fileHooks{files: map[string][]byte{sampleConfig: []byte("migrate: true\n")}}
	f.target.Hooks["sample"] = hooks
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	var seen owned.RuntimeValues
	jobs := &filesJobBackend{componentBackend: f.backend, run: func(ctx context.Context, labels map[string]string) error {
		var err error
		seen, err = f.target.values(ctx, labels)
		return err
	}}
	f.target.Backend = func(string, string) owned.Backend { return jobs }
	if _, err := f.target.Apply(t.Context(), f.componentJob(t, "migrate", newPostgres)); err != nil {
		t.Fatal(err)
	}
	if string(seen.Files[sampleConfig]) != "migrate: true\n" || jobs.labels[owned.LabelRuntimeFiles] != "true" || !strings.HasPrefix(jobs.labels[owned.LabelRevision], "dep/resource:app--migrate/") {
		t.Fatalf("job files %q, labels %v", seen.Files, jobs.labels)
	}
	if _, ok := f.secrets.m[jobs.labels[owned.LabelRuntime]+"/files"]; ok {
		t.Fatal("the job's files outlived it")
	}
}

// filesJobBackend runs a job by calling run with its labels.
type filesJobBackend struct {
	*componentBackend
	run    func(context.Context, map[string]string) error
	labels map[string]string
}

func (b *filesJobBackend) RunJob(ctx context.Context, _ string, _ cpprovider.ServiceSpec, labels map[string]string, _ time.Duration) (owned.JobResult, error) {
	b.labels = labels
	return owned.JobResult{}, b.run(ctx, labels)
}
