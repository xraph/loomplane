//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestRetentionInventoryScopesPhysicalSourceAndProtectsCopies(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	source := planning.Binding{ID: "binding", OrgID: "o", ProjectID: "p", EnvironmentID: "e", Name: "db", Subject: planning.ResourceSubject, Provider: "server", ExternalID: "instance", ConnectionID: "c"}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,created_by,created_at,completed_at)
 SELECT 'point'||n,'o','p','e','c','db','succeeded','request'||n,$1::jsonb,'u',now()-n*interval '1 day',now()-n*interval '1 day' FROM generate_series(1,6) n`, raw); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE loomplane_service_backups SET source=jsonb_set(source,'{external_id}','"replacement-instance"') WHERE id='point4'`,
		`UPDATE loomplane_service_backups SET source=jsonb_set(source,'{id}','"replacement-binding"') WHERE id='point5'`,
		`UPDATE loomplane_service_backups SET name='other-db' WHERE id='point6'`,
		`INSERT INTO loomplane_service_restores(id,org_id,project_id,backup_id,status,request_id,created_by,created_at,expires_at)
 VALUES('restore1','o','p','point1','running','restore1','u',now(),now()+interval '1 day'),('restore2','o','p','point2','cleanup_failed','restore2','u',now(),now()+interval '1 day'),('restore3','o','p','point3','deleted','restore3','u',now(),now()+interval '1 day')`,
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
	store := recovery.NewPgStore(pg)
	anchor, err := store.Get(ctx, "o", "point1")
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.RetentionInventory(ctx, anchor)
	if err != nil || len(items) != 3 {
		t.Fatalf("retention crossed a source boundary: %+v %v", items, err)
	}
	for _, item := range items {
		if item.InUse != (item.Backup.ID != "point3") {
			t.Fatalf("active or failed-cleanup copy lost protection: %+v", item)
		}
	}
	otherOrg := *anchor
	otherOrg.OrgID = "other-org"
	items, err = store.RetentionInventory(ctx, &otherOrg)
	if err != nil || len(items) != 0 {
		t.Fatalf("retention crossed organization scope: %d %v", len(items), err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,created_by,created_at)
 SELECT 'extra'||n,'o','p','e','c','db','failed','extra'||n,$1::jsonb,'u',now()-n*interval '1 day' FROM generate_series(1,1005) n`, raw); err != nil {
		t.Fatal(err)
	}
	items, err = store.RetentionInventory(ctx, anchor)
	if err != nil || len(items) != 1001 {
		t.Fatalf("inventory did not expose its truncation sentinel: %d %v", len(items), err)
	}
}
