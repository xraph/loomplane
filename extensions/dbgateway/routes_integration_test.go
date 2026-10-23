//go:build integration

package dbgateway

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/security"
)

// testDB is a migrated database of its own holding one environment.
func testDB(t *testing.T) *pgdriver.PgDB {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for database gateway route tests")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_dbgateway_routes_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	for _, q := range []string{
		`INSERT INTO loomplane_projects (id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES ('proj_1','org_1','P','p','active','usr_1',now(),now())`,
		`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env_1','org_1','proj_1','production','production',now(),now())`,
	} {
		if _, err = pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return pg
}

// memSecrets is a deployments.ResourceSecrets over a map.
type memSecrets map[string][]byte

func (m memSecrets) Put(_ context.Context, k string, v []byte) error { m[k] = v; return nil }
func (m memSecrets) Delete(_ context.Context, k string) error        { delete(m, k); return nil }
func (m memSecrets) Get(_ context.Context, k string) ([]byte, error) {
	v, ok := m[k]
	if !ok {
		return nil, deployments.ErrNoCredential
	}
	return v, nil
}

// A gateway's own control client, against these routes over real HTTP, gets
// a snapshot that verifies and applies, reports, and is told there's no hold
// handler.
func TestControlClientAgainstTheRoutes(t *testing.T) {
	store := dbgateway.NewStore(testDB(t))
	ctx := t.Context()
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw, _, err := store.EnsureGateway(ctx, dbgateway.Gateway{
		OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "main",
		Provider: "cloud-run", Region: "us-central1", Suffix: "us-central1.db.example.com",
		SigningPublicKey: signPub, SealingPublicKey: sealing.PublicKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.EnsureRoute(ctx, "org_1", gw.ID, "orders")
	if err != nil {
		t.Fatal(err)
	}
	secrets := memSecrets{deployments.ResourceURLKey("org_1", "env_1", "orders"): []byte("postgres://app:pw@db.do.example:25060/orders?sslmode=require")}

	now := time.Now()
	cpPub, cpPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := plane.TrustKey{PublicKey: cpPub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	svc := dbgateway.NewService(store, resourceURLs{secrets: secrets}, []dbgateway.SigningKey{{Private: cpPriv, Trust: trust}}, time.Now)
	r := forge.NewRouter()
	if err = mount(r, &handlers{svc: svc, wait: 200 * time.Millisecond, every: 20 * time.Millisecond, now: time.Now,
		warn: func(msg string, werr error) { t.Errorf("%s: %v", msg, werr) }}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	client := plane.NewControlClient(srv.URL, gw.ID, signPriv)
	s, err := pollVerified(t, client, 0, svc.TrustKeys(), gw.ID)
	if err != nil || s == nil {
		t.Fatalf("Poll: %+v %v", s, err)
	}
	router := plane.NewRouter(&plane.Store{}, sealing, gw.ID, plane.PgDialer{}, nil, plane.Suffix(gw.Suffix))
	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		router.Drain(dctx)
	})
	if err = router.Apply(*s); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st := router.Stats(); len(st) != 1 || st[0].RouteID != route.ID || !st[0].Reachable {
		t.Fatalf("router stats %+v", st)
	}

	// Nothing changed: the poll waits and comes back empty.
	again, err := pollVerified(t, client, s.Version, svc.TrustKeys(), gw.ID)
	if err != nil || again != nil {
		t.Fatalf("second poll: %+v %v", again, err)
	}
	if err = store.SetRouteState(ctx, "org_1", gw.ID, route.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	next, err := pollVerified(t, client, s.Version, svc.TrustKeys(), gw.ID)
	if err != nil || next == nil || next.Version != s.Version+1 || next.Routes[0].State != plane.RouteStatePaused {
		t.Fatalf("after pausing: %+v %v", next, err)
	}

	resp, err := client.Report(ctx, plane.Report{InstanceID: "inst-1", SnapshotVersion: next.Version, Routes: router.Stats()})
	if err != nil || resp.LiveInstances != 1 {
		t.Fatalf("Report: %+v %v", resp, err)
	}
	if err = client.Hold(ctx, plane.Label{DB: "orders", Branch: "dev"}); err == nil {
		t.Fatal("Hold succeeded with no hold handler")
	}

	// A gateway ahead of the store, as after a restore, gets a version past
	// its own straight away.
	ahead, err := pollVerified(t, client, 100, svc.TrustKeys(), gw.ID)
	if err != nil || ahead == nil || ahead.Version != 101 {
		t.Fatalf("a poll after 100: %+v %v, want version 101", ahead, err)
	}
	if err = router.Apply(*ahead); err != nil {
		t.Fatalf("Apply after the raise: %v", err)
	}

	// Another key can't speak for this gateway.
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := plane.NewControlClient(srv.URL, gw.ID, otherKey).Poll(ctx, 0); err == nil {
		t.Fatal("a request signed with another key was answered")
	}
}

// catalogAuthz grants each user what security.Catalog gives their role.
type catalogAuthz map[string]string

func (a catalogAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	for _, r := range security.Catalog() {
		if r.Slug == a[p.UserID] && slices.Contains(r.Permissions, perm) {
			return nil
		}
	}
	return security.ErrForbidden
}

// The project routes over the real store: a viewer lists the gateway with
// its reporting instance and can't pause; an admin's pause is audited and
// reaches the gateway's next snapshot.
func TestManagementRoutesAgainstTheStore(t *testing.T) {
	store := dbgateway.NewStore(testDB(t))
	ctx := t.Context()
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw, _, err := store.EnsureGateway(ctx, dbgateway.Gateway{
		OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "main",
		Provider: "cloud-run", Region: "us-central1", Suffix: "us-central1.db.example.com",
		SigningPublicKey: signPub, SealingPublicKey: sealing.PublicKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.EnsureRoute(ctx, "org_1", gw.ID, "orders")
	if err != nil {
		t.Fatal(err)
	}
	secrets := memSecrets{deployments.ResourceURLKey("org_1", "env_1", "orders"): []byte("postgres://app:pw@db.do.example:25060/orders?sslmode=require")}
	now := time.Now()
	cpPub, cpPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := plane.TrustKey{PublicKey: cpPub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	svc := dbgateway.NewService(store, resourceURLs{secrets: secrets}, []dbgateway.SigningKey{{Private: cpPriv, Trust: trust}}, time.Now)
	trail, events := memChronicle(t)
	svc.Authz = catalogAuthz{"usr_viewer": security.RoleViewer, "usr_admin": security.RoleAdmin}
	svc.Audit = auditor{chronicle: trail, appID: "app_1", log: forge.NewNoopLogger()}
	r := forge.NewRouter()
	if err = mount(r, &handlers{svc: svc, wait: 200 * time.Millisecond, every: 20 * time.Millisecond, now: time.Now,
		warn: func(msg string, werr error) { t.Errorf("%s: %v", msg, werr) }}); err != nil {
		t.Fatal(err)
	}
	if err = mountManagement(r, nil, svc); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	client := plane.NewControlClient(srv.URL, gw.ID, signPriv)
	s, err := pollVerified(t, client, 0, svc.TrustKeys(), gw.ID)
	if err != nil || s == nil {
		t.Fatalf("Poll: %+v %v", s, err)
	}
	stats := []plane.RouteStats{{RouteID: route.ID, BackendConns: 3, MaxConnections: 25, PinRate: 0.5, Reachable: true}}
	if _, err = client.Report(ctx, plane.Report{InstanceID: "inst-1", SnapshotVersion: s.Version, Routes: stats}); err != nil {
		t.Fatal(err)
	}

	out := callAs(t, r, http.MethodGet, "/api/v1/projects/proj_1/db-gateways", "usr_viewer")
	if out.Code != http.StatusOK {
		t.Fatalf("list: %d %q", out.Code, out.Body.String())
	}
	var list GatewayList
	if err = json.Unmarshal(out.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Gateways) != 1 {
		t.Fatalf("list %s", out.Body.String())
	}
	g := list.Gateways[0]
	if g.ID != gw.ID || g.SnapshotVersion != s.Version || len(g.Instances) != 1 || !g.Instances[0].Reporting || g.Instances[0].Behind ||
		len(g.Routes) != 1 || g.Routes[0] != (dbgateway.RouteStatus{
		ID: route.ID, Resource: "orders", State: "active", Reported: true, Reachable: true, BackendConns: 3, MaxConnections: 25, PinRate: 0.5,
	}) {
		t.Fatalf("list %s", out.Body.String())
	}
	for _, secret := range []string{"salt", "postgres://", "public_key", "suffix"} {
		if strings.Contains(out.Body.String(), secret) {
			t.Fatalf("the list carries %q: %s", secret, out.Body.String())
		}
	}

	pause := "/api/v1/projects/proj_1/db-gateways/" + gw.ID + "/routes/" + route.ID + "/pause"
	if out = callAs(t, r, http.MethodPost, pause, "usr_viewer"); out.Code != http.StatusForbidden {
		t.Fatalf("a viewer's pause: %d %q, want 403", out.Code, out.Body.String())
	}
	if out = callAs(t, r, http.MethodPost, "/api/v1/projects/proj_2/db-gateways/"+gw.ID+"/routes/"+route.ID+"/pause", "usr_admin"); out.Code != http.StatusNotFound {
		t.Fatalf("a pause through another project: %d, want 404", out.Code)
	}
	// An admin of another organization sees no gateway and can't pause.
	elsewhere := func(method, uri string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, uri, nil)
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org_2", UserID: "usr_admin"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if out = elsewhere(http.MethodGet, "/api/v1/projects/proj_1/db-gateways"); out.Code != http.StatusOK || strings.Contains(out.Body.String(), gw.ID) {
		t.Fatalf("another organization's list: %d %s", out.Code, out.Body.String())
	}
	if out = elsewhere(http.MethodPost, pause); out.Code != http.StatusNotFound {
		t.Fatalf("another organization's pause: %d, want 404", out.Code)
	}
	routes, err := store.Routes(ctx, "org_1", gw.ID)
	if err != nil || routes[0].State != "active" {
		t.Fatalf("after refused pauses: %+v %v", routes, err)
	}
	if out = callAs(t, r, http.MethodPost, pause, "usr_admin"); out.Code != http.StatusOK {
		t.Fatalf("an admin's pause: %d %q", out.Code, out.Body.String())
	}
	next, err := pollVerified(t, client, s.Version, svc.TrustKeys(), gw.ID)
	if err != nil || next == nil || next.Routes[0].State != plane.RouteStatePaused {
		t.Fatalf("after pausing: %+v %v", next, err)
	}
	if out = callAs(t, r, http.MethodPost, "/api/v1/projects/proj_1/db-gateways/"+gw.ID+"/routes/"+route.ID+"/resume", "usr_admin"); out.Code != http.StatusOK {
		t.Fatalf("an admin's resume: %d %q", out.Code, out.Body.String())
	}
	// Each event names the snapshot version that carries its change.
	want := []string{
		fmt.Sprintf("dbGateway.route.pause usr_admin proj_1 env_1 %s %s orders paused v%d", gw.ID, route.ID, next.Version),
		fmt.Sprintf("dbGateway.route.resume usr_admin proj_1 env_1 %s %s orders active v%d", gw.ID, route.ID, next.Version+1),
	}
	if got := auditedRouteChanges(t, events); !slices.Equal(got, want) {
		t.Fatalf("audited %v, want %v", got, want)
	}
}

// pollVerified polls the control plane like a gateway and returns the
// snapshot verified against trust. Nothing new is nil.
func pollVerified(t *testing.T, client *plane.ControlClient, after uint64, trust []plane.TrustKey, gatewayID string) (*plane.Snapshot, error) {
	t.Helper()
	env, err := client.Poll(t.Context(), after)
	if err != nil || env == nil {
		return nil, err
	}
	s, err := plane.VerifySnapshot(*env, trust, gatewayID, time.Now())
	if err != nil {
		t.Fatalf("VerifySnapshot: %v", err)
	}
	return &s, nil
}
