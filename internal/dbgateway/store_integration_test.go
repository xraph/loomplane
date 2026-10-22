//go:build integration

package dbgateway

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/ops"
)

// testStore is a Store over a database of its own, migrated like production
// and holding one project with one environment.
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for database gateway store tests")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_dbgateway_%d", time.Now().UnixNano())
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
	dsn = u.String()
	if err = ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	for _, q := range []string{
		`INSERT INTO loomplane_projects (id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES ('proj_1','org_1','P','p','active','usr_1',now(),now())`,
		`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env_1','org_1','proj_1','production','production',now(),now())`,
		`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env_2','org_1','proj_1','staging','preview',now(),now())`,
	} {
		if _, err = pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(pg)
}

func newGateway(t *testing.T, env, name string) Gateway {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Gateway{
		OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: env, Name: name,
		Provider: "cloud-run", Region: "us-central1", Suffix: "db.example.com",
		SigningPublicKey: pub, SealingPublicKey: seal.PublicKey(),
	}
}

var idGrammar = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func TestEnsureGatewayIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	first := newGateway(t, "env_1", "main")
	stored, created, err := s.EnsureGateway(ctx, first)
	if err != nil || !created {
		t.Fatalf("first ensure: created=%v err=%v", created, err)
	}
	if !idGrammar.MatchString(stored.ID) {
		t.Fatalf("gateway id %q breaks the plane's grammar", stored.ID)
	}
	if !bytes.Equal(stored.SigningPublicKey, first.SigningPublicKey) || !bytes.Equal(stored.SealingPublicKey.Bytes(), first.SealingPublicKey.Bytes()) {
		t.Fatal("stored keys differ from the ones given")
	}

	// A second ensure with other keys keeps the first ones, whatever else it says.
	again := newGateway(t, "env_1", "main")
	again.Region = "europe-west1"
	got, created, err := s.EnsureGateway(ctx, again)
	if err != nil || created {
		t.Fatalf("second ensure: created=%v err=%v", created, err)
	}
	if got.ID != stored.ID || got.Region != "us-central1" {
		t.Fatalf("second ensure returned %+v, want the stored gateway %+v", got, stored)
	}
	if !bytes.Equal(got.SigningPublicKey, first.SigningPublicKey) || !bytes.Equal(got.SealingPublicKey.Bytes(), first.SealingPublicKey.Bytes()) {
		t.Fatal("a second ensure replaced the stored keys")
	}

	// The same name in another environment is another gateway.
	other, created, err := s.EnsureGateway(ctx, newGateway(t, "env_2", "main"))
	if err != nil || !created || other.ID == stored.ID {
		t.Fatalf("other environment: %+v created=%v err=%v", other, created, err)
	}

	read, err := s.Gateway(ctx, "org_1", stored.ID)
	if err != nil || read.Name != "main" || !bytes.Equal(read.SigningPublicKey, first.SigningPublicKey) || !bytes.Equal(read.SealingPublicKey.Bytes(), first.SealingPublicKey.Bytes()) {
		t.Fatalf("read back %+v: %v", read, err)
	}
	if _, err = s.Gateway(ctx, "org_2", stored.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization read the gateway: %v", err)
	}
	if _, err = s.Gateway(ctx, "org_1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing gateway: %v", err)
	}
	list, err := s.Gateways(ctx, "org_1", "env_1")
	if err != nil || len(list) != 1 || list[0].ID != stored.ID {
		t.Fatalf("environment list: %+v %v", list, err)
	}
	list, err = s.Gateways(ctx, "org_1", "")
	if err != nil || len(list) != 2 {
		t.Fatalf("organization list: %+v %v", list, err)
	}
}

func TestEnsureGatewayRejectsBadInput(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	noSigning := newGateway(t, "env_1", "a")
	noSigning.SigningPublicKey = nil
	noSealing := newGateway(t, "env_1", "b")
	noSealing.SealingPublicKey = nil
	badID := newGateway(t, "env_1", "c")
	badID.ID = "has space"
	noEnvironment := newGateway(t, "env_missing", "d")
	for name, g := range map[string]Gateway{"signing key": noSigning, "sealing key": noSealing, "id": badID, "environment": noEnvironment} {
		if _, _, err := s.EnsureGateway(ctx, g); err == nil {
			t.Errorf("accepted a gateway with a bad %s", name)
		}
	}
}

