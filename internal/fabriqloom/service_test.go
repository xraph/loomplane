package fabriqloom_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

var fabriqRef = deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq"}

// Who may do what, by user id.
var roles = map[string][]security.Permission{
	"admin":     {security.DeploymentRead, security.DeploymentCreate, security.SecretManage},
	"developer": {security.DeploymentRead, security.DeploymentCreate},
	"viewer":    {security.DeploymentRead},
}

func as(user string) security.Principal { return security.Principal{OrgID: "org", UserID: user} }

type jobCall struct {
	Component, Name string
	Ref             deployments.ResourceRef
}

// fakeCatalog is CatalogAccess for one hosted Fabriq, "fabriq" in proj's
// env, with the console reaching the fake.
type fakeCatalog struct {
	f    *fakeFabriq
	mu   sync.Mutex
	kind string
	// on are the optional dependencies that are on.
	on         map[string]bool
	roles      map[string][]security.Permission
	channelErr error
	result     owned.JobResult
	jobErr     error
	jobs       []jobCall
}

func newFakeCatalog(f *fakeFabriq) *fakeCatalog {
	return &fakeCatalog{f: f, kind: "fabriq", on: map[string]bool{"falkordb": true}, roles: roles}
}

func (c *fakeCatalog) allow(p security.Principal, need security.Permission) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.OrgID == "org" && slices.Contains(c.roles[p.UserID], need) {
		return nil
	}
	return security.ErrForbidden
}

func (c *fakeCatalog) Components(_ context.Context, p security.Principal, need security.Permission, ref deployments.ResourceRef) ([]*planning.Binding, error) {
	if err := c.allow(p, need); err != nil {
		return nil, err
	}
	if ref != fabriqRef {
		return nil, deployments.ErrComponentNotFound
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	deps := map[string]planning.DependencyChoice{"postgres": {Mode: planning.DependencyNew}, "redis": {Mode: planning.DependencyNew}}
	for _, dep := range []string{"falkordb", "elasticsearch"} {
		mode := planning.DependencyOff
		if c.on[dep] {
			mode = planning.DependencyNew
		}
		deps[dep] = planning.DependencyChoice{Mode: mode}
	}
	parent := binding("fabriq", planning.ResourceConfig{Provider: "server", Kind: c.kind,
		Composition: &planning.CompositionConfig{Kind: c.kind, Version: "1.9", Revision: "1", Dependencies: deps}})
	parts := []*planning.Binding{parent}
	components := []string{"admin", "server"}
	if c.on["falkordb"] {
		components = append(components, "rebuild-graph")
	}
	if c.on["elasticsearch"] {
		components = append(components, "rebuild-search")
	}
	for _, name := range components {
		role := recipes.RoleProcess
		if strings.HasPrefix(name, "rebuild-") {
			role = recipes.RoleJob
		}
		parts = append(parts, binding("fabriq--"+name, planning.ResourceConfig{Provider: "server", Kind: c.kind, Component: &planning.ComponentConfig{
			Kind: c.kind, Resource: "fabriq", Revision: "1", Component: recipes.ResolvedComponent{Component: recipes.Component{Name: name, Role: role}},
		}}))
	}
	return parts, nil
}

func binding(name string, cfg planning.ResourceConfig) *planning.Binding {
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return &planning.Binding{ID: "bind-" + name, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: name, Provider: "server", Config: raw}
}

func (c *fakeCatalog) Channel(_ context.Context, p security.Principal, need security.Permission, ref deployments.ComponentRef) (*deployments.ComponentChannel, error) {
	if err := c.allow(p, need); err != nil {
		return nil, err
	}
	if c.channelErr != nil {
		return nil, c.channelErr
	}
	if ref.ResourceRef != fabriqRef || ref.Component != "admin" || ref.Port != "http" {
		return nil, errors.New("only the console's http port reaches the admin API")
	}
	return c.f.channel(), nil
}

func (c *fakeCatalog) RunJob(_ context.Context, orgID string, ref deployments.ResourceRef, component, name string) (owned.JobResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if orgID != "org" {
		return owned.JobResult{}, errors.New("another organization")
	}
	c.jobs = append(c.jobs, jobCall{Component: component, Name: name, Ref: ref})
	return c.result, c.jobErr
}

func (c *fakeCatalog) ran() []jobCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.jobs)
}

// fakeManifests serves one project's current manifest.
type fakeManifests struct{ m *manifest.Manifest }

