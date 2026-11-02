//go:build integration

package catalogproxy

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
)

// scratchStore is a PgStore over a database of its own, dropped when the
// test ends, with the migration's table.
func scratchStore(t *testing.T) *PgStore {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the catalog admin session store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("catalog_proxy_%d", time.Now().UnixNano())
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
		if m.Version != "20261030000200" {
			continue
		}
		if err = m.Up(ctx, exec); err != nil {
			t.Fatal(err)
		}
		return NewPgStore(pg)
	}
	t.Fatal("no catalog_proxy_sessions migration")
	return nil
}

func TestPgStoreKeepsSessions(t *testing.T) {
	store := scratchStore(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	r := &Record{TokenHash: hashToken("token-a"), OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "gateway", Kind: "octopus", UserID: "usr",
		CreatedAt: at, LastUsedAt: at, ExpiresAt: at.Add(IdleTTL)}
	if err := store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	other := *r
	other.TokenHash, other.Resource = hashToken("token-b"), "other"
	if err := store.Create(ctx, &other); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, r); err == nil {
		t.Fatal("kept a token hash twice")
	}
	got, err := store.Get(ctx, r.TokenHash)
	if err != nil || *got != *r {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err = store.Get(ctx, hashToken("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	later := at.Add(10 * time.Minute)
	if err = store.Touch(ctx, r.TokenHash, later, later.Add(IdleTTL)); err != nil {
		t.Fatal(err)
	}
	// A touch from a slower replica never moves a session back.
	if err = store.Touch(ctx, r.TokenHash, at.Add(time.Minute), at.Add(time.Minute+IdleTTL)); err != nil {
		t.Fatal(err)
	}
	if got, err = store.Get(ctx, r.TokenHash); err != nil || !got.LastUsedAt.Equal(later) || !got.ExpiresAt.Equal(later.Add(IdleTTL)) {
		t.Fatalf("touched: %+v %v", got, err)
	}

	if err = store.Prune(ctx, later.Add(IdleTTL)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, other.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired session survived pruning: %v", err)
	}
	if _, err = store.Get(ctx, r.TokenHash); err != nil {
		t.Fatalf("pruned a live session: %v", err)
	}

	if err = store.DeleteResource(ctx, "org", "env", "gateway"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, r.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked: %v", err)
	}
	if err = store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(ctx, r.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, r.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
}