func TestEnsureGatewayConcurrent(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	results := make([]bool, 8)
	ids := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, created, err := s.EnsureGateway(t.Context(), newGateway(t, "env_1", "racy"))
			if err != nil {
				t.Error(err)
				return
			}
			results[i], ids[i] = created, g.ID
		}()
	}
	wg.Wait()
	created := 0
	for i, c := range results {
		if c {
			created++
		}
		if ids[i] != ids[0] {
			t.Fatalf("callers saw different gateways: %v", ids)
		}
	}
	if created != 1 {
		t.Fatalf("%d callers created the gateway, want 1", created)
	}
}

func TestEnsureRouteKeepsSalt(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	gw, _, err := s.EnsureGateway(ctx, newGateway(t, "env_1", "main"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.EnsureRoute(ctx, "org_1", gw.ID, "orders-db")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Salt) != 16 || bytes.Equal(first.Salt, make([]byte, 16)) {
		t.Fatalf("salt %x, want 16 random bytes", first.Salt)
	}
	if first.State != "active" || first.Resource != "orders-db" || !idGrammar.MatchString(first.ID) {
		t.Fatalf("route %+v", first)
	}
	again, err := s.EnsureRoute(ctx, "org_1", gw.ID, "orders-db")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !bytes.Equal(again.Salt, first.Salt) {
		t.Fatalf("second ensure changed the route: %+v then %+v", first, again)
	}
	second, err := s.EnsureRoute(ctx, "org_1", gw.ID, "billing-db")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || bytes.Equal(second.Salt, first.Salt) {
		t.Fatalf("two resources share an id or salt: %+v %+v", first, second)
	}

	if err = s.SetRouteState(ctx, "org_1", gw.ID, first.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRouteState(ctx, "org_1", gw.ID, first.ID, "sleeping"); err == nil {
		t.Fatal("accepted an unknown state")
	}
	if err = s.SetRouteState(ctx, "org_1", gw.ID, "missing", "active"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing route: %v", err)
	}
	// Pausing and resuming leaves the salt alone.
	routes, err := s.Routes(ctx, "org_1", gw.ID)
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes %+v: %v", routes, err)
	}
	for _, r := range routes {
		switch r.Resource {
		case "orders-db":
			if r.State != "paused" || !bytes.Equal(r.Salt, first.Salt) {
				t.Fatalf("paused route %+v", r)
			}
		case "billing-db":
			if r.State != "active" {
				t.Fatalf("untouched route %+v", r)
			}
		default:
			t.Fatalf("unexpected route %+v", r)
		}
	}
	if _, err = s.EnsureRoute(ctx, "org_1", "missing", "orders-db"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("route on a missing gateway: %v", err)
	}
	if _, err = s.EnsureRoute(ctx, "org_2", gw.ID, "orders-db"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("route on another organization's gateway: %v", err)
	}
}

func TestBumpSnapshotOnlyOnChange(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	gw, _, err := s.EnsureGateway(ctx, newGateway(t, "env_1", "main"))
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		digest  string
		version uint64
		changed bool
	}{
		{"aaa", 1, true},
		{"aaa", 1, false},
		{"bbb", 2, true},
		{"bbb", 2, false},
		{"aaa", 3, true},
	}
	for i, st := range steps {
		v, changed, err := s.BumpSnapshot(ctx, "org_1", gw.ID, st.digest)
		if err != nil || v != st.version || changed != st.changed {
			t.Fatalf("step %d (%s): version=%d changed=%v err=%v, want %d %v", i, st.digest, v, changed, err, st.version, st.changed)
		}
	}
	if _, _, err = s.BumpSnapshot(ctx, "org_1", "missing", "aaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot of a missing gateway: %v", err)
	}
}

func TestBumpSnapshotConcurrentBumpsOnce(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	gw, _, err := s.EnsureGateway(ctx, newGateway(t, "env_1", "main"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BumpSnapshot(ctx, "org_1", gw.ID, "base"); err != nil {
		t.Fatal(err)
	}
	for round, digest := range []string{"fresh", "newer"} {
		var wg sync.WaitGroup
		versions := make([]uint64, 8)
		changed := make([]bool, 8)
		for i := range versions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, c, err := s.BumpSnapshot(ctx, "org_1", gw.ID, digest)
				if err != nil {
					t.Error(err)
					return
				}
				versions[i], changed[i] = v, c
			}()
		}
		wg.Wait()
		want := uint64(round + 2)
		bumps := 0
		for i := range versions {
			if versions[i] != want {
				t.Fatalf("round %d: versions %v, want all %d", round, versions, want)
			}
			if changed[i] {
				bumps++
			}
		}
		if bumps != 1 {
			t.Fatalf("round %d: %d callers saw a change, want 1", round, bumps)
		}
	}
}

