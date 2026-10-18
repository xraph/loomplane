//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/connections"
	federationstore "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/ops"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestFederationDestinations(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := t.Context()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := federationstore.NewPgStore(pg)
	now := time.Now().UTC()
	d := &domain.Destination{ID: "d", Scope: domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}, Name: "primary", ConnectionID: "c", Ownership: domain.Imported, Identity: domain.PhysicalIdentity{Provider: "server", Account: "daemon", ResourceID: "daemon"}, Generation: 1, CreatedAt: now, UpdatedAt: now}
	if err = store.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, "foreign", "d"); !errors.Is(err, federationstore.ErrNotFound) {
		t.Fatalf("cross tenant read: %v", err)
	}
	if err = connections.NewPgStore(pg).DeleteConnection(ctx, "o", "c"); !errors.Is(err, connections.ErrInUse) {
		t.Fatalf("delete retained connection: %v", err)
	}
	a, err := store.Get(ctx, "o", "d")
	if err != nil {
		t.Fatal(err)
	}
	b := *a
	a.Generation++
	a.Quarantined = true
	if err = store.Update(ctx, a, 1); err != nil {
		t.Fatal(err)
	}
	b.Generation++
	if err = store.Update(ctx, &b, 1); !errors.Is(err, federationstore.ErrConflict) {
		t.Fatalf("stale writer overwrote quarantine: %v", err)
	}
	a.Generation++
	a.Quarantined = false
	if err = store.Update(ctx, a, 2); !errors.Is(err, federationstore.ErrConflict) {
		t.Fatalf("quarantine cleared: %v", err)
	}
	alias := *d
	alias.ID = "alias"
	alias.Name = "alias"
	if err = store.Create(ctx, &alias); err != nil {
		t.Fatal(err)
	}
	if got, getErr := store.Get(ctx, "o", "alias"); getErr != nil || got.Identity.Revision() != d.Identity.Revision() {
		t.Fatalf("physical alias: %+v %v", got, getErr)
	}
}
