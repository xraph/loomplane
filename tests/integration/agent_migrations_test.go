//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ops"
)

// The agent libraries' tables are part of the one migration pass whether
// or not AI is on, so turning it on later needs no migration step. None of
// them needs the vector extension: weave's pgvector group runs on demand.
func TestMigrations_libraryTablesWithAIOff(t *testing.T) {
	ctx := context.Background()
	dsn := freshDatabase(t)
	for range 2 { // the second pass has nothing left to do
		if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, table := range []string{"cortex_runs", "nexus_tenants", "shield_profiles", "weave_collections"} {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('public.`+table+`') IS NOT NULL`).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s: exists %v, %v", table, exists, err)
		}
	}
	var vector int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = 'vector'`).Scan(&vector); err != nil || vector != 0 {
		t.Fatalf("vector extension rows %d, %v; the central pass must not need it", vector, err)
	}

	cfg := config.Defaults()
	cfg.Database.URL = dsn
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	if cfg.AI.Enabled {
		t.Fatal("AI is on by default")
	}
	start(t, cfg).stop()
}

// A database where 3b's pgvector group ran on demand isn't one a newer
// Loomplane migrated: the schema guard knows those versions.
func TestSchemaGuard_acceptsOptionalPgvectorRows(t *testing.T) {
	ctx := context.Background()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	vectors, err := pgvector.Migrations(1536)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, m := range vectors.Migrations() {
		names[m.Version] = m.Name
	}
	optional := migrations.OptionalVersions()
	if len(optional) == 0 {
		t.Fatal("no optional versions")
	}
	for _, gv := range optional {
		group, version, _ := strings.Cut(gv, ":")
		// The columns grove writes for every migration All() applied.
		if _, xerr := conn.Exec(ctx, `INSERT INTO grove_migrations (version, name, "group") VALUES ($1, $2, $3)`, version, names[version], group); xerr != nil {
			t.Fatal(xerr)
		}
	}

	if cerr := ops.CheckCompatible(ctx, dsn, migrations.All()); !errors.Is(cerr, ops.ErrNewerSchema) {
		t.Fatalf("CheckCompatible without the optional versions: %v, want ErrNewerSchema", cerr)
	}
	if cerr := ops.CheckCompatibleWith(ctx, dsn, migrations.All(), optional); cerr != nil {
		t.Fatalf("CheckCompatibleWith the optional versions: %v", cerr)
	}
	st, err := ops.SchemaStatusWith(ctx, dsn, migrations.All(), optional)
	if err != nil || len(st.Unknown) != 0 || len(st.Pending) != 0 {
		t.Fatalf("status %+v, %v", st, err)
	}
}
