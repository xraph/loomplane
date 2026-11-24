//go:build integration

package catalogconnect

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
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
			created_at TIMESTAMPTZ NOT NULL, revision INTEGER NOT NULL DEFAULT 1,
			origin TEXT NOT NULL DEFAULT 'registered' CHECK (origin IN ('registered','provider')),
			description TEXT NOT NULL DEFAULT '', environment_label TEXT NOT NULL DEFAULT '',
			restricted BOOLEAN NOT NULL DEFAULT false, admin_set BOOLEAN NOT NULL DEFAULT false,
			updated_at TIMESTAMPTZ, last_check JSONB, UNIQUE (org_id, name))`,
		`CREATE TABLE loomplane_catalog_endpoint_grants (
			endpoint_id TEXT NOT NULL REFERENCES loomplane_catalog_endpoints(id) ON DELETE CASCADE,
			org_id TEXT NOT NULL, project_id TEXT NOT NULL, created_by TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (endpoint_id, project_id))`,
		`CREATE TABLE loomplane_bindings (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL DEFAULT '',
			environment_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', config JSONB NOT NULL DEFAULT '{}')`,
		`CREATE TABLE loomplane_projects (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE loomplane_environments (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
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
	// A read fills in the workspace service's defaults: registered, and
	// shared with every project.
	want := *e
	want.Origin, want.Projects = OriginRegistered, []string{}
	if !reflect.DeepEqual(*got, want) {
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

func TestPgStoreKeepsWorkspaceServices(t *testing.T) {
	store, pg := scratchStore(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	e := &Endpoint{
		ID: "endpoint_ws", OrgID: "org", Name: "orders", Engine: "postgres", Host: "db.example", Port: 5432,
		Database: "orders", Username: "app", TLS: "require", CreatedBy: "user", CreatedAt: at, Revision: 1,
		Description: "Orders database", EnvironmentLabel: "production",
	}
	if err := store.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "org", e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin != OriginRegistered || got.Description != "Orders database" || got.EnvironmentLabel != "production" ||
		got.Restricted || len(got.Projects) != 0 || got.LastCheck != nil {
		t.Fatalf("created: %+v", got)
	}

	// Sharing replaces the list, in one go.
	if err = store.SetSharing(ctx, "org", e.ID, true, []string{"project_b", "project_a"}, "user", at); err != nil {
		t.Fatal(err)
	}
	if err = store.SetSharing(ctx, "org", e.ID, true, []string{"project_a", "project_c"}, "user", at); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.Get(ctx, "org", e.ID); !got.Restricted || !reflect.DeepEqual(got.Projects, []string{"project_a", "project_c"}) {
		t.Fatalf("shared: %v %v", got.Restricted, got.Projects)
	}
	if !got.SharedWith("project_c") || got.SharedWith("project_b") {
		t.Fatalf("shared with: %v", got.Projects)
	}
	if err = store.SetSharing(ctx, "org", "endpoint_none", false, nil, "user", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sharing a missing endpoint: %v", err)
	}

	// Details, then a rename onto a taken name.
	if got, err = store.Update(ctx, "org", e.ID, Details{Name: "orders-db", Description: "Orders", EnvironmentLabel: "staging"}, at); err != nil ||
		got.Name != "orders-db" || got.EnvironmentLabel != "staging" || got.UpdatedAt == nil {
		t.Fatalf("update: %+v %v", got, err)
	}
	other := *e
	other.ID, other.Name = "endpoint_other", "taken"
	if err = store.Create(ctx, &other); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Update(ctx, "org", e.ID, Details{Name: "taken"}, at); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename onto a taken name: %v", err)
	}

	// A replacement moves the revision on, once.
	next := *got
	next.Host, next.Database = "db2.example", "orders2"
	if err = store.Replace(ctx, &next, 1, at); err != nil {
		t.Fatal(err)
	}
	if err = store.Replace(ctx, &next, 1, at); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale replacement: %v", err)
	}
	if got, _ = store.Get(ctx, "org", e.ID); got.Revision != 2 || got.Host != "db2.example" || got.Database != "orders2" {
		t.Fatalf("replaced: %+v", got)
	}

	// The admin flag and the newest check.
	if err = store.SetAdmin(ctx, "org", e.ID, true, at); err != nil {
		t.Fatal(err)
	}
	check := &LastCheck{At: at, OK: true, Version: "16.4", Target: "conn_1", Capabilities: &Capabilities{CreateDatabase: true}}
	if err = store.SaveCheck(ctx, "org", e.ID, check); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.Get(ctx, "org", e.ID); !got.AdminSet || got.LastCheck == nil || !got.LastCheck.At.Equal(at) ||
		got.LastCheck.Version != "16.4" || !got.LastCheck.Capabilities.CreateDatabase || got.LastCheck.Capabilities.CreateUser {
		t.Fatalf("admin and check: %+v %+v", got, got.LastCheck)
	}

	// What uses it, by project and environment.
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects (id, name) VALUES ('project_a', 'shop'), ('project_c', 'billing')`,
		`INSERT INTO loomplane_environments (id, name) VALUES ('env_p', 'production')`,
		`INSERT INTO loomplane_bindings (id, org_id, project_id, environment_id, name, config) VALUES
			('b1', 'org', 'project_a', 'env_p', 'fabriq', '{"composition":{"dependencies":{"postgres":{"external":"endpoint_ws"}}}}'),
			('b2', 'org', 'project_c', 'env_p', 'orders', '{"existing_resource":{"endpoint_id":"endpoint_ws"}}'),
			('b3', 'other', 'project_a', 'env_p', 'orders', '{"existing_resource":{"endpoint_id":"endpoint_ws"}}'),
			('b4', 'org', 'project_a', 'env_p', 'cache', '{}')`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	uses, err := store.Uses(ctx, "org", e.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []Use{
		{ProjectID: "project_c", Project: "billing", EnvironmentID: "env_p", Environment: "production", Resource: "orders"},
		{ProjectID: "project_a", Project: "shop", EnvironmentID: "env_p", Environment: "production", Resource: "fabriq"},
	}
	if !reflect.DeepEqual(uses, want) {
		t.Fatalf("uses:\n got %+v\nwant %+v", uses, want)
	}
}
