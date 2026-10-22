//go:build integration

package dbgateway

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
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
	svc := dbgateway.NewService(store, resourceURLs{secrets: secrets}, cpPriv, trust, time.Now)
	r := forge.NewRouter()
	if err = mount(r, &handlers{svc: svc, wait: 200 * time.Millisecond, every: 20 * time.Millisecond, now: time.Now,
		warn: func(msg string, werr error) { t.Errorf("%s: %v", msg, werr) }}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	client := plane.NewControlClient(srv.URL, gw.ID, signPriv)
	s, err := client.Poll(ctx, 0)
	if err != nil || s == nil {
		t.Fatalf("Poll: %+v %v", s, err)
	}
	if err = plane.VerifySnapshot(*s, svc.TrustKeys(), gw.ID, time.Now()); err != nil {
		t.Fatalf("VerifySnapshot: %v", err)
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
	again, err := client.Poll(ctx, s.Version)
	if err != nil || again != nil {
		t.Fatalf("second poll: %+v %v", again, err)
	}
	if err = store.SetRouteState(ctx, "org_1", gw.ID, route.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	next, err := client.Poll(ctx, s.Version)
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
	ahead, err := client.Poll(ctx, 100)
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