func (f fakeManifests) Latest(_ context.Context, orgID, projectID string) (*manifests.Revision, error) {
	if f.m == nil || orgID != "org" || projectID != "proj" {
		return nil, manifests.ErrNotFound
	}
	return &manifests.Revision{OrgID: orgID, ProjectID: projectID, Revision: 1, Manifest: f.m}, nil
}

// needing is a manifest whose services need fabriq with the default
// tenants given.
func needing(tenants map[string]string) *manifest.Manifest {
	m := &manifest.Manifest{Version: 1, Services: map[string]manifest.Service{}, Resources: map[string]manifest.Resource{"fabriq": {Kind: "fabriq"}}}
	for service, tenant := range tenants {
		m.Services[service] = manifest.Service{Source: service, Needs: []string{"fabriq"}, NeedOptions: map[string]map[string]string{"fabriq": {"tenant": tenant}}}
	}
	if _, ok := m.Services["web"]; !ok {
		m.Services["web"] = manifest.Service{Source: "web"}
	}
	return m
}

type serviceHarness struct {
	*harness
	catalog  *fakeCatalog
	rebuilds *memRebuilds
	enqueued []string
	svc      *fabriqloom.Service
}

func newServiceHarness(m *manifest.Manifest, tenants ...string) *serviceHarness {
	h := &serviceHarness{harness: newHarness(tenants...), rebuilds: &memRebuilds{}}
	h.catalog = newFakeCatalog(h.f)
	h.svc = &fabriqloom.Service{
		Access: h.catalog, Keys: h.keys, Secrets: h.secrets, RebuildStore: h.rebuilds, Manifests: fakeManifests{m},
		Enqueue: func(_ context.Context, orgID, id string) error {
			h.enqueued = append(h.enqueued, orgID+"/"+id)
			return nil
		},
		Now: func() time.Time { return harnessNow },
	}
	return h
}

func TestTenantsListAndCreate(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	ctx := t.Context()
	tenant, created, err := h.svc.CreateTenant(ctx, as("developer"), fabriqRef, "umbrella")
	if err != nil || !created || tenant.ID != "umbrella" {
		t.Fatalf("create: %+v %v %v", tenant, created, err)
	}
	tenant, created, err = h.svc.CreateTenant(ctx, as("developer"), fabriqRef, "umbrella")
	if err != nil || created || tenant.ID != "umbrella" {
		t.Fatalf("again: %+v %v %v", tenant, created, err)
	}
	_, _, err = h.svc.CreateTenant(ctx, as("developer"), fabriqRef, "Umbrella")
	if !errors.Is(err, fabriqloom.ErrInvalid) || err.Error() != "Use lowercase letters, digits and dashes." {
		t.Fatalf("Umbrella: %v", err)
	}
	if n := h.f.count(http.MethodPost, "/admin/tenants"); n != 2 {
		t.Fatalf("POST /admin/tenants %d times", n)
	}
	if _, _, err = h.svc.CreateTenant(ctx, as("viewer"), fabriqRef, "globex"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	tenants, err := h.svc.Tenants(ctx, as("viewer"), fabriqRef)
	if err != nil || len(tenants) != 2 || tenants[0].ID != "acme" || tenants[1].ID != "umbrella" {
		t.Fatalf("tenants: %+v %v", tenants, err)
	}
	// Another resource, or a composition of another kind, isn't a Fabriq.
	if _, err = h.svc.Tenants(ctx, as("viewer"), deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "db"}); !errors.Is(err, deployments.ErrComponentNotFound) {
		t.Fatalf("another resource: %v", err)
	}
	h.catalog.kind = "octopus"
	if _, err = h.svc.Tenants(ctx, as("viewer"), fabriqRef); !errors.Is(err, fabriqloom.ErrNotFound) {
		t.Fatalf("octopus: %v", err)
	}
	h.catalog.kind = "fabriq-library"
	if _, err = h.svc.Tenants(ctx, as("viewer"), fabriqRef); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("fabriq-library: %v", err)
	}
}

func TestTenantsUnavailableSaysWhy(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	h.catalog.channelErr = errors.New("dial lp-x:8081: connection refused")
	_, err := h.svc.Tenants(t.Context(), as("viewer"), fabriqRef)
	if !errors.Is(err, fabriqloom.ErrUnavailable) || err.Error() != "Fabriq isn't reachable right now: dial lp-x:8081: connection refused" {
		t.Fatalf("channel: %v", err)
	}
	h.catalog.channelErr = nil
	for _, status := range []int{http.StatusUnauthorized, http.StatusUpgradeRequired, http.StatusBadGateway, http.StatusInternalServerError} {
		h.f.refuse = status
		if _, err = h.svc.Consumers(t.Context(), as("viewer"), fabriqRef); !errors.Is(err, fabriqloom.ErrUnavailable) {
			t.Fatalf("%d: %v", status, err)
		}
		noSecret(t, h.f, err)
	}
	// A permission problem stays one.
	if _, err = h.svc.Tenants(t.Context(), security.Principal{OrgID: "org", UserID: "stranger"}, fabriqRef); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("stranger: %v", err)
	}
}

