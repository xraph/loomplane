package agenttest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
)

// DatabaseEnv names the Postgres FreshDatabase creates databases next to.
const DatabaseEnv = "LOOMPLANE_TEST_DATABASE_URL"

// PgvectorEnv names a Postgres with pgvector (make up-knowledge), which
// FreshPgvectorDatabase creates databases next to.
const PgvectorEnv = "LOOMPLANE_TEST_PGVECTOR_URL"

// FreshDatabase creates an empty database next to the one in
// LOOMPLANE_TEST_DATABASE_URL, runs every migration group Loomplane runs
// (migrations.All) on it and returns its DSN. It's dropped when the test
// ends. Without the variable the test is skipped.
func FreshDatabase(t testing.TB) string {
	t.Helper()
	return freshOn(t, DatabaseEnv, "set "+DatabaseEnv+" to run the agent's database tests")
}

// FreshPgvectorDatabase is FreshDatabase on the Postgres with pgvector in
// LOOMPLANE_TEST_PGVECTOR_URL. Without the variable the test is skipped.
func FreshPgvectorDatabase(t testing.TB) string {
	t.Helper()
	return freshOn(t, PgvectorEnv, "set "+PgvectorEnv+" (make up-knowledge) to run the knowledge tests that need pgvector")
}

func freshOn(t testing.TB, env, skip string) string {
	t.Helper()
	admin := os.Getenv(env)
	if admin == "" {
		t.Skip(skip)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("agenttest: connect to the test Postgres: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "loomplane_agent_" + hex.EncodeToString(suffix)
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("agenttest: create database: %v", err)
	}
	t.Cleanup(func() {
		if _, derr := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); derr != nil {
			t.Logf("agenttest: drop database %s: %v", name, derr)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dsn := u.String()

	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatalf("agenttest: open the fresh database: %v", err)
	}
	defer func() { _ = pg.Close() }()
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.NewOrchestrator(exec, migrations.All()...).Migrate(ctx); err != nil {
		t.Fatalf("agenttest: migrate: %v", err)
	}
	return dsn
}
