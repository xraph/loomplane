package fabriqloom_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/recipes"
)

// memKeys is a KeyStore in memory, with the table's unique indexes and a
// lock per service, as PgStore's leases are.
type memKeys struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
	rows  []fabriqloom.KeyRecord
	// beforeInsert runs before each Insert, outside the lock.
	beforeInsert func(r *fabriqloom.KeyRecord)
	// waiting, when set, hears the service of each LockService that finds
	// the lock held, before it waits.
	waiting chan string
	// lose, when set, ends each lease it gives at once with cause
	// ErrLeaseLost, as PgStore's does when a renewal finds it gone.
	lose bool
}

func (s *memKeys) LockService(ctx context.Context, orgID, environmentID, resource, service string) (context.Context, func(), error) {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	key := strings.Join([]string{orgID, environmentID, resource, service}, "\x00")
	lock, ok := s.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[key] = lock
	}
	waiting := s.waiting
	s.mu.Unlock()
	if !lock.TryLock() {
		if waiting != nil {
			waiting <- service
		}
		lock.Lock()
	}
	held, cancel := context.WithCancelCause(ctx)
	if s.lose {
		cancel(fabriqloom.ErrLeaseLost)
	}
	return held, func() { cancel(nil); lock.Unlock() }, nil
}

func (s *memKeys) Insert(_ context.Context, r *fabriqloom.KeyRecord) error {
	if hook := s.beforeInsert; hook != nil {
		s.beforeInsert = nil
		hook(r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.rows {
		if x.ID == r.ID {
			return errors.New("duplicate id")
		}
		if (r.State == fabriqloom.KeyActive || r.State == fabriqloom.KeyPending) && x.State == r.State &&
			x.OrgID == r.OrgID && x.EnvironmentID == r.EnvironmentID && x.Resource == r.Resource && x.Service == r.Service {
			return fabriqloom.ErrConflict
		}
	}
	s.rows = append(s.rows, *r)
	return nil
}

func (s *memKeys) SetState(_ context.Context, orgID, id string, from []fabriqloom.KeyState, to fabriqloom.KeyState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.rows {
		if x.OrgID != orgID || x.ID != id {
			continue
		}
		if !slices.Contains(from, x.State) {
			return fabriqloom.ErrStateChanged
		}
		if to == fabriqloom.KeyActive || to == fabriqloom.KeyPending {
			for _, y := range s.rows {
				if y.ID != id && y.State == to && y.OrgID == x.OrgID && y.EnvironmentID == x.EnvironmentID && y.Resource == x.Resource && y.Service == x.Service {
					return fabriqloom.ErrConflict
				}
			}
		}
		now := time.Now().UTC()
		s.rows[i].State, s.rows[i].UpdatedAt = to, now
		if to == fabriqloom.KeyRevoked {
			s.rows[i].RevokedAt = &now
		}
		return nil
	}
	return fabriqloom.ErrStateChanged
}

func (s *memKeys) ForService(_ context.Context, orgID, environmentID, resource, service string) ([]*fabriqloom.KeyRecord, error) {
	return s.list(func(r fabriqloom.KeyRecord) bool {
		return r.OrgID == orgID && r.EnvironmentID == environmentID && r.Resource == resource && r.Service == service
	}), nil
}

func (s *memKeys) ForResource(_ context.Context, orgID, environmentID, resource string) ([]*fabriqloom.KeyRecord, error) {
	return s.list(func(r fabriqloom.KeyRecord) bool {
		return r.OrgID == orgID && r.EnvironmentID == environmentID && r.Resource == resource
	}), nil
}

func (s *memKeys) list(keep func(fabriqloom.KeyRecord) bool) []*fabriqloom.KeyRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*fabriqloom.KeyRecord
	for i := len(s.rows) - 1; i >= 0; i-- {
		if keep(s.rows[i]) {
			r := s.rows[i]
			out = append(out, &r)
		}
	}
	return out
}

func (s *memKeys) inState(service string, state fabriqloom.KeyState) []fabriqloom.KeyRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fabriqloom.KeyRecord
	for _, r := range s.rows {
		if r.Service == service && r.State == state {
			out = append(out, r)
		}
	}
	return out
}

// memSecrets is ResourceSecrets in memory. Setting failDelete fails every
// Delete with it.
type memSecrets struct {
	mu         sync.Mutex
	m          map[string][]byte
	failDelete error
}

func newMemSecrets() *memSecrets { return &memSecrets{m: map[string][]byte{}} }

func (s *memSecrets) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = slices.Clone(value)
	return nil
}

func (s *memSecrets) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, deployments.ErrNoCredential
	}
	return slices.Clone(v), nil
}

func (s *memSecrets) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete != nil {
		return s.failDelete
	}
	delete(s.m, key)
	return nil
}

func (s *memSecrets) has(id string) bool {
	_, err := s.Get(context.Background(), vaultKey(id))
	return err == nil
}

