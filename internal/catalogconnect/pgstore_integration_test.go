//go:build integration

package catalogconnect

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
)

// scratchStore is a PgStore over a database of its own, dropped when the
// test ends. The tables are the migration's, and the part of
// loomplane_bindings the in-use check reads.
func scratchStore(t *testing.T) (*PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the catalog endpoint store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("catalog_endpoints_%d", time.Now().UnixNano())
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
	for _, stmt := range []string{
		`CREATE TABLE loomplane_catalog_endpoints (
			id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL, engine TEXT NOT NULL, host TEXT NOT NULL,
			port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535), database TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '', tls TEXT NOT NULL, created_by TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL, revision INTEGER NOT NULL DEFAULT 1, UNIQUE (org_id, name))`,
		`CREATE TABLE loomplane_bindings (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, config JSONB NOT NULL DEFAULT '{}')`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	return NewPgStore(pg), pg
}

func TestPgStoreKeepsEndpointsAndRefusesDeletingOneInUse(t *testing.T) {
	store, pg := scratchStore(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e := &Endpoint{
		ID: "endpoint_01", OrgID: "org", Name: "prod-pg", Engine: "postgres", Host: "db.example", Port: 5432,
		Database: "app", Username: "u", TLS: "require", CreatedBy: "user", CreatedAt: at, Revision: 1,
	}
	if err := store.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	twin := *e
	twin.ID = "endpoint_02"
	if err := store.Create(ctx, &twin); !errors.Is(err, ErrNameTaken) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second prod-pg: %v", err)
	}
	other := *e
	other.ID, other.OrgID = "endpoint_03", "other"
	if err := store.Create(ctx, &other); err != nil {
		t.Fatalf("another organization's prod-pg: %v", err)
	}
	got, err := store.Get(ctx, "org", e.ID)
	if err != nil || !got.CreatedAt.Equal(at) {
		t.Fatalf("get: %+v %v", got, err)
	}
	got.CreatedAt = at
	if *got != *e {
		t.Fatalf("get: %+v", got)
	}
	if _, err = store.Get(ctx, "other", e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization's get: %v", err)
	}
	list, err := store.List(ctx, "org")
	if err != nil || len(list) != 1 || list[0].ID != e.ID {
		t.Fatalf("list: %+v %v", list, err)
	}

	for _, b := range []struct{ id, org, config string }{
		{"b1", "org", `{"composition":{"dependencies":{"postgres":{"external":"endpoint_01"}}}}`},
		{"b2", "other", `{"composition":{"dependencies":{"postgres":{"external":"endpoint_03"}}}}`},
	} {
		if _, err = pg.Exec(ctx, `INSERT INTO loomplane_bindings (id, org_id, config) VALUES ($1, $2, $3)`, b.id, b.org, b.config); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Delete(ctx, "org", e.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("deleted an endpoint in use: %v", err)
	}
	if _, err = store.Get(ctx, "org", e.ID); err != nil {
		t.Fatalf("a refused delete removed the row: %v", err)
	}
	// A binding that connects a provisioned dependency, or another
	// organization's binding, doesn't hold the endpoint.
	if _, err = pg.Exec(ctx, `UPDATE loomplane_bindings SET config = '{"composition":{"dependencies":{"postgres":{"resource":"db"}}}}' WHERE id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, `UPDATE loomplane_bindings SET config = '{"composition":{"dependencies":{"postgres":{"external":"endpoint_01"}}}}' WHERE id = 'b2'`); err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(ctx, "org", e.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(ctx, "org", e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
