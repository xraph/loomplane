//go:build integration

package integration

import (
	"context"
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
	t.Run("lifecycle references and scoped names", func(t *testing.T) {
		for _, sql := range []string{
			`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p2','o','p2','p2','ready','u',now(),now()),('p3','foreign','p3','p3','ready','u',now(),now())`,
			`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e2','o','p2','prod','production',now(),now()),('e3','foreign','p3','prod','production',now(),now())`,
			`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c2','foreign','server','c2','daemon','ready','u',now(),now())`,
		} {
			if _, err := conn.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
		for _, other := range []domain.Destination{
			{ID: "d2", Scope: domain.Scope{OrgID: "o", ProjectID: "p2", EnvironmentID: "e2"}, ConnectionID: "c"},
			{ID: "d3", Scope: domain.Scope{OrgID: "foreign", ProjectID: "p3", EnvironmentID: "e3"}, ConnectionID: "c2"},
		} {
			item := *d
			item.ID = other.ID
			item.Scope = other.Scope
			item.ConnectionID = other.ConnectionID
			if err := store.Create(ctx, &item); err != nil {
				t.Fatalf("same name across scope: %v", err)
			}
		}
		rows, listErr := store.List(ctx, d.Scope, "", 10)
		if listErr != nil || len(rows) != 2 {
			t.Fatalf("scoped list: %+v %v", rows, listErr)
		}
		if _, renameErr := store.Rename(ctx, "foreign", "d", "renamed", 2, now); !errors.Is(renameErr, federationstore.ErrNotFound) {
			t.Fatalf("foreign rename: %v", renameErr)
		}
		renamed, renameErr := store.Rename(ctx, "o", "alias", "renamed", 1, now)
		if renameErr != nil || renamed.Generation != 2 || renamed.Identity != d.Identity {
			t.Fatalf("rename: %+v %v", renamed, renameErr)
		}
		if deleteErr := store.Delete(ctx, "o", "alias", 1); !errors.Is(deleteErr, federationstore.ErrConflict) {
			t.Fatalf("stale delete: %v", deleteErr)
		}
		if _, insertErr := conn.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id) VALUES('foreign','p3','e3','d','agent','agent1')`); insertErr == nil {
			t.Fatal("foreign destination reference accepted")
		}
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id) VALUES('o','p','e','d','agent','agent1')`); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, "o", "d", 2); !errors.Is(err, federationstore.ErrInUse) {
			t.Fatalf("active agent lost destination: %v", err)
		}
		if _, err := conn.Exec(ctx, `DELETE FROM loomplane_federation_destination_refs WHERE owner_id='agent1'`); err != nil {
			t.Fatal(err)
		}
		// A concurrent reference acquisition must serialize with unregister.
		tx, beginErr := conn.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id) VALUES('o','p','e','alias','trust','trust1')`); err != nil {
			t.Fatal(err)
		}
		timeout, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		deleteErr := store.Delete(timeout, "o", "alias", 2)
		cancel()
		if deleteErr == nil {
			t.Fatal("unregister bypassed concurrent reference lock")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, "o", "alias", 2); !errors.Is(err, federationstore.ErrInUse) {
			t.Fatalf("committed trust reference ignored: %v", err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_federation_connection_refs(org_id,connection_id,kind,owner_id) VALUES('o','c','cleanup','cleanup1')`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `DELETE FROM loomplane_federation_destination_refs WHERE owner_id='trust1'`); err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			id         string
			generation int64
		}{{"d", 2}, {"alias", 2}, {"d2", 1}} {
			if err := store.Delete(ctx, "o", item.id, item.generation); err != nil {
				t.Fatal(err)
			}
		}
		if err := connections.NewPgStore(pg).DeleteConnection(ctx, "o", "c"); !errors.Is(err, connections.ErrInUse) {
			t.Fatalf("cleanup reference lost: %v", err)
		}
		if _, err := conn.Exec(ctx, `DELETE FROM loomplane_federation_connection_refs WHERE owner_id='cleanup1'`); err != nil {
			t.Fatal(err)
		}
		if err := connections.NewPgStore(pg).DeleteConnection(ctx, "o", "c"); err != nil {
			t.Fatal(err)
		}
	})
}