func vaultKey(id string) string {
	return deployments.ResourceKey("org", "env", "fabriq", "fabriq-key:"+id)
}

// fakeAccess opens channels to the fake for Loomplane's own background
// work, or fails with err.
type fakeAccess struct {
	f            *fakeFabriq
	err          error
	mu           sync.Mutex
	opened       []deployments.ComponentRef
	controlToken []byte
	secretErr    error
}

func (a *fakeAccess) InternalChannel(_ context.Context, orgID string, ref deployments.ComponentRef) (*deployments.ComponentChannel, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opened = append(a.opened, ref)
	if a.err != nil {
		return nil, a.err
	}
	if orgID != "org" || ref.Component != "admin" || ref.Port != "http" || ref.Resource != "fabriq" {
		return nil, errors.New("not the fabriq console")
	}
	return a.f.channel(), nil
}

func (a *fakeAccess) InternalSecret(_ context.Context, orgID string, ref deployments.ResourceRef, name string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if orgID != "org" || ref != fabriqRef || name != fixtureControlSecret {
		return nil, deployments.ErrComponentNotFound
	}
	return slices.Clone(a.controlToken), a.secretErr
}

func (a *fakeAccess) opens() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.opened)
}

// harnessNow is the harness's clock: the day after the fake's keys were
// issued.
var harnessNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

type harness struct {
	f       *fakeFabriq
	keys    *memKeys
	secrets *memSecrets
	access  *fakeAccess
	hooks   *fabriqloom.Hooks
	warned  []string
}

func newHarness(tenants ...string) *harness {
	h := &harness{f: newFakeFabriq(tenants...), keys: &memKeys{}, secrets: newMemSecrets()}
	h.access = &fakeAccess{f: h.f, controlToken: []byte(fixtureControlToken)}
	h.hooks = fabriqloom.NewHooks(h.deps())
	return h
}

func (h *harness) deps() fabriqloom.HookDeps {
	return fabriqloom.HookDeps{Keys: h.keys, Secrets: h.secrets, Access: h.access, Now: func() time.Time { return harnessNow }, Warn: func(msg string, err error) {
		h.warned = append(h.warned, msg+": "+err.Error())
	}}
}

// consumer is service needing the "fabriq" resource with tenant as its
// default.
func (h *harness) consumer(service, tenant string) deployments.Consumer {
	var options map[string]string
	if tenant != "" {
		options = map[string]string{"tenant": tenant}
	}
	return deployments.Consumer{
		OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production",
		Resource: "fabriq", Kind: "fabriq", Output: "url", Service: service, Options: options,
		Channel: func(_ context.Context, component, port string) (*deployments.ComponentChannel, error) {
			if component != "admin" || port != "http" {
				return nil, errors.New("only the console's http port reaches the admin API")
			}
			return h.f.channel(), nil
		},
		Address: func(component, port string) (*url.URL, error) {
			if component != "server" || port != "api" {
				return nil, errors.New("no such port")
			}
			return url.Parse("http://lp-a:8081")
		},
	}
}

func (h *harness) bind(t *testing.T, service, tenant string) string {
	t.Helper()
	dsn, err := h.hooks.BindConsumer(t.Context(), h.consumer(service, tenant))
	noSecret(t, h.f, err)
	if err != nil {
		t.Fatalf("bind %s: %v", service, err)
	}
	return dsn
}

// keyOf is the key in a DSN.
func keyOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Username()
}

func idOf(t *testing.T, f *fakeFabriq, dsn string) string {
	t.Helper()
	key := keyOf(t, dsn)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.Key == key {
			return k.ID
		}
	}
	t.Fatal("the DSN's key isn't one Fabriq issued")
	return ""
}

func TestBindConsumerIssuesOncePerService(t *testing.T) {
	h := newHarness("acme")
	first := h.bind(t, "api", "acme")
	second := h.bind(t, "api", "acme")
	if strings.Contains(first, fixtureControlToken) || strings.Contains(second, fixtureControlToken) {
		t.Fatal("consumer DSN included the console credential")
	}
	if first != second {
		t.Fatal("two binds gave api two DSNs")
	}
	if !strings.HasPrefix(first, "fabriq://fq_") || !strings.HasSuffix(first, "@lp-a:8081/acme?tls=false") {
		t.Fatal("api's DSN isn't fabriq://<key>@lp-a:8081/acme?tls=false")
	}
	if n := h.f.count(http.MethodPost, "/admin/keys"); n != 1 {
		t.Fatalf("POST /admin/keys %d times", n)
	}
	web := h.bind(t, "web", "acme")
	if web == first {
		t.Fatal("web got api's key")
	}
	if n := h.f.count(http.MethodPost, "/admin/keys"); n != 2 {
		t.Fatalf("POST /admin/keys %d times", n)
	}
	for _, service := range []string{"api", "web"} {
		active := h.keys.inState(service, fabriqloom.KeyActive)
		if len(active) != 1 || active[0].Tenant != "acme" || active[0].Prefix == "" || active[0].ProjectID != "proj" {
			t.Fatalf("%s's records: %+v", service, active)
		}
		if !h.secrets.has(active[0].ID) {
			t.Fatalf("%s's key isn't in Vault", service)
		}
		if got := h.f.valid("loomplane:" + service); len(got) != 1 || got[0] != active[0].ID {
			t.Fatalf("%s's keys in Fabriq: %v", service, got)
		}
	}
	if k := h.f.key(h.keys.inState("api", fabriqloom.KeyActive)[0].ID); k.CanManageKeys || k.TenantID != "" {
		t.Fatalf("api's key %s: can manage keys %v, tenant %q", k.ID, k.CanManageKeys, k.TenantID)
	}
}