func TestConsumersJoinManifestRecordsAndFabriq(t *testing.T) {
	h := newServiceHarness(needing(map[string]string{"api": "acme", "jobs": "globex"}), "acme")
	h.f.applied = []string{"202610210001", "202610210035"}
	apiKey := idOf(t, h.f, h.bind(t, "api", "acme"))
	used := time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC)
	h.f.use(apiKey, used)
	// billing needed fabriq once; its key was revoked when it stopped.
	billing := idOf(t, h.f, h.bind(t, "billing", "acme"))
	if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("billing", "acme")); err != nil {
		t.Fatal(err)
	}
	keys, err := h.svc.Consumers(t.Context(), as("viewer"), fabriqRef)
	if err != nil {
		t.Fatal(err)
	}
	byService := map[string]fabriqloom.ConsumerKey{}
	order := make([]string, 0, len(keys))
	for _, k := range keys {
		byService[k.Service] = k
		order = append(order, k.Service)
	}
	if !slices.Equal(order, []string{"api", "billing", "jobs"}) {
		t.Fatalf("services: %v", order)
	}
	api := byService["api"]
	if api.State != "active" || api.Tenant != "acme" || api.Prefix != h.f.key(apiKey).Prefix || api.LastUsedAt == nil || !api.LastUsedAt.Equal(used) || !api.LastUsedKnown || api.CreatedAt == nil {
		t.Fatalf("api: %+v", api)
	}
	if jobs := byService["jobs"]; jobs.State != "unreleased" || jobs.Tenant != "globex" || jobs.Prefix != "" || jobs.CreatedAt != nil {
		t.Fatalf("jobs: %+v", jobs)
	}
	if b := byService["billing"]; b.State != "revoked" || b.RevokedAt == nil || b.Prefix != h.f.key(billing).Prefix {
		t.Fatalf("billing: %+v", b)
	}
	// Without Plan 2a's last_used_at migration, Fabriq can't say.
	h.f.applied = []string{"202610210001"}
	keys, err = h.svc.Consumers(t.Context(), as("viewer"), fabriqRef)
	if err != nil || keys[0].LastUsedKnown {
		t.Fatalf("without last_used_at: %+v %v", keys, err)
	}
}

func TestRotateIssuesPendingOnce(t *testing.T) {
	h := newServiceHarness(needing(map[string]string{"api": "acme"}), "acme")
	active := idOf(t, h.f, h.bind(t, "api", "acme"))
	before := h.f.count(http.MethodPost, "/admin/keys")
	if _, err := h.svc.Rotate(t.Context(), as("developer"), fabriqRef, "api"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer: %v", err)
	}
	first, err := h.svc.Rotate(t.Context(), as("admin"), fabriqRef, "api")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.Rotate(t.Context(), as("admin"), fabriqRef, "api")
	if err != nil {
		t.Fatal(err)
	}
	if n := h.f.count(http.MethodPost, "/admin/keys") - before; n != 1 {
		t.Fatalf("POST /admin/keys %d times", n)
	}
	pending := h.keys.inState("api", fabriqloom.KeyPending)
	if len(pending) != 1 || pending[0].Tenant != "acme" || !h.secrets.has(pending[0].ID) {
		t.Fatalf("pending: %+v", pending)
	}
	if first.State != "rotating" || second.State != "rotating" || first.Prefix != pending[0].Prefix || second.Prefix != first.Prefix {
		t.Fatalf("rotations: %+v %+v", first, second)
	}
	// The running release keeps its key until the next one.
	if got := h.keys.inState("api", fabriqloom.KeyActive); len(got) != 1 || got[0].ID != active || h.f.key(active).RevokedAt != nil {
		t.Fatalf("active: %+v", got)
	}
	// The next release promotes it.
	dsn := h.bind(t, "api", "acme")
	if idOf(t, h.f, dsn) != pending[0].ID {
		t.Fatal("the next release didn't get the rotated key")
	}
	if _, err := h.svc.Rotate(t.Context(), as("admin"), fabriqRef, "jobs"); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("a service without a key: %v", err)
	}
}

