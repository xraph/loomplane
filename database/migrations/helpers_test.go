//go:build integration

package migrations

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"
)

// scratch is an empty database of its own on the test server, dropped when
// the test ends. The connection is for the test's own statements, and the
// executor runs migrations the way the orchestrator hands them one.
func scratch(t *testing.T) (*pgx.Conn, migrate.Executor) {
	t.Helper()
	base := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("LOOMPLANE_TEST_DATABASE_URL isn't set; make test-integration sets it")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_migrations_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	dsn, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + name
	db := pgdriver.New()
	if err := db.Open(ctx, dsn.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return conn, pgmigrate.New(db)
}

// up runs each named migration's up in order. It calls the function
// directly, so there's no transaction and no applied record, and a test can
// run one twice to show a rerun changes nothing.
func up(t *testing.T, ex migrate.Executor, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := migration(t, name).Up(context.Background(), ex); err != nil {
			t.Fatalf("%s up: %v", name, err)
		}
	}
}

// down runs each named migration's down in order.
func down(t *testing.T, ex migrate.Executor, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := migration(t, name).Down(context.Background(), ex); err != nil {
			t.Fatalf("%s down: %v", name, err)
		}
	}
}

// migration finds a migration of Loomplane's by the name databases record.
func migration(t *testing.T, name string) *migrate.Migration {
	t.Helper()
	for _, g := range []*migrate.Group{Group(), NativeCredentialsGroup()} {
		for _, m := range g.Migrations() {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("no migration named %q", name)
	return nil
}

func exec(t *testing.T, conn *pgx.Conn, stmts ...string) {
	t.Helper()
	for i, s := range stmts {
		if _, err := conn.Exec(context.Background(), s); err != nil {
			t.Fatalf("statement %d: %v", i+1, err)
		}
	}
}

func strs(t *testing.T, conn *pgx.Conn, query string) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}
