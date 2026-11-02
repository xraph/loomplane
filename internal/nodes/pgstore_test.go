//go:build integration

package nodes

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

func testStore(t *testing.T) (*PgStore, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for node store tests")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_nodes_%d", time.Now().UnixNano())
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
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return NewPgStore(pg), conn
}

func newNode(id, org, name string) *Node {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &Node{
		ID: id, OrgID: org, Name: name, PublicKey: []byte{1, 2, 3}, KeyFingerprint: "fp-" + id,
		IdentityGeneration: 1, Status: StatusPending, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker},
		IngressMode: "none", Role: "node", Labels: map[string]string{"zone": "a"}, Version: "1.0.0", ProtocolVersion: 1,
		Inventory: nodeproto.Inventory{OS: "linux", CPUs: 4}, JoinedBy: "usr_1", JoinedByKeyID: "key_1",
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestCreateGetListRoundTrip(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	n := newNode("node_1", "org_1", "web-1")
	if err := s.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "org_1", "node_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web-1" || got.Labels["zone"] != "a" || len(got.Endpoints) != 1 || got.Inventory.CPUs != 4 || string(got.PublicKey) != "\x01\x02\x03" || got.JoinedByKeyID != "key_1" {
		t.Fatalf("round trip: %+v", got)
	}
	if _, err = s.Get(ctx, "org_2", "node_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org get: %v", err)
	}
	if _, err = s.GetByID(ctx, "node_1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetByID(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	list, err := s.List(ctx, "org_1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	byKey, err := s.ListJoinedByKey(ctx, "org_1", "key_1")
	if err != nil || len(byKey) != 1 {
		t.Fatalf("by key: %v %v", byKey, err)
	}
	if byKey, _ = s.ListJoinedByKey(ctx, "org_1", "other"); len(byKey) != 0 {
		t.Fatalf("other key: %v", byKey)
	}
	got.Version = "1.1.0"
	got.Labels = map[string]string{"zone": "b"}
	if err = s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Get(ctx, "org_1", "node_1"); again.Version != "1.1.0" || again.Labels["zone"] != "b" {
		t.Fatalf("update: %+v", again)
	}
	if err = s.Update(ctx, newNode("ghost", "org_1", "ghost")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
}

func TestTouchWritesLastSeenAndUsageOnly(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	for range 3 {
		if err := s.Touch(ctx, "node_1", at, nodeproto.Usage{CPUPercent: 12.5, MemoryUsedMiB: 100, DiskUsedGiB: 5}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetByID(ctx, "node_1")
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(at) {
		t.Fatalf("last seen: %v", got.LastSeenAt)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE type='node.updated'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("events after touches: %d %v", n, err)
	}
	if err := s.Touch(ctx, "nope", at, nodeproto.Usage{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("touch missing: %v", err)
	}
}

func TestClaimSessionEpochs(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	e1, err := s.ClaimSession(ctx, Session{NodeID: "node_1", Replica: "r1", RemoteAddr: "1.1.1.1", ConnectedAt: at})
	if err != nil || e1 != 1 {
		t.Fatalf("first claim: %d %v", e1, err)
	}
	e2, err := s.ClaimSession(ctx, Session{NodeID: "node_1", Replica: "r2", RemoteAddr: "2.2.2.2", ConnectedAt: at})
	if err != nil || e2 != 2 {
		t.Fatalf("second claim: %d %v", e2, err)
	}
	if err = s.ReleaseSession(ctx, "node_1", 1); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Session(ctx, "node_1")
	if err != nil || sess.Epoch != 2 || sess.Replica != "r2" {
		t.Fatalf("session after stale release: %+v %v", sess, err)
	}
	if err = s.ReleaseSession(ctx, "node_1", 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, "node_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session after release: %v", err)
	}
	// Epochs keep rising after a release.
	if e3, err := s.ClaimSession(ctx, Session{NodeID: "node_1", ConnectedAt: at}); err != nil || e3 != 3 {
		t.Fatalf("claim after release: %d %v", e3, err)
	}
	if _, err = s.ClaimSession(ctx, Session{NodeID: "ghost", ConnectedAt: at}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim for a missing node: %v", err)
	}
}

func TestNameUniqueWhileLive(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, newNode("node_2", "org_1", "web-1")); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := s.Create(ctx, newNode("node_3", "org_2", "web-1")); err != nil {
		t.Fatalf("same name in another org: %v", err)
	}
	first, _ := s.GetByID(ctx, "node_1")
	now := time.Now().UTC()
	first.Status, first.RevokedAt, first.RevokedBy = StatusRevoked, &now, "usr_1"
	if err := s.Update(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, newNode("node_2", "org_1", "web-1")); err != nil {
		t.Fatalf("name after revoke: %v", err)
	}
}

func TestMarkStaleOffline(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	stale := newNode("node_stale", "org_1", "stale")
	stale.Status = StatusOnline
	seen := now.Add(-46 * time.Second)
	stale.LastSeenAt = &seen
	fresh := newNode("node_fresh", "org_1", "fresh")
	fresh.Status = StatusOnline
	recent := now.Add(-5 * time.Second)
	fresh.LastSeenAt = &recent
	pending := newNode("node_pending", "org_1", "pending")
	for _, n := range []*Node{stale, fresh, pending} {
		if err := s.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.MarkStaleOffline(ctx, now.Add(-45*time.Second))
	if err != nil || len(ids) != 1 || ids[0] != "node_stale" {
		t.Fatalf("stale: %v %v", ids, err)
	}
	if got, _ := s.GetByID(ctx, "node_stale"); got.Status != StatusOffline {
		t.Fatalf("status: %s", got.Status)
	}
	if got, _ := s.GetByID(ctx, "node_fresh"); got.Status != StatusOnline {
		t.Fatalf("fresh status: %s", got.Status)
	}
	if err = s.SetStatus(ctx, "node_fresh", StatusDraining); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(ctx, "node_fresh"); got.Status != StatusDraining {
		t.Fatalf("set status: %s", got.Status)
	}
	if err = s.SetStatus(ctx, "nope", StatusOnline); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set status missing: %v", err)
	}
}

func TestStaleReleaseKeepsNewSession(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	claim := func(replica string) int64 {
		e, err := s.ClaimSession(ctx, Session{NodeID: "node_1", Replica: replica, ConnectedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	claim("r1")
	claim("r2")
	if err := s.ReleaseSession(ctx, "node_1", 2); err != nil {
		t.Fatal(err)
	}
	if e := claim("r3"); e != 3 {
		t.Fatalf("epoch: %d", e)
	}
	for _, stale := range []int64{1, 2} {
		if err := s.ReleaseSession(ctx, "node_1", stale); err != nil {
			t.Fatal(err)
		}
	}
	if sess, err := s.Session(ctx, "node_1"); err != nil || sess.Epoch != 3 || sess.Replica != "r3" {
		t.Fatalf("session: %+v %v", sess, err)
	}
}

func revoke(t *testing.T, s *PgStore, id string) {
	t.Helper()
	n, err := s.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	n.Status, n.RevokedAt, n.RevokedBy = StatusRevoked, &now, "usr_1"
	if err = s.Update(t.Context(), n); err != nil {
		t.Fatal(err)
	}
}

func TestClaimRefusesRevoked(t *testing.T) {
	s, _ := testStore(t)
	if err := s.Create(t.Context(), newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	revoke(t, s, "node_1")
	if _, err := s.ClaimSession(t.Context(), Session{NodeID: "node_1", ConnectedAt: time.Now()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim on revoked: %v", err)
	}
	if _, err := s.Session(t.Context(), "node_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session row: %v", err)
	}
}

func TestStaleUpdateStaysRevoked(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.GetByID(ctx, "node_1")
	revoke(t, s, "node_1")
	revoked, _ := s.GetByID(ctx, "node_1")
	stale.Version = "9.9.9"
	stale.Status = StatusOnline
	if err := s.Update(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetByID(ctx, "node_1")
	if got.Status != StatusRevoked || got.RevokedAt == nil || !got.RevokedAt.Equal(*revoked.RevokedAt) || got.RevokedBy != "usr_1" || got.Version != "9.9.9" {
		t.Fatalf("after stale update: %+v", got)
	}
}

func TestSetStatusAfterRevoke(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	revoke(t, s, "node_1")
	if err := s.SetStatus(ctx, "node_1", StatusOnline); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set online after revoke: %v", err)
	}
	if got, _ := s.GetByID(ctx, "node_1"); got.Status != StatusRevoked {
		t.Fatalf("status: %s", got.Status)
	}
	if err := s.SetStatus(ctx, "node_1", StatusRevoked); err != nil {
		t.Fatal(err)
	}
}

func TestMarkStaleOfflineWithoutLastSeen(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	n := newNode("node_1", "org_1", "web-1")
	n.Status = StatusOnline
	if err := s.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE loomplane_nodes SET updated_at=now()-interval '10 minutes' WHERE id='node_1'`); err != nil {
		t.Fatal(err)
	}
	ids, err := s.MarkStaleOffline(ctx, time.Now().Add(-45*time.Second))
	if err != nil || len(ids) != 1 {
		t.Fatalf("stale: %v %v", ids, err)
	}
}

func TestUnchangedUpdateAppendsNoEvent(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	n, _ := s.GetByID(ctx, "node_1")
	if err := s.Update(ctx, n); err != nil {
		t.Fatal(err)
	}
	var c int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE type='node.updated'`).Scan(&c); err != nil || c != 1 {
		t.Fatalf("events: %d %v", c, err)
	}
}

func TestCreateDefaults(t *testing.T) {
	s, _ := testStore(t)
	n := newNode("node_1", "org_1", "web-1")
	n.IdentityGeneration, n.CreatedAt, n.UpdatedAt = 0, time.Time{}, time.Time{}
	if err := s.Create(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetByID(t.Context(), "node_1")
	if got.IdentityGeneration != 1 || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("defaults: %+v", got)
	}
}