func TestBindConsumerCreatesMissingTenant(t *testing.T) {
	h := newHarness("acme")
	dsn := h.bind(t, "api", "globex")
	if !strings.HasSuffix(dsn, "/globex?tls=false") {
		t.Fatal("the DSN doesn't name globex")
	}
	if _, ok := h.f.tenants["globex"]; !ok {
		t.Fatal("globex wasn't created")
	}
	// Known now: the POST answers 200 and nothing changes.
	if again := h.bind(t, "api", "globex"); again != dsn {
		t.Fatal("a second bind changed the DSN")
	}
	if n := h.f.count(http.MethodPost, "/admin/tenants"); n != 2 {
		t.Fatalf("POST /admin/tenants %d times", n)
	}
}

func TestBindConsumerTenantChangeRetiresOldKey(t *testing.T) {
	h := newHarness("acme")
	old := h.bind(t, "api", "acme")
	oldID := idOf(t, h.f, old)
	next := h.bind(t, "api", "globex")
	if next == old || !strings.HasSuffix(next, "/globex?tls=false") {
		t.Fatal("a tenant change kept the old DSN")
	}
	nextID := idOf(t, h.f, next)
	active, retiring := h.keys.inState("api", fabriqloom.KeyActive), h.keys.inState("api", fabriqloom.KeyRetiring)
	if len(active) != 1 || active[0].ID != nextID || active[0].Tenant != "globex" {
		t.Fatalf("active: %+v", active)
	}
	if len(retiring) != 1 || retiring[0].ID != oldID {
		t.Fatalf("retiring: %+v", retiring)
	}
	// The running service still holds the old key until the release that
	// carries the new one writes its binding.
	if got := h.f.valid("loomplane:api"); !slices.Equal(got, []string{oldID, nextID}) {
		t.Fatalf("valid keys before the release: %v", got)
	}
	// Binding again for the same tenant changes nothing.
	if again := h.bind(t, "api", "globex"); again != next {
		t.Fatal("a third bind changed the DSN")
	}

	observer := fabriqloom.NewObserver(h.deps())
	if err := observer.Released(t.Context(), release(t, "api", "fabriq")); err != nil {
		t.Fatal(err)
	}
	if got := h.f.valid("loomplane:api"); !slices.Equal(got, []string{nextID}) {
		t.Fatalf("valid keys after the release: %v", got)
	}
	if got := h.keys.inState("api", fabriqloom.KeyRevoked); len(got) != 1 || got[0].ID != oldID || got[0].RevokedAt == nil {
		t.Fatalf("revoked: %+v", got)
	}
	if h.secrets.has(oldID) || !h.secrets.has(nextID) {
		t.Fatal("Vault holds the wrong keys")
	}
	if got := h.keys.inState("api", fabriqloom.KeyActive); len(got) != 1 {
		t.Fatalf("active: %+v", got)
	}
}

