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
	e3, err := s.ClaimSession(ctx, Session{NodeID: "node_1", ConnectedAt: at})
	if err != nil || e3 != 3 {
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

func TestUpdateSettings(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	n := newNode("node_1", "org_1", "web-1")
	n.Endpoints = []nodeproto.Endpoint{nodeproto.EndpointDocker, nodeproto.EndpointKube}
	if err := s.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	// A status set elsewhere survives a settings write.
	if err := s.SetStatus(ctx, "node_1", StatusOnline); err != nil {
		t.Fatal(err)
	}
	// A rename leaves the ingress mode nobody's choice.
	name := "web-1b"
	if got, err := s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Name: &name}); err != nil || got.IngressModeByOwner {
		t.Fatalf("rename: %+v %v", got, err)
	}
	name, mode := "web-2", "relay"
	got, err := s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Name: &name, IngressMode: &mode, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusOnline || got.Name != "web-2" || got.IngressMode != "relay" || len(got.Endpoints) != 1 || got.Labels["zone"] != "a" || !got.IngressModeByOwner {
		t.Fatalf("node %+v", got)
	}
	// An address check's settling doesn't take the owner's choice away.
	at := time.Now().UTC()
	if got, err = s.SetIngress(ctx, "node_1", IngressSetting{Mode: "none", Detail: "no public address answered", SentAt: at}); err != nil || !got.IngressModeByOwner {
		t.Fatalf("after a check: %+v %v", got, err)
	}

	// A stale widen is refused, and nothing changes.
	_, err = s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker, nodeproto.EndpointKube}})
	if !errors.Is(err, ErrWidenRemotely) {
		t.Fatalf("widen: %v", err)
	}
	// Narrowing to nothing and clearing labels are real changes.
	got, err = s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Endpoints: []nodeproto.Endpoint{}, Labels: map[string]string{}})
	if err != nil || len(got.Endpoints) != 0 || len(got.Labels) != 0 {
		t.Fatalf("empty: %v %+v", err, got)
	}
	// Wrong org and revoked nodes are not found.
	if _, err = s.UpdateSettings(ctx, "org_2", "node_1", SettingsUpdate{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org: %v", err)
	}
	cur, _ := s.GetByID(ctx, "node_1")
	now := time.Now().UTC()
	cur.RevokedAt, cur.RevokedBy = &now, "usr_1"
	if err = s.Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err = s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked widen: %v", err)
	}
}

func TestUnchangedUpdateSettingsAppendsNoEvent(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	n, _ := s.GetByID(ctx, "node_1")
	var before, after int
	q := `SELECT count(*) FROM loomplane_events WHERE type='node.updated'`
	if err := conn.QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Name: &n.Name, Labels: n.Labels, IngressMode: &n.IngressMode, Endpoints: n.Endpoints}); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, q).Scan(&after); err != nil || after != before {
		t.Fatalf("events %d -> %d %v", before, after, err)
	}
}