func TestBumpSnapshotConcurrentFirstSnapshot(t *testing.T) {
	s := testStore(t)
	gw, _, err := s.EnsureGateway(t.Context(), newGateway(t, "env_1", "main"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	versions := make([]uint64, 8)
	for i := range versions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _, err := s.BumpSnapshot(t.Context(), "org_1", gw.ID, "first")
			if err != nil {
				t.Error(err)
			}
			versions[i] = v
		}()
	}
	wg.Wait()
	for _, v := range versions {
		if v != 1 {
			t.Fatalf("versions %v, want all 1", versions)
		}
	}
}

func TestReportCountsLive(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	gw, _, err := s.EnsureGateway(ctx, newGateway(t, "env_1", "main"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	stale := plane.Report{InstanceID: "inst-old", SnapshotVersion: 3}
	if live, err := s.Report(ctx, "org_1", gw.ID, stale, now.Add(-31*time.Second)); err != nil || live != 1 {
		// Seen at its own report time, an instance is live for itself.
		t.Fatalf("first report: live=%d err=%v", live, err)
	}
	fresh := plane.Report{
		InstanceID: "inst-new", SnapshotVersion: 4, Draining: true,
		Routes: []plane.RouteStats{{RouteID: "r1", BackendConns: 3, Pinned: 1, PinRate: 0.25, WaitP95: 42 * time.Millisecond, Reachable: true, MaxConnections: 100}},
	}
	live, err := s.Report(ctx, "org_1", gw.ID, fresh, now)
	if err != nil || live != 1 {
		t.Fatalf("two instances, one 31s old: live=%d err=%v, want 1", live, err)
	}
	// Exactly 30 seconds old is no longer live; 29 is. The count is taken
	// at the time of the report that asks for it.
	for id, age := range map[string]time.Duration{"inst-edge": 30 * time.Second, "inst-near": 29 * time.Second} {
		if _, err = s.Report(ctx, "org_1", gw.ID, plane.Report{InstanceID: id}, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if live, err = s.Report(ctx, "org_1", gw.ID, fresh, now); err != nil || live != 2 {
		t.Fatalf("with a 30s and a 29s old instance: live=%d err=%v, want 2", live, err)
	}

	// A repeat report updates the instance rather than adding one.
	fresh.SnapshotVersion, fresh.Draining = 5, false
	if _, err = s.Report(ctx, "org_1", gw.ID, fresh, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Instances(ctx, "org_1", gw.ID)
	if err != nil || len(got) != 4 {
		t.Fatalf("instances %+v: %v", got, err)
	}
	var newest InstanceRecord
	for _, in := range got {
		if in.InstanceID == "inst-new" {
			newest = in
		}
	}
	if newest.SnapshotVersion != 5 || newest.Draining || !newest.LastSeenAt.Equal(now) {
		t.Fatalf("updated instance %+v", newest)
	}
	if len(newest.Metrics) != 1 || newest.Metrics[0] != fresh.Routes[0] {
		t.Fatalf("stored metrics %+v, want %+v", newest.Metrics, fresh.Routes)
	}
	for _, in := range got {
		if in.Metrics == nil {
			t.Fatalf("instance %s has nil metrics", in.InstanceID)
		}
	}

	if _, err = s.Report(ctx, "org_1", "missing", fresh, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("report for a missing gateway: %v", err)
	}
	if _, err = s.Report(ctx, "org_1", gw.ID, plane.Report{}, now); err == nil {
		t.Fatal("accepted a report with no instance id")
	}
}

func TestDeletingTheEnvironmentRemovesItsGateways(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	gw, _, err := s.EnsureGateway(ctx, newGateway(t, "env_1", "main"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureRoute(ctx, "org_1", gw.ID, "orders-db"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BumpSnapshot(ctx, "org_1", gw.ID, "aaa"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Report(ctx, "org_1", gw.ID, plane.Report{InstanceID: "i"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(ctx, `DELETE FROM loomplane_environments WHERE id='env_1'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"gateways", "gateway_routes", "gateway_snapshots", "gateway_instances"} {
		var n int
		if err = s.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_db_`+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s kept %d rows after its environment was deleted: %v", table, n, err)
		}
	}
}
