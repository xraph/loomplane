package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

// guardDatabase makes an empty database to migrate, and drops it after.
// It skips without LOOMPLANE_TEST_DATABASE_URL, as the integration tests do.
func guardDatabase(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL to run the schema guard tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to %s: %v", admin, err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "loomplane_guard_" + hex.EncodeToString(suffix)
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, derr := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); derr != nil {
			t.Logf("drop database %s: %v", name, derr)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// fakeCloudGroup stands in for a migration group only a cloud build runs.
func fakeCloudGroup() *migrate.Group {
	g := migrate.NewGroup("cloud_guard_test")
	noop := func(context.Context, migrate.Executor) error { return nil }
	g.MustRegister(&migrate.Migration{Name: "noop", Version: "1", Up: noop, Down: noop})
	return g
}

// guardCheck is the comparison the schema guard makes at startup, against
// the groups a build knows.
func guardCheck(dsn string, groups []*migrate.Group) error {
	return ops.CheckCompatibleWith(context.Background(), dsn, groups, migrations.OptionalVersions())
}

// A database a cloud build migrated, with a group this build doesn't have,
// is one a newer or different Loomplane migrated: the guard refuses it.
func TestGuard_UnknownGroupRefuses(t *testing.T) {
	dsn := guardDatabase(t)
	cloud := append(migrations.All(), fakeCloudGroup())
	if err := ops.Migrate(context.Background(), dsn, cloud); err != nil {
		t.Fatal(err)
	}
	// Whatever migrations.All() holds in this build, it has no
	// cloud_guard_test group.
	err := guardCheck(dsn, migrations.All())
	if !errors.Is(err, ops.ErrNewerSchema) {
		t.Fatalf("guard let through a group this build doesn't know: %v", err)
	}
}
