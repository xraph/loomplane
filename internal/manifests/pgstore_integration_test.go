//go:build integration

package manifests

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

func TestPgStore_checksExpectedRevisionAtInsert(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the PostgreSQL revision test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("manifest_revision_%d", time.Now().UnixNano())
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
	defer pg.Close()
	if _, err = pg.Exec(ctx, `CREATE TABLE loomplane_manifests (
		id text PRIMARY KEY, org_id text NOT NULL, project_id text NOT NULL, revision integer NOT NULL,
		body text NOT NULL, digest text NOT NULL, created_by text NOT NULL, created_at timestamptz NOT NULL,
		UNIQUE (project_id, revision))`); err != nil {
		t.Fatal(err)
	}
	store := NewPgStore(pg)
	zero, one, two := 0, 1, 2
	rev := func(id, yaml string, expected *int) *Revision {
		return &Revision{
			ID: id, OrgID: "org", ProjectID: "project", YAML: yaml, CreatedAt: time.Now(), expectedRevision: expected,
		}
	}
	if err = store.CreateRevision(ctx, rev("first", "version: 1\n", &zero)); err != nil {
		t.Fatal(err)
	}
	// Another writer commits after the editing service's last read.
	if err = store.CreateRevision(ctx, rev("other", "# Other writer\nversion: 1\n", nil)); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateRevision(ctx, rev("stale", "# Stale edit\nversion: 1\n", &one)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale insert: %v", err)
	}
	latest, err := store.CurrentRevision(ctx, "org", "project")
	if err != nil || latest.ID != "other" || latest.Revision != 2 {
		t.Fatalf("lost newer revision: %+v %v", latest, err)
	}
	if err = store.CreateRevision(ctx, rev("retry", "# Fresh edit\nversion: 1\n", &two)); err != nil {
		t.Fatal(err)
	}
}
