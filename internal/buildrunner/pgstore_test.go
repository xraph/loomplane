package buildrunner

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
)

func testPgStore(t *testing.T) (*PgStore, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for runner pool store tests")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_runners_%d", time.Now().UnixNano())
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

// addNode inserts a runner node row for a member to reference.
func addNode(t *testing.T, conn *pgx.Conn, id, role string) error {
	t.Helper()
	now := time.Now().UTC()
	_, err := conn.Exec(t.Context(), `INSERT INTO loomplane_nodes(id,org_id,name,public_key,key_fingerprint,role,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`,
		id, "org_1", id, []byte(id), "fp-"+id, role, now)
	return err
}

func TestPgStorePoolsAndMembers(t *testing.T) {
	s, conn := testPgStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	pool := &Pool{ID: "rpool_1", OrgID: "org_1", Scope: ScopeOrganization, Name: "builds", ConnectionID: "conn_1", Mode: ModeSelfHosted, Revision: 1,
		Settings: PoolSettings{MaxRunners: 3}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePool(ctx, &Pool{ID: "rpool_2", OrgID: "org_1", Scope: ScopeOrganization, Name: "builds", ConnectionID: "conn_2", Mode: ModeSelfHosted, Revision: 1, CreatedAt: now, UpdatedAt: now}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second pool with the name: %v", err)
	}
	if err := s.CreatePool(ctx, &Pool{ID: "rpool_3", OrgID: "org_1", Scope: ScopeProject, Name: "noproject", ConnectionID: "conn_3", Mode: ModeSelfHosted, Revision: 1, CreatedAt: now, UpdatedAt: now}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a project pool without a project: %v", err)
	}
	got, err := s.PoolByConnection(ctx, "org_1", "conn_1")
	if err != nil || got.ID != "rpool_1" || got.Settings.MaxRunners != 3 {
		t.Fatalf("pool %+v %v", got, err)
	}
	if _, err = s.PoolByConnection(ctx, "org_2", "conn_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization's pool by its connection: %v", err)
	}
	got.Architecture, got.RuntimeIdentity, got.Revision = "amd64", "id", 2
	if err = s.UpdatePool(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePool(ctx, got, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("an update against a stale revision: %v", err)
	}
	// A member references a runner node.
	if err = s.AddMember(ctx, &Member{NodeID: "node_x", PoolID: "rpool_1", OrgID: "org_1"}); err == nil {
		t.Fatal("a member without a node was added")
	}
	if err = addNode(t, conn, "node_1", "runner"); err != nil {
		t.Fatalf("a runner node: %v", err)
	}
	if err = addNode(t, conn, "node_2", "runner"); err != nil {
		t.Fatal(err)
	}
	if err = addNode(t, conn, "node_bad", "builder"); err == nil {
		t.Fatal("a node with an unknown role was stored")
	}
	for _, id := range []string{"node_1", "node_2"} {
		if err = s.AddMember(ctx, &Member{NodeID: id, PoolID: "rpool_1", OrgID: "org_1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordBuild(ctx, "node_1", now); err != nil {
		t.Fatal(err)
	}
	members, err := s.ListMembers(ctx, "rpool_1")
	if err != nil || len(members) != 2 || members[0].NodeID != "node_2" || members[1].Builds != 1 {
		t.Fatalf("members %+v %v; the runner that never built comes first", members, err)
	}
	m := members[1]
	m.RuntimeIdentity, m.Evidence, m.Reason = "id", map[string]string{"isolation": "firecracker"}, ""
	if err = s.UpdateMember(ctx, &m); err != nil {
		t.Fatal(err)
	}
	back, err := s.GetMember(ctx, "node_1")
	if err != nil || back.Evidence["isolation"] != "firecracker" || back.RuntimeIdentity != "id" {
		t.Fatalf("member %+v %v", back, err)
	}
	if err = s.DeletePool(ctx, "rpool_1"); !errors.Is(err, ErrInUse) {
		t.Fatalf("a pool with members was deleted: %v", err)
	}
	for _, id := range []string{"node_1", "node_2"} {
		if err = s.DeleteMember(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.DeletePool(ctx, "rpool_1"); err != nil {
		t.Fatal(err)
	}
}

func TestPgStoreMachines(t *testing.T) {
	s, conn := testPgStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	pool := &Pool{ID: "rpool_od", OrgID: "org_1", Scope: ScopeOrganization, Name: "burst", ConnectionID: "conn_od", Mode: ModeOnDemand, Revision: 1,
		Settings: PoolSettings{Provider: "digitalocean", MaxRunners: 2}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	m := &Machine{ID: "rmach_1", PoolID: pool.ID, OrgID: "org_1", Name: "runner-1", Region: "nyc3", Size: "s-2vcpu-4gb-amd", State: MachineCreating, CreatedAt: now, ActiveAt: now}
	if err := s.CreateMachine(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := addNode(t, conn, "node_m1", "runner"); err != nil {
		t.Fatal(err)
	}
	m.ProviderID, m.State, m.NodeID = "d1", MachineReady, "node_m1"
	if err := s.UpdateMachine(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.AdjustInFlight(ctx, "node_m1", 1, false, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AdjustInFlight(ctx, "node_m1", -1, true, now); err != nil {
		t.Fatal(err)
	}
	// An update that read the machine before the build finished doesn't
	// undo its count.
	m.Reason = "checked"
	if err := s.UpdateMachine(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.MachineByNode(ctx, "node_m1")
	if err != nil || got.Builds != 1 || got.InFlight != 0 || got.Reason != "checked" {
		t.Fatalf("machine %+v %v", got, err)
	}
	pools, err := s.OnDemandPools(ctx)
	if err != nil || len(pools) != 1 || pools[0].ID != pool.ID {
		t.Fatalf("on-demand pools %+v %v", pools, err)
	}
	m.State = MachineGone
	if err = s.UpdateMachine(ctx, m); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.ListMachines(ctx, pool.ID); len(ms) != 0 {
		t.Fatalf("a gone machine is listed: %+v", ms)
	}
}

func TestPgStoreWaitsQueueInOrder(t *testing.T) {
	s, _ := testPgStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	for i, op := range []string{"build_a", "build_b", "build_c"} {
		ahead, err := s.Wait(ctx, "org_1", "conn_pool", op, now.Add(time.Duration(i)*time.Second))
		if err != nil || ahead != i {
			t.Fatalf("%s: ahead %d %v", op, ahead, err)
		}
	}
	// Another pool's waits don't count.
	if ahead, _ := s.Wait(ctx, "org_1", "conn_other", "build_x", now.Add(5*time.Second)); ahead != 0 {
		t.Fatalf("another pool counted: %d", ahead)
	}
	if err := s.Unwait(ctx, "build_a"); err != nil {
		t.Fatal(err)
	}
	if ahead, _ := s.Wait(ctx, "org_1", "conn_pool", "build_c", now.Add(10*time.Second)); ahead != 1 {
		t.Fatalf("after the first got a runner, build_c has %d ahead", ahead)
	}
	// A wait no build refreshed stops counting.
	if ahead, _ := s.Wait(ctx, "org_1", "conn_pool", "build_c", now.Add(10*time.Minute)); ahead != 0 {
		t.Fatalf("an expired wait still counts: %d", ahead)
	}
}