func TestBindConsumerPromotesPendingKey(t *testing.T) {
	h := newHarness("acme")
	old := h.bind(t, "api", "acme")
	oldID := idOf(t, h.f, old)
	// What Rotate leaves: a key issued with the active record's tenant,
	// kept pending.
	admin := fixtureAdmin(t, h.f)
	issued, err := admin.IssueKey(t.Context(), "loomplane:api")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.secrets.Put(t.Context(), vaultKey(issued.ID), []byte(issued.Key)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := h.keys.Insert(t.Context(), &fabriqloom.KeyRecord{ID: issued.ID, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq", Service: "api",
		Tenant: "acme", Prefix: issued.Prefix, State: fabriqloom.KeyPending, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	dsn := h.bind(t, "api", "acme")
	if keyOf(t, dsn) != issued.Key {
		t.Fatal("the pending key wasn't released")
	}
	if got := h.keys.inState("api", fabriqloom.KeyActive); len(got) != 1 || got[0].ID != issued.ID {
		t.Fatalf("active: %+v", got)
	}
	if got := h.keys.inState("api", fabriqloom.KeyRetiring); len(got) != 1 || got[0].ID != oldID {
		t.Fatalf("retiring: %+v", got)
	}
	if n := h.f.count(http.MethodPost, "/admin/keys"); n != 2 {
		t.Fatalf("POST /admin/keys %d times", n)
	}
}

func TestBindConsumerRevokesOrphanedKeys(t *testing.T) {
	h := newHarness("acme")
	// Fabriq issued this one and Loomplane kept it in Vault, then crashed
	// before recording it.
	orphan := h.f.plant("loomplane:api")
	if err := h.secrets.Put(t.Context(), vaultKey(orphan.ID), []byte(orphan.Key)); err != nil {
		t.Fatal(err)
	}
	other := h.f.plant("session")
	webs := h.f.plant("loomplane:web")
	dsn := h.bind(t, "api", "acme")
	if keyOf(t, dsn) == orphan.Key {
		t.Fatal("the orphan was released")
	}
	if k := h.f.key(orphan.ID); k.RevokedAt == nil {
		t.Fatal("the orphan is still valid")
	}
	if h.secrets.has(orphan.ID) {
		t.Fatal("the orphan is still in Vault")
	}
	// The orphan went before the new key was issued.
	var order []string
	for _, r := range h.f.seen() {
		if r.Method == http.MethodDelete || (r.Method == http.MethodPost && r.Path == "/admin/keys") {
			order = append(order, r.Method)
		}
	}
	if !slices.Equal(order, []string{http.MethodDelete, http.MethodPost}) {
		t.Fatalf("order: %v", order)
	}
	if h.f.key(other.ID).RevokedAt != nil || h.f.key(webs.ID).RevokedAt != nil {
		t.Fatal("a key with another label was revoked")
	}
	if got := h.keys.inState("api", fabriqloom.KeyActive); len(got) != 1 {
		t.Fatalf("active: %+v", got)
	}
	if got := h.f.valid("loomplane:api"); len(got) != 1 || got[0] != idOf(t, h.f, dsn) {
		t.Fatalf("valid: %v", got)
	}
}

func TestBindConsumerReconcilesFreshOrphansEvenWithAnActiveKey(t *testing.T) {
	h := newHarness("acme")
	orphan := h.f.plantAt("loomplane:api", harnessNow)
	dsn := h.bind(t, "api", "acme")
	if h.f.key(orphan.ID).RevokedAt == nil {
		t.Fatal("immediate retry left the fresh orphan valid")
	}
	later := h.f.plantAt("loomplane:api", harnessNow)
	if got := h.bind(t, "api", "acme"); got != dsn {
		t.Fatal("reconciliation replaced the recorded active key")
	}
	if h.f.key(later.ID).RevokedAt == nil {
		t.Fatal("release with an active key skipped orphan cleanup")
	}
	deps := h.deps()
	deps.Now = func() time.Time { return harnessNow.Add(20 * time.Minute) }
	h.hooks = fabriqloom.NewHooks(deps)
	if got := h.bind(t, "api", "acme"); got != dsn {
		t.Fatal("later release replaced the active key")
	}
	if got := h.f.valid("loomplane:api"); len(got) != 1 {
		t.Fatalf("valid credentials: %v", got)
	}
	if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("api", "acme")); err != nil {
		t.Fatal(err)
	}
	if got := h.f.valid("loomplane:api"); len(got) != 0 {
		t.Fatalf("revocation left keys: %v", got)
	}
}

func TestBindConsumerConflictKeepsTheWinner(t *testing.T) {
	h := newHarness("acme")
	var winner string
	h.keys.beforeInsert = func(r *fabriqloom.KeyRecord) {
		// Another release of api records its key first.
		admin := fixtureAdmin(t, h.f)
		issued, err := admin.IssueKey(context.Background(), "loomplane:api")
		if err != nil {
			t.Error(err)
			return
		}
		winner = issued.Key
		_ = h.secrets.Put(context.Background(), vaultKey(issued.ID), []byte(issued.Key))
		w := *r
		w.ID, w.Prefix = issued.ID, issued.Prefix
		if err := h.keys.Insert(context.Background(), &w); err != nil {
			t.Error(err)
		}
	}
	dsn := h.bind(t, "api", "acme")
	if keyOf(t, dsn) != winner {
		t.Fatal("the losing key was released")
	}
	if got := h.f.valid("loomplane:api"); len(got) != 1 || got[0] != idOf(t, h.f, dsn) {
		t.Fatalf("valid: %v", got)
	}
	if got := h.keys.inState("api", fabriqloom.KeyActive); len(got) != 1 {
		t.Fatalf("active: %+v", got)
	}
}

func TestBindConsumerReissuesWhenVaultLostTheKey(t *testing.T) {
	h := newHarness("acme")
	old := h.bind(t, "api", "acme")
	oldID := idOf(t, h.f, old)
	if err := h.secrets.Delete(t.Context(), vaultKey(oldID)); err != nil {
		t.Fatal(err)
	}
	next := h.bind(t, "api", "acme")
	if next == old {
		t.Fatal("a key Vault doesn't hold was released")
	}
	if got := h.keys.inState("api", fabriqloom.KeyRetiring); len(got) != 1 || got[0].ID != oldID {
		t.Fatalf("retiring: %+v", got)
	}
	if got := h.keys.inState("api", fabriqloom.KeyActive); len(got) != 1 {
		t.Fatalf("active: %+v", got)
	}
}

func TestBindConsumerRefusesMissingTenantOption(t *testing.T) {
	h := newHarness("acme")
	_, err := h.hooks.BindConsumer(t.Context(), h.consumer("api", ""))
	if err == nil || !strings.Contains(err.Error(), "api needs a default tenant") {
		t.Fatalf("got %v", err)
	}
	if _, err := h.hooks.BindConsumer(t.Context(), h.consumer("api", "Not_Valid")); err == nil {
		t.Fatal("an invalid tenant was accepted")
	}
	if len(h.f.seen()) != 0 {
		t.Fatal("Fabriq was asked anything")
	}
}

func TestBindConsumerKeepsSecretsOutOfErrors(t *testing.T) {
	h := newHarness("acme")
	h.bind(t, "api", "acme")
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadGateway, http.StatusInternalServerError} {
		h.f.refuse = status
		_, err := h.hooks.BindConsumer(t.Context(), h.consumer("web", "acme"))
		if err == nil {
			t.Fatalf("%d: no error", status)
		}
		noSecret(t, h.f, err)
	}
}

func TestReleaseConsumerRevokesAndForgets(t *testing.T) {
	h := newHarness("acme")
	old := idOf(t, h.f, h.bind(t, "api", "acme"))
	next := idOf(t, h.f, h.bind(t, "api", "globex"))
	web := idOf(t, h.f, h.bind(t, "web", "acme"))
	if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("api", "globex")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, next} {
		if h.f.key(id).RevokedAt == nil {
			t.Fatalf("%s is still valid", id)
		}
		if h.secrets.has(id) {
			t.Fatalf("%s is still in Vault", id)
		}
	}
	if got := h.keys.inState("api", fabriqloom.KeyRevoked); len(got) != 2 {
		t.Fatalf("revoked: %+v", got)
	}
	if h.f.key(web).RevokedAt != nil || !h.secrets.has(web) {
		t.Fatal("web's key went too")
	}
	// Twice, and for a service never bound, is no error.
	if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("api", "globex")); err != nil {
		t.Fatal(err)
	}
	if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("jobs", "acme")); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseConsumerRevokesUnrecordedKeys(t *testing.T) {
	h := newHarness("acme")
	// A bind issued this key and kept it in Vault, then crashed before
	// recording it, so api has no record at all.
	orphan := h.f.plant("loomplane:api")
	if err := h.secrets.Put(t.Context(), vaultKey(orphan.ID), []byte(orphan.Key)); err != nil {
		t.Fatal(err)
	}
	webs := h.f.plant("loomplane:web")
	session := h.f.plant("session")
	if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("api", "acme")); err != nil {
		t.Fatal(err)
	}
	if h.f.key(orphan.ID).RevokedAt == nil {
		t.Fatal("the unrecorded key still opens Fabriq")
	}
	if h.secrets.has(orphan.ID) {
		t.Fatal("the unrecorded key is still in Vault")
	}
	if h.f.key(webs.ID).RevokedAt != nil || h.f.key(session.ID).RevokedAt != nil {
		t.Fatal("a key with another label was revoked")
	}
	// With nothing recorded, a Fabriq that can't be reached is no error:
	// the next sweep that reaches it revokes what is left.
	down := h.consumer("api", "acme")
	down.Channel = func(context.Context, string, string) (*deployments.ComponentChannel, error) {
		return nil, errors.New("dial lp-a:8080: connection refused")
	}
	if err := h.hooks.ReleaseConsumer(t.Context(), down); err != nil {
		t.Fatalf("unreachable with nothing recorded: %v", err)
	}
	for _, status := range []int{http.StatusBadGateway, http.StatusInternalServerError, http.StatusUnauthorized} {
		h.f.refuse = status
		if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("api", "acme")); err != nil {
			t.Fatalf("%d with nothing recorded: %v", status, err)
		}
	}
	h.f.refuse = 0
	// A failed revoke with nothing recorded is quiet too.
	h.f.plant("loomplane:api")
	h.f.failRevoke = true
	if err := h.hooks.ReleaseConsumer(t.Context(), h.consumer("api", "acme")); err != nil {
		t.Fatalf("a failed revoke with nothing recorded: %v", err)
	}
}