func countNodeEvents(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var c int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_events WHERE type='node.updated'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestApplyInventoryWritesOnlyItsColumns(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	n := newNode("node_1", "org_1", "web-1")
	n.Endpoints = []nodeproto.Endpoint{nodeproto.EndpointProbe}
	if err := s.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, "node_1", StatusOnline); err != nil {
		t.Fatal(err)
	}
	before := countNodeEvents(t, conn)

	// Inventory alone is not in the event payload, so it appends nothing.
	inv := nodeproto.Inventory{OS: "linux", CPUs: 16, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker}}
	if err := s.ApplyInventory(ctx, "node_1", inv, "1.0.0", 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetByID(ctx, "node_1")
	if got.Inventory.CPUs != 16 || got.Status != StatusOnline || len(got.Endpoints) != 1 || got.Endpoints[0] != nodeproto.EndpointProbe {
		t.Fatalf("node %+v", got)
	}
	if c := countNodeEvents(t, conn); c != before {
		t.Fatalf("inventory-only change appended events: %d -> %d", before, c)
	}

	// A version change is in the payload, so it appends one.
	if err := s.ApplyInventory(ctx, "node_1", inv, "1.1.0", 1); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetByID(ctx, "node_1")
	if got.Version != "1.1.0" || got.ProtocolVersion != 1 {
		t.Fatalf("version %q protocol %d", got.Version, got.ProtocolVersion)
	}
	if c := countNodeEvents(t, conn); c != before+1 {
		t.Fatalf("version change events: %d -> %d", before, c)
	}

	if err := s.ApplyInventory(ctx, "nope", inv, "1.1.0", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	now := time.Now().UTC()
	got.RevokedAt, got.RevokedBy = &now, "usr_1"
	if err := s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyInventory(ctx, "node_1", nodeproto.Inventory{OS: "plan9"}, "9.9.9", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked: %v", err)
	}
	got, _ = s.GetByID(ctx, "node_1")
	if got.Inventory.OS != "linux" || got.Version != "1.1.0" {
		t.Fatalf("revoked node changed: %+v", got)
	}
}

func TestAllowEndpointsUnions(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	n := newNode("node_1", "org_1", "web-1")
	n.Endpoints = []nodeproto.Endpoint{nodeproto.EndpointProbe}
	if err := s.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, "node_1", StatusOnline); err != nil {
		t.Fatal(err)
	}
	before := countNodeEvents(t, conn)
	got, err := s.AllowEndpoints(ctx, "node_1", []nodeproto.Endpoint{nodeproto.EndpointDocker, nodeproto.EndpointProbe, nodeproto.EndpointDocker})
	if err != nil {
		t.Fatal(err)
	}
	want := []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker}
	if fmt.Sprint(got.Endpoints) != fmt.Sprint(want) || got.Status != StatusOnline {
		t.Fatalf("node %+v", got)
	}
	if c := countNodeEvents(t, conn); c != before+1 {
		t.Fatalf("widen events: %d -> %d", before, c)
	}
	// Allowing what is already there changes nothing.
	got, err = s.AllowEndpoints(ctx, "node_1", []nodeproto.Endpoint{nodeproto.EndpointDocker})
	if err != nil || fmt.Sprint(got.Endpoints) != fmt.Sprint(want) {
		t.Fatalf("again: %v %+v", err, got)
	}
	if c := countNodeEvents(t, conn); c != before+1 {
		t.Fatalf("no-op allow appended events: %d", c)
	}

	now := time.Now().UTC()
	got.RevokedAt, got.RevokedBy = &now, "usr_1"
	if err := s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowEndpoints(ctx, "node_1", []nodeproto.Endpoint{nodeproto.EndpointKube}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := s.AllowEndpoints(ctx, "nope", []nodeproto.Endpoint{nodeproto.EndpointKube}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestUseNonceOnce(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	exp := time.Now().Add(time.Minute)
	if err := s.UseNonce(ctx, "n1", exp); err != nil {
		t.Fatal(err)
	}
	if err := s.UseNonce(ctx, "n1", exp); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("replay: %v", err)
	}
	if err := s.UseNonce(ctx, "n2", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExpiredNonces(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The expired row is gone; the live one still refuses a replay.
	if err := s.UseNonce(ctx, "n2", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("expired row kept: %v", err)
	}
	if err := s.UseNonce(ctx, "n1", exp); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("live row pruned: %v", err)
	}
}

func TestMarkOfflineIfEpoch(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	e1, err := s.ClaimSession(ctx, Session{NodeID: "node_1", Replica: "a", ConnectedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := s.ClaimSession(ctx, Session{NodeID: "node_1", Replica: "b", ConnectedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetStatus(ctx, "node_1", StatusOnline); err != nil {
		t.Fatal(err)
	}
	// The older session ending leaves the newer one's status alone.
	if err = s.MarkOfflineIfEpoch(ctx, "node_1", e1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(ctx, "node_1"); got.Status != StatusOnline {
		t.Fatalf("stale epoch: %s", got.Status)
	}
	if err = s.MarkOfflineIfEpoch(ctx, "node_1", e2); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(ctx, "node_1"); got.Status != StatusOffline {
		t.Fatalf("current epoch: %s", got.Status)
	}
	// A revoked node stays revoked.
	cur, _ := s.GetByID(ctx, "node_1")
	now := time.Now().UTC()
	cur.RevokedAt, cur.RevokedBy = &now, "usr_1"
	if err = s.Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkOfflineIfEpoch(ctx, "node_1", e2); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(ctx, "node_1"); got.Status != StatusRevoked {
		t.Fatalf("revoked: %s", got.Status)
	}
}

// A key belongs to one node for good, revoked or not. NameTaken looks at live
// nodes of one org only.
func TestKeyNeverJoinsTwice(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	first := newNode("node_1", "org_1", "web-1")
	if err := s.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first.Status, first.RevokedAt, first.RevokedBy = StatusRevoked, &now, "usr_1"
	if err := s.Update(ctx, first); err != nil {
		t.Fatal(err)
	}
	if known, err := s.KeyKnown(ctx, "fp-node_1"); err != nil || !known {
		t.Fatalf("revoked key known: %v %v", known, err)
	}
	if known, err := s.KeyKnown(ctx, "fp-other"); err != nil || known {
		t.Fatalf("unknown key known: %v %v", known, err)
	}
	again := newNode("node_2", "org_2", "web-2")
	again.KeyFingerprint = "fp-node_1"
	if err := s.Create(ctx, again); !errors.Is(err, ErrKeyKnown) {
		t.Fatalf("a revoked node's key joined again: %v", err)
	}
	if taken, err := s.NameTaken(ctx, "org_1", "web-1"); err != nil || taken {
		t.Fatalf("a revoked node's name is taken: %v %v", taken, err)
	}
	if err := s.Create(ctx, newNode("node_3", "org_1", "web-3")); err != nil {
		t.Fatal(err)
	}
	if taken, err := s.NameTaken(ctx, "org_1", "web-3"); err != nil || !taken {
		t.Fatalf("live name: %v %v", taken, err)
	}
	if taken, err := s.NameTaken(ctx, "org_2", "web-3"); err != nil || taken {
		t.Fatalf("name in another org: %v %v", taken, err)
	}
}

func TestSetIngressAndAppliedRoundTrip(t *testing.T) {
	s, conn := testStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, newNode("node_1", "org_1", "web-1")); err != nil {
		t.Fatal(err)
	}
	events := func() int {
		var c int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE type='node.updated'`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	before := events()
	at := time.Now().UTC().Truncate(time.Microsecond)
	got, err := s.SetIngress(ctx, "node_1", IngressSetting{Mode: "direct", PublicAddress: "203.0.113.7", ConfirmedAt: &at, SentAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if got.IngressMode != "direct" || got.PublicAddress != "203.0.113.7" || got.PublicAddressConfirmedAt == nil || !got.PublicAddressConfirmedAt.Equal(at) || got.IngressSentAt == nil || got.IngressApplied != "" || got.IngressDetail != "" {
		t.Fatalf("node %+v", got)
	}
	if events() != before+1 {
		t.Fatal("a new address appended no node.updated")
	}
	// The node's answer is bookkeeping: stored, and no event. An answer
	// from a superseded session is dropped.
	epoch, err := s.ClaimSession(ctx, Session{NodeID: "node_1", Replica: "r1", ConnectedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordIngressApplied(ctx, "node_1", "relay", epoch-1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale answer: %v", err)
	}
	if err = s.RecordIngressApplied(ctx, "node_1", "direct", epoch); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GetByID(ctx, "node_1"); n.IngressApplied != "direct" || events() != before+1 {
		t.Fatalf("applied %+v, %d events", n, events())
	}
	got, err = s.SetIngress(ctx, "node_1", IngressSetting{Mode: "none", Detail: "no public address answered", SentAt: at})
	if err != nil || got.PublicAddress != "" || got.PublicAddressConfirmedAt != nil || got.IngressDetail != "no public address answered" || got.IngressApplied != "direct" {
		t.Fatalf("none: %+v %v", got, err)
	}
	// A detail alone keeps the rest and appends no event.
	count := events()
	if err = s.SetIngressDetail(ctx, "node_1", "address changed to 198.51.100.9 but it doesn't answer on port 80"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GetByID(ctx, "node_1"); n.IngressDetail != "address changed to 198.51.100.9 but it doesn't answer on port 80" || n.IngressMode != "none" || !n.IngressSentAt.Equal(at) || events() != count {
		t.Fatalf("detail %+v, %d events", n, events())
	}
	// The first-inventory resend restamps the send time alone, with no event.
	count = events()
	resent := at.Add(time.Minute)
	if err = s.MarkIngressSent(ctx, "node_1", resent); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GetByID(ctx, "node_1"); !n.IngressSentAt.Equal(resent) || events() != count {
		t.Fatalf("resent %+v, %d events", n, events())
	}
	// A settings change of the mode sends it again and drops the old detail.
	mode := "direct"
	got, err = s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{IngressMode: &mode})
	if err != nil || got.IngressDetail != "" || got.IngressSentAt == nil || !got.IngressSentAt.After(at) {
		t.Fatalf("settings: %+v %v", got, err)
	}
	name := "web-2"
	again, err := s.UpdateSettings(ctx, "org_1", "node_1", SettingsUpdate{Name: &name})
	if err != nil || !again.IngressSentAt.Equal(*got.IngressSentAt) {
		t.Fatalf("a rename moved the send time: %+v %v", again, err)
	}
	// A revoked node takes neither.
	n, _ := s.GetByID(ctx, "node_1")
	now := time.Now().UTC()
	n.RevokedAt, n.RevokedBy, n.Status = &now, "usr_1", StatusRevoked
	if err = s.Update(ctx, n); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetIngress(ctx, "node_1", IngressSetting{Mode: "none", SentAt: at}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked set: %v", err)
	}
	if err = s.MarkIngressSent(ctx, "node_1", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked sent: %v", err)
	}
	if err = s.RecordIngressApplied(ctx, "node_1", "none", epoch); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked applied: %v", err)
	}
	if err = s.SetIngressDetail(ctx, "node_1", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked detail: %v", err)
	}
}