func TestRevokeCutsAccessNow(t *testing.T) {
	h := newServiceHarness(needing(map[string]string{"api": "acme", "web": "acme"}), "acme")
	api := idOf(t, h.f, h.bind(t, "api", "acme"))
	web := idOf(t, h.f, h.bind(t, "web", "acme"))
	if _, err := h.svc.Rotate(t.Context(), as("admin"), fabriqRef, "api"); err != nil {
		t.Fatal(err)
	}
	pending := h.keys.inState("api", fabriqloom.KeyPending)[0].ID
	if _, err := h.svc.Revoke(t.Context(), as("developer"), fabriqRef, "api"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer: %v", err)
	}
	got, err := h.svc.Revoke(t.Context(), as("admin"), fabriqRef, "api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "revoked" || got.RevokedAt == nil {
		t.Fatalf("revoked: %+v", got)
	}
	for _, id := range []string{api, pending} {
		if h.f.key(id).RevokedAt == nil {
			t.Fatalf("%s still opens Fabriq", id)
		}
		if h.secrets.has(id) {
			t.Fatalf("%s is still in Vault", id)
		}
	}
	if n := len(h.keys.inState("api", fabriqloom.KeyRevoked)); n != 2 {
		t.Fatalf("%d revoked records", n)
	}
	if h.f.key(web).RevokedAt != nil {
		t.Fatal("web lost its key too")
	}
	// Its need still stands: the next release issues a new one.
	if dsn := h.bind(t, "api", "acme"); idOf(t, h.f, dsn) == api || idOf(t, h.f, dsn) == pending {
		t.Fatal("the next release got a revoked key")
	}
}

func TestRevokeAlsoCutsUnrecordedKeys(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	h.bind(t, "api", "acme")
	h.f.plantAt("loomplane:api", harnessNow)
	if _, err := h.svc.Revoke(t.Context(), as("admin"), fabriqRef, "api"); err != nil {
		t.Fatal(err)
	}
	if got := h.f.valid("loomplane:api"); len(got) != 0 {
		t.Fatalf("unrecorded keys survived revoke: %v", got)
	}
	// A retry still reconciles when every recorded key is already revoked.
	h.f.plantAt("loomplane:api", harnessNow)
	if _, err := h.svc.Revoke(t.Context(), as("admin"), fabriqRef, "api"); err != nil {
		t.Fatal(err)
	}
	if got := h.f.valid("loomplane:api"); len(got) != 0 {
		t.Fatalf("keys survived revoke retry: %v", got)
	}
}

func TestBindWaitsForRotationToRecordItsKey(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	h.bind(t, "api", "acme")
	issued, resume := make(chan struct{}), make(chan struct{})
	h.keys.beforeInsert = func(*fabriqloom.KeyRecord) { close(issued); <-resume }
	waiting := make(chan string, 1)
	h.keys.waiting = waiting
	rotated := make(chan error, 1)
	go func() { _, err := h.svc.Rotate(t.Context(), as("admin"), fabriqRef, "api"); rotated <- err }()
	<-issued
	bound := make(chan error, 1)
	go func() { _, err := h.hooks.BindConsumer(t.Context(), h.consumer("api", "acme")); bound <- err }()
	// The bind found api's lock held by the rotation, which can't let go
	// until it has recorded its key.
	if service := <-waiting; service != "api" {
		t.Fatalf("%s waited", service)
	}
	select {
	case err := <-bound:
		t.Fatalf("bind ran before rotation recorded its key: %v", err)
	default:
	}
	close(resume)
	if err := <-rotated; err != nil {
		t.Fatal(err)
	}
	if err := <-bound; err != nil {
		t.Fatal(err)
	}
	active := h.keys.inState("api", fabriqloom.KeyActive)
	if len(active) != 1 || h.f.key(active[0].ID).RevokedAt != nil {
		t.Fatal("rotation's key was lost")
	}
}