func TestRemovedToleratesAGoneServer(t *testing.T) {
	h := newHarness("acme")
	api := idOf(t, h.f, h.bind(t, "api", "acme"))
	web := idOf(t, h.f, h.bind(t, "web", "acme"))
	h.access.err = errors.New("dial lp-a:8080: connection refused")
	if err := h.hooks.Removed(t.Context(), deployments.ComposedResource{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq", Kind: "fabriq"}); err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"api", "web"} {
		if got := h.keys.inState(service, fabriqloom.KeyRevoked); len(got) != 1 || got[0].RevokedAt == nil {
			t.Fatalf("%s: %+v", service, got)
		}
	}
	if h.secrets.has(api) || h.secrets.has(web) {
		t.Fatal("Vault still holds the keys")
	}
}

func TestRemovedRevokesInFabriqWhenItCan(t *testing.T) {
	h := newHarness("acme")
	api := idOf(t, h.f, h.bind(t, "api", "acme"))
	if err := h.hooks.Removed(t.Context(), deployments.ComposedResource{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq", Kind: "fabriq"}); err != nil {
		t.Fatal(err)
	}
	if h.f.key(api).RevokedAt == nil {
		t.Fatal("api's key is still valid")
	}
}

func TestReadyRevokesKeysNoLiveRecordNames(t *testing.T) {
	h := newHarness("acme")
	api := idOf(t, h.f, h.bind(t, "api", "acme"))
	web := idOf(t, h.f, h.bind(t, "web", "acme"))
	// The resource was removed once while Fabriq was down: Loomplane
	// recorded web's key revoked, and Fabriq, re-added on the Postgres that
	// outlived it, still accepts it.
	if err := h.keys.SetState(t.Context(), "org", web, []fabriqloom.KeyState{fabriqloom.KeyActive}, fabriqloom.KeyRevoked); err != nil {
		t.Fatal(err)
	}
	old := h.f.plant("loomplane:old-svc")
	if err := h.secrets.Put(t.Context(), vaultKey(old.ID), []byte(old.Key)); err != nil {
		t.Fatal(err)
	}
	session := h.f.plant("session")
	ci := h.f.plant("ci")
	if err := h.hooks.Ready(t.Context(), adminRun(h, "admin")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old.ID, web} {
		if h.f.key(id).RevokedAt == nil {
			t.Fatalf("%s still opens Fabriq", id)
		}
	}
	if h.secrets.has(old.ID) {
		t.Fatal("old-svc's key is still in Vault")
	}
	if h.f.key(api).RevokedAt != nil || !h.secrets.has(api) {
		t.Fatal("api's recorded key was revoked")
	}
	if h.f.key(session.ID).RevokedAt != nil || h.f.key(ci.ID).RevokedAt != nil {
		t.Fatal("a key Loomplane didn't label was revoked")
	}
	// Other components don't sweep.
	h.f.plant("loomplane:later")
	if err := h.hooks.Ready(t.Context(), adminRun(h, "server")); err != nil {
		t.Fatal(err)
	}
	if got := h.f.valid("loomplane:later"); len(got) != 1 {
		t.Fatalf("the server's readiness swept: %v", got)
	}
}

func TestReadyReportsAFailedSweep(t *testing.T) {
	h := newHarness("acme")
	orphan := h.f.plant("loomplane:old-svc")
	h.f.failRevoke = true
	err := h.hooks.Ready(t.Context(), adminRun(h, "admin"))
	if err == nil || !strings.Contains(err.Error(), "old-svc") {
		t.Fatalf("a failed revoke passed: %v", err)
	}
	noSecret(t, h.f, err)
	// A key Fabriq no longer has can't be used: that is revoked.
	h.f.failRevoke, h.f.revokeNotFound = false, true
	if err = h.hooks.Ready(t.Context(), adminRun(h, "admin")); err != nil {
		t.Fatalf("404: %v", err)
	}
	if h.f.key(orphan.ID) == nil {
		t.Fatal("the fake lost the key")
	}
}

func adminRun(h *harness, component string) deployments.ComponentRun {
	return deployments.ComponentRun{
		OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq", Kind: "fabriq", Provider: "server",
		Component: recipes.ResolvedComponent{Component: recipes.Component{Name: component, Env: []recipes.EnvVar{
			{Name: fixtureControlEnv, Secret: fixtureControlSecret},
		}}},
		Env:     map[string]string{fixtureControlEnv: fixtureControlToken},
		Secrets: map[string][]byte{fixtureControlSecret: []byte(fixtureControlToken)},
		Channel: func(_ context.Context, component, port string) (*deployments.ComponentChannel, error) {
			if component != "admin" || port != "http" {
				return nil, errors.New("only the console's http port reaches the admin API")
			}
			return h.f.channel(), nil
		},
	}
}

func TestReadyChecksTheOperatorKey(t *testing.T) {
	h := newHarness("acme")
	if err := h.hooks.Ready(t.Context(), adminRun(h, "admin")); err != nil {
		t.Fatal(err)
	}
	requests := h.f.seen()
	if n := h.f.count(http.MethodGet, "/admin/tenants"); n != 2 {
		t.Fatalf("GET /admin/tenants %d times", n)
	}
	if len(requests[0].Header.Values(fixtureControlHeader)) != 0 || requests[1].Header.Get(fixtureControlHeader) != fixtureControlToken {
		t.Fatal("readiness did not check refusal before sending the credential")
	}
	h.f.refuse = http.StatusUnauthorized
	err := h.hooks.Ready(t.Context(), adminRun(h, "admin"))
	if err == nil || err.Error() != "Fabriq refused Loomplane's operator key (HTTP 401)" {
		t.Fatalf("got %v", err)
	}
	// Other components aren't checked.
	before := len(h.f.seen())
	if err = h.hooks.Ready(t.Context(), adminRun(h, "server")); err != nil {
		t.Fatal(err)
	}
	if len(h.f.seen()) != before {
		t.Fatal("the server's readiness asked the admin API")
	}
	h.f.refuse = http.StatusBadGateway
	if err = h.hooks.Ready(t.Context(), adminRun(h, "admin")); !errors.Is(err, fabriqloom.ErrUnreachable) {
		t.Fatalf("502: %v", err)
	}
	// Another admin API version isn't the operator key's fault.
	h.f.refuse = http.StatusUpgradeRequired
	err = h.hooks.Ready(t.Context(), adminRun(h, "admin"))
	if !errors.Is(err, fabriqloom.ErrAPIVersion) || !strings.HasPrefix(err.Error(), "this Fabriq server speaks another admin API version") || strings.Contains(err.Error(), "operator key") {
		t.Fatalf("426: %v", err)
	}
}

func TestReadyRequiresMatchingRenderedControlCredential(t *testing.T) {
	for name, alter := range map[string]func(*deployments.ComponentRun){
		"missing secret": func(run *deployments.ComponentRun) {
			delete(run.Secrets, fixtureControlSecret)
		},
		"malformed secret": func(run *deployments.ComponentRun) {
			run.Secrets[fixtureControlSecret] = []byte(strings.ToUpper(fixtureControlToken))
		},
		"missing env": func(run *deployments.ComponentRun) {
			delete(run.Env, fixtureControlEnv)
		},
		"env differs": func(run *deployments.ComponentRun) {
			run.Env[fixtureControlEnv] = strings.Repeat("b", 64)
		},
		"wrong env source": func(run *deployments.ComponentRun) {
			run.Component.Env[0].Secret = "admin-key"
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness("acme")
			run := adminRun(h, "admin")
			alter(&run)
			err := h.hooks.Ready(t.Context(), run)
			if err == nil || strings.Contains(err.Error(), fixtureControlToken) {
				t.Fatal("bad rendered credential was accepted or exposed")
			}
			if len(h.f.seen()) != 0 || h.f.closes() != 1 {
				t.Fatal("bad rendered credential reached the console or leaked its channel")
			}
		})
	}
}

func TestReadyChecksControlGate(t *testing.T) {
	for _, tc := range []struct {
		name, servedToken, errorText string
		requests                     int
	}{
		{"ungated runtime", "", "without a token with HTTP 200", 1},
		{"wrong rendered token", strings.Repeat("b", 64), "refused Loomplane's control credential (HTTP 403)", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness("acme")
			h.f.controlToken = tc.servedToken
			err := h.hooks.Ready(t.Context(), adminRun(h, "admin"))
			if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatal("readiness accepted a failed control gate check")
			}
			if len(h.f.seen()) != tc.requests || h.f.closes() != 1 {
				t.Fatal("failed gate check did not stop readiness")
			}
		})
	}
}

