//go:build integration

package fabriqloom

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
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/deployments"
)

// scratchKeys is a PgStore over a database of its own, dropped when the
// test ends, with the fabriq migration applied twice (central migrations
// aren't transactional, so a rerun must be harmless).
func scratchKeys(t *testing.T) (*PgStore, *pgdriver.PgDB, *migrate.Migration, migrate.Executor) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the Fabriq key store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fabriq_keys_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("LOOMPLANE_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations.Group().Migrations() {
		if m.Version != "20261115000100" {
			continue
		}
		if m.Name != "fabriq" {
			t.Fatalf("migration 20261115000100 is %q", m.Name)
		}
		for range 2 {
			if err = m.Up(ctx, exec); err != nil {
				t.Fatal(err)
			}
		}
		return NewPgStore(pg), pg, m, exec
	}
	t.Fatal("no fabriq migration")
	return nil, nil, nil, nil
}

func TestServiceKeyLockCoordinatesStoreInstances(t *testing.T) {
	s, db, _, _ := scratchKeys(t)
	other := NewPgStore(db)
	release, err := s.LockService(t.Context(), "org", "env", "fabriq", "api")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if unlock, lockErr := other.LockService(ctx, "org", "env", "fabriq", "api"); lockErr == nil {
		unlock()
		t.Fatal("two store instances held the same service key lock")
	}
	ctxOther, cancelOther := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelOther()
	unlockOther, err := other.LockService(ctxOther, "org", "env", "fabriq", "web")
	if err != nil {
		t.Fatalf("unrelated service blocked: %v", err)
	}
	unlockOther()
	release()
	unlockAgain, err := other.LockService(ctxOther, "org", "env", "fabriq", "api")
	if err != nil {
		t.Fatalf("service lock wasn't released: %v", err)
	}
	unlockAgain()
}

func TestKeyStorePG(t *testing.T) {
	store, pg, m, exec := scratchKeys(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	record := func(id, service string, state KeyState, offset int) *KeyRecord {
		created := at.Add(time.Duration(offset) * time.Minute)
		return &KeyRecord{ID: id, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq", Service: service,
			Tenant: "acme", Prefix: "fq_" + id, State: state, CreatedAt: created, UpdatedAt: created}
	}

	if err := store.Insert(ctx, record("k1", "api", KeyActive, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, record("k2", "api", KeyActive, 1)); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second active key: %v", err)
	}
	if err := store.Insert(ctx, record("k2", "api", KeyPending, 1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, record("k3", "api", KeyPending, 2)); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second pending key: %v", err)
	}
	// Other services, environments and resources don't conflict.
	if err := store.Insert(ctx, record("k4", "web", KeyActive, 3)); err != nil {
		t.Fatal(err)
	}
	other := record("k5", "api", KeyActive, 4)
	other.EnvironmentID = "env2"
	if err := store.Insert(ctx, other); err != nil {
		t.Fatal(err)
	}
	search := record("k6", "api", KeyActive, 5)
	search.Resource = "fabriq2"
	if err := store.Insert(ctx, search); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, record("k1", "jobs", KeyActive, 6)); err == nil {
		t.Fatal("a duplicate key id")
	}

	// Promoting pending while another is active conflicts; retiring first
	// doesn't.
	if err := store.SetState(ctx, "org", "k2", []KeyState{KeyPending}, KeyActive); !errors.Is(err, ErrConflict) {
		t.Fatalf("two active keys: %v", err)
	}
	if err := store.SetState(ctx, "org", "k1", []KeyState{KeyActive}, KeyRetiring); err != nil {
		t.Fatal(err)
	}
	if err := store.SetState(ctx, "org", "k2", []KeyState{KeyPending}, KeyActive); err != nil {
		t.Fatal(err)
	}
	if err := store.SetState(ctx, "org", "k1", []KeyState{KeyActive}, KeyRevoked); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("from the wrong state: %v", err)
	}
	if err := store.SetState(ctx, "other-org", "k1", []KeyState{KeyRetiring}, KeyRevoked); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("another organization's key: %v", err)
	}
	if err := store.SetState(ctx, "org", "nope", []KeyState{KeyRetiring}, KeyRevoked); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("an unknown key: %v", err)
	}
	if err := store.SetState(ctx, "org", "k1", []KeyState{KeyRetiring}, KeyRevoked); err != nil {
		t.Fatal(err)
	}

	api, err := store.ForService(ctx, "org", "env", "fabriq", "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(api) != 2 || api[0].ID != "k2" || api[1].ID != "k1" {
		t.Fatalf("api's keys, newest first: %+v", api)
	}
	if api[0].State != KeyActive || api[0].RevokedAt != nil || api[0].Tenant != "acme" || api[0].Prefix != "fq_k2" || api[0].ProjectID != "proj" || !api[0].CreatedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("k2: %+v", api[0])
	}
	if api[1].State != KeyRevoked || api[1].RevokedAt == nil || !api[1].UpdatedAt.After(api[1].CreatedAt) {
		t.Fatalf("k1: %+v", api[1])
	}
	all, err := store.ForResource(ctx, "org", "env", "fabriq")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "k4" {
		t.Fatalf("the resource's keys: %+v", all)
	}
	if none, err := store.ForResource(ctx, "other-org", "env", "fabriq"); err != nil || len(none) != 0 {
		t.Fatalf("another organization: %+v %v", none, err)
	}

	if err := m.Down(ctx, exec); err != nil {
		t.Fatal(err)
	}
	if err := m.Down(ctx, exec); err != nil {
		t.Fatal(err)
	}
	var gone string
	if err := pg.QueryRow(ctx, `SELECT coalesce(to_regclass('loomplane_fabriq_keys')::text, 'gone')`).Scan(&gone); err != nil || gone != "gone" {
		t.Fatalf("down left the table: %s %v", gone, err)
	}
}