func TestStatusCountsEntityTypes(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	h.f.types = []string{"a", "b", "c", "d", "e", "f"}
	st, err := h.svc.Status(t.Context(), as("viewer"), fabriqRef)
	if err != nil || st.EntityTypes != 6 || !st.Reachable || st.Kind != "fabriq" || st.Version != "1.9" {
		t.Fatalf("status: %+v %v", st, err)
	}
	var typesTenant string
	for _, r := range h.f.seen() {
		if r.Path == "/admin/entities/types" {
			typesTenant = r.Header.Get("X-Tenant-ID")
		}
	}
	if typesTenant != "acme" {
		t.Fatalf("X-Tenant-ID: %q", typesTenant)
	}

	empty := newServiceHarness(nil)
	empty.f.types = []string{"a"}
	st, err = empty.svc.Status(t.Context(), as("viewer"), fabriqRef)
	if err != nil || st.EntityTypes != 0 || !st.Reachable {
		t.Fatalf("no tenants: %+v %v", st, err)
	}
	if n := empty.f.count(http.MethodGet, "/admin/entities/types"); n != 0 {
		t.Fatalf("GET /admin/entities/types %d times", n)
	}

	// Down or starting, Status still answers, and says why.
	h.catalog.channelErr = errors.New("dial lp-x:8080: connection refused")
	st, err = h.svc.Status(t.Context(), as("viewer"), fabriqRef)
	if err != nil || st.Reachable || st.Reason != "dial lp-x:8080: connection refused" || st.Kind != "fabriq" || st.EntityTypes != 0 {
		t.Fatalf("down: %+v %v", st, err)
	}
	h.catalog.channelErr = nil
	h.f.refuse = http.StatusBadGateway
	if st, err = h.svc.Status(t.Context(), as("viewer"), fabriqRef); err != nil || st.Reachable || !strings.Contains(st.Reason, "isn't answering") {
		t.Fatalf("502: %+v %v", st, err)
	}
	if _, err = h.svc.Status(t.Context(), security.Principal{OrgID: "org", UserID: "stranger"}, fabriqRef); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("stranger: %v", err)
	}
}

func TestStatusReadsMigrations(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	applied := make([]string, 0, 35)
	for i := 1; i <= 34; i++ {
		applied = append(applied, "20261021"+strings.Repeat("0", 2)+twoDigits(i))
	}
	h.f.applied = append(slices.Clone(applied), "202610210035")
	st, err := h.svc.Status(t.Context(), as("viewer"), fabriqRef)
	if err != nil || st.Migrations != 35 || !st.MigrationsKnown || !st.KeysReportUse {
		t.Fatalf("35 applied: %+v %v", st, err)
	}
	var migrationsTenant string
	for _, r := range h.f.seen() {
		if r.Path == "/admin/migrations" {
			migrationsTenant = r.Header.Get("X-Tenant-ID")
		}
	}
	if migrationsTenant != "acme" {
		t.Fatalf("X-Tenant-ID: %q", migrationsTenant)
	}
	h.f.applied = applied
	if st, err = h.svc.Status(t.Context(), as("viewer"), fabriqRef); err != nil || st.Migrations != 34 || st.KeysReportUse {
		t.Fatalf("34 applied: %+v %v", st, err)
	}
	for _, status := range []int{http.StatusForbidden, http.StatusNotImplemented} {
		h.f.noMigrations = status
		st, err = h.svc.Status(t.Context(), as("viewer"), fabriqRef)
		if err != nil || !st.Reachable || st.MigrationsKnown || st.Migrations != 0 || st.KeysReportUse {
			t.Fatalf("%d: %+v %v", status, st, err)
		}
	}
}

func twoDigits(i int) string { return string(rune('0'+i/10)) + string(rune('0'+i%10)) }

func TestLibraryStatusHasNoTenants(t *testing.T) {
	h := newServiceHarness(nil)
	h.catalog.kind = "fabriq-library"
	h.catalog.channelErr = errors.New("must not open admin channel")
	status, err := h.svc.Status(t.Context(), as("viewer"), fabriqRef)
	if err != nil || status.Kind != "fabriq-library" {
		t.Fatalf("status %+v: %v", status, err)
	}
	if _, err = h.svc.Tenants(t.Context(), as("viewer"), fabriqRef); !errors.Is(err, fabriqloom.ErrInvalid) || err.Error() != "fabriq-library has no tenants or keys" {
		t.Fatalf("tenants: %v", err)
	}
	if _, err = h.svc.Consumers(t.Context(), as("viewer"), fabriqRef); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("consumers: %v", err)
	}
	if _, _, err = h.svc.CreateTenant(t.Context(), as("developer"), fabriqRef, "acme"); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("create: %v", err)
	}
	if _, err = h.svc.Rotate(t.Context(), as("admin"), fabriqRef, "web"); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("rotate: %v", err)
	}
	if _, err = h.svc.Revoke(t.Context(), as("admin"), fabriqRef, "web"); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("revoke: %v", err)
	}
}