func legacyAdminRun(t *testing.T, h *harness, architecture string) deployments.ComponentRun {
	t.Helper()
	_, history, err := recipes.Default().ResolveRevision("fabriq", "1.9", "1", architecture, "server")
	if err != nil {
		t.Fatal(err)
	}
	run := adminRun(h, "admin")
	for _, component := range history.Components {
		if component.Name == "admin" {
			run.Component = component
			break
		}
	}
	run.Secrets = map[string][]byte{"admin-key": []byte("fq_fixture_operator")}
	run.Env = map[string]string{
		"FABRIQ_ADMIN_UPSTREAM":     "http://lp-fabriq:8081",
		"FABRIQ_ADMIN_UPSTREAM_KEY": string(run.Secrets["admin-key"]),
		"FABRIQ_ADMIN_BASE_PATH":    fabriqloom.ConsoleBasePath,
	}
	for _, env := range run.Component.Env {
		if env.Name == "LOOMPLANE_FABRIQ_SERVER_IMAGE" {
			run.Env[env.Name] = env.Value
		}
	}
	return run
}

func TestReadyRequiresHistoricalImageAndEnv(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			h := newHarness("acme")
			h.f.controlToken = ""
			if err := h.hooks.Ready(t.Context(), legacyAdminRun(t, h, architecture)); err != nil {
				t.Fatal(err)
			}
			if n := h.f.count(http.MethodGet, "/admin/tenants"); n != 1 {
				t.Fatalf("historical tenant requests: %d", n)
			}
		})
	}
	for name, alter := range map[string]func(*deployments.ComponentRun){
		"different image": func(run *deployments.ComponentRun) {
			run.Component.Image = "ghcr.io/xraph/fabriq-admin@sha256:" + strings.Repeat("a", 64)
		},
		"different env contract": func(run *deployments.ComponentRun) {
			run.Component.Env[1].Secret = "different-secret"
		},
		"different rendered env": func(run *deployments.ComponentRun) {
			run.Env["FABRIQ_ADMIN_BASE_PATH"] = "/"
		},
		"unexpected rendered token": func(run *deployments.ComponentRun) {
			run.Env[fixtureControlEnv] = fixtureControlToken
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness("acme")
			h.f.controlToken = ""
			run := legacyAdminRun(t, h, "amd64")
			alter(&run)
			if err := h.hooks.Ready(t.Context(), run); err == nil || strings.Contains(err.Error(), fixtureControlToken) {
				t.Fatal("modified historical component was accepted or exposed a credential")
			}
			if len(h.f.seen()) != 0 || h.f.closes() != 1 {
				t.Fatal("modified historical component reached the console or leaked its channel")
			}
		})
	}
}