func TestRebuildStorePG(t *testing.T) {
	_, pg, _, _ := scratchKeys(t)
	store := NewPgRebuildStore(pg)
	ctx := t.Context()
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	ref := deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq"}
	rebuild := func(id, projection, request string, offset int) *Rebuild {
		return &Rebuild{ID: id, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq", Projection: projection,
			RequestID: request, Status: RebuildQueued, CreatedBy: "usr", CreatedAt: at.Add(time.Duration(offset) * time.Minute)}
	}

	first, err := store.Create(ctx, rebuild("r1", "graph", "request-0001", 0))
	if err != nil || first.ID != "r1" || first.Status != RebuildQueued || !first.CreatedAt.Equal(at) {
		t.Fatalf("first: %+v %v", first, err)
	}
	// Another request while it's queued gets it; so does the same request.
	again, err := store.Create(ctx, rebuild("r2", "graph", "request-0002", 1))
	if err != nil || again.ID != "r1" {
		t.Fatalf("while queued: %+v %v", again, err)
	}
	if again, err = store.Create(ctx, rebuild("r3", "graph", "request-0001", 1)); err != nil || again.ID != "r1" {
		t.Fatalf("the same request: %+v %v", again, err)
	}
	if _, err = store.Create(ctx, rebuild("r4", "search", "request-0001", 1)); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("a request id for another projection: %v", err)
	}
	search, err := store.Create(ctx, rebuild("r5", "search", "request-0005", 2))
	if err != nil || search.ID != "r5" {
		t.Fatalf("search: %+v %v", search, err)
	}
	if _, err = store.Create(ctx, rebuild("r6", "vectors", "request-0006", 3)); err == nil {
		t.Fatal("an unknown projection was stored")
	}

	claimed, err := store.Claim(ctx, "org", "r1", "attempt-1")
	if err != nil || claimed.Status != RebuildRunning || claimed.Attempt != "attempt-1" {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if _, err = store.Claim(ctx, "org", "r1", "attempt-2"); !errors.Is(err, ErrRebuildClaimed) {
		t.Fatalf("a second claim: %v", err)
	}
	// Still running: a new request gets it.
	if again, err = store.Create(ctx, rebuild("r7", "graph", "request-0007", 4)); err != nil || again.ID != "r1" {
		t.Fatalf("while running: %+v %v", again, err)
	}
	unfinished, err := store.Unfinished(ctx)
	if err != nil || len(unfinished) != 2 || unfinished[0].ID != "r1" || unfinished[1].ID != "r5" {
		t.Fatalf("unfinished: %+v %v", unfinished, err)
	}
	done := at.Add(time.Hour)
	if err = store.Finish(ctx, "org", "r1", "attempt-2", RebuildFailed, "late", done); !errors.Is(err, ErrRebuildClaimed) {
		t.Fatalf("another attempt's finish: %v", err)
	}
	if err = store.Finish(ctx, "org", "r1", "attempt-1", RebuildFailed, "tenant globex: connection reset", done); err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(ctx, "org", "r1", "attempt-1", RebuildSucceeded, "", done); !errors.Is(err, ErrRebuildClaimed) {
		t.Fatalf("finishing twice: %v", err)
	}
	// Finished, the projection takes a new rebuild.
	next, err := store.Create(ctx, rebuild("r8", "graph", "request-0008", 5))
	if err != nil || next.ID != "r8" {
		t.Fatalf("after it finished: %+v %v", next, err)
	}

	list, err := store.List(ctx, "org", ref)
	if err != nil || len(list) != 3 || list[0].ID != "r8" || list[2].ID != "r1" {
		t.Fatalf("list: %+v %v", list, err)
	}
	old := list[2]
	if old.Status != RebuildFailed || old.Output != "tenant globex: connection reset" || old.FinishedAt == nil || !old.FinishedAt.Equal(done) || old.CreatedBy != "usr" || old.RequestID != "request-0001" {
		t.Fatalf("r1: %+v", old)
	}
	if other, oerr := store.List(ctx, "other-org", ref); oerr != nil || len(other) != 0 {
		t.Fatalf("another organization: %+v %v", other, oerr)
	}
	if _, err = store.Claim(ctx, "other-org", "r8", "attempt-3"); !errors.Is(err, ErrRebuildClaimed) {
		t.Fatalf("another organization's claim: %v", err)
	}
}