func TestHookControlCredentialFailuresKeepRemoteKeys(t *testing.T) {
	ctx := t.Context()
	h := newHarness("acme")
	old := idOf(t, h.f, h.bind(t, "api", "acme"))
	current := idOf(t, h.f, h.bind(t, "api", "globex"))
	h.access.secretErr = errors.New("Vault failure with " + fixtureControlToken)
	seen, closed := len(h.f.seen()), h.f.closes()

	if _, err := h.hooks.BindConsumer(ctx, h.consumer("jobs", "another")); err == nil || strings.Contains(err.Error(), fixtureControlToken) {
		t.Fatal("credential failure let bind reach Fabriq or exposed the credential")
	}
	if err := h.hooks.ReleaseConsumer(ctx, h.consumer("api", "globex")); err == nil || strings.Contains(err.Error(), fixtureControlToken) {
		t.Fatal("credential failure let release reach Fabriq or exposed the credential")
	}
	if err := fabriqloom.NewObserver(h.deps()).Released(ctx, release(t, "api", "fabriq")); err == nil || strings.Contains(err.Error(), fixtureControlToken) {
		t.Fatal("credential failure let the observer revoke a key or exposed the credential")
	}
	if len(h.f.seen()) != seen || h.f.closes() != closed+3 {
		t.Fatal("failed credentials reached Fabriq or left channels open")
	}
	if len(h.keys.inState("api", fabriqloom.KeyActive)) != 1 || len(h.keys.inState("api", fabriqloom.KeyRetiring)) != 1 {
		t.Fatal("credential failure changed the live records")
	}
	for _, id := range []string{old, current} {
		if !h.secrets.has(id) || h.f.key(id).RevokedAt != nil {
			t.Fatal("a live key was revoked or forgotten")
		}
	}

	h.access.secretErr = nil
	h.access.controlToken = []byte(strings.Repeat("b", 64))
	if err := h.hooks.ReleaseConsumer(ctx, h.consumer("api", "globex")); err == nil {
		t.Fatal("the wrong token revoked live keys")
	}
	for _, id := range []string{old, current} {
		if !h.secrets.has(id) || h.f.key(id).RevokedAt != nil {
			t.Fatal("the wrong token revoked or forgot a live key")
		}
	}

	// Removal still forgets the local keys when no matching parent remains.
	h.access.secretErr = deployments.ErrComponentNotFound
	seen, closed = len(h.f.seen()), h.f.closes()
	if err := h.hooks.Removed(ctx, deployments.ComposedResource{
		OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq", Kind: "fabriq",
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.f.seen()) != seen || h.f.closes() != closed+1 {
		t.Fatal("removal tried the console without a credential or left its channel open")
	}
	for _, id := range []string{old, current} {
		if h.secrets.has(id) || h.f.key(id).RevokedAt != nil {
			t.Fatal("removal failed to forget a key locally or touched a remote key")
		}
	}
}

func TestConsumerControlCredentialUsesExactScope(t *testing.T) {
	for name, alter := range map[string]func(*deployments.Consumer){
		"org":         func(c *deployments.Consumer) { c.OrgID = "other" },
		"project":     func(c *deployments.Consumer) { c.ProjectID = "other" },
		"environment": func(c *deployments.Consumer) { c.EnvironmentID = "other" },
		"resource":    func(c *deployments.Consumer) { c.Resource = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness("acme")
			c := h.consumer("api", "acme")
			alter(&c)
			if _, err := h.hooks.BindConsumer(t.Context(), c); err == nil {
				t.Fatal("wrong scope obtained a credential")
			}
			if len(h.f.seen()) != 0 || h.f.closes() != 1 {
				t.Fatal("wrong scope reached Fabriq or left its channel open")
			}
		})
	}
}
