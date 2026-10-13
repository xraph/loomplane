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
	"github.com/xraph/loomplane/internal/ops"
)

func TestConnections_protectActiveTargetsBuildersAndReplacementCleanup(t *testing.T) {
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
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := connections.NewPgStore(pg)
	now := time.Now().UTC()
	ids := []string{"conn_01m4va49zwe9pvk24sm9h8v0ta", "conn_01m4va49zwe9pvk24sm9h8v0tb", "conn_01m4va49zwe9pvk24sm9h8v0tc"}
	for _, id := range ids {
		if err = store.CreateConnection(ctx, &connections.Connection{ID: id, OrgID: "org_1", Name: id, Cloud: "server", Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		`INSERT INTO loomplane_projects (id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES ('proj_1','org_1','Test','test','active','usr_1',now(),now())`,
		`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env_1','org_1','proj_1','production','production',now(),now())`,
		`INSERT INTO loomplane_manifests (id,org_id,project_id,body,digest,revision,created_by,created_at) VALUES ('manifest_1','org_1','proj_1','version: 1','digest',1,'usr_1',now())`,
		`INSERT INTO loomplane_plans (id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at,result) VALUES ('plan_1','org_1','proj_1','env_1','production','ready','manifest_1',1,'usr_1',now(),'{"actions":[{"connection_id":"` + ids[0] + `","service":{"build_target":{"builder":"` + ids[1] + `"}}}]}')`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids[:2] {
		if err = store.DeleteConnection(ctx, "org_1", id); !errors.Is(err, connections.ErrInUse) {
			t.Fatalf("deleted active reference %s: %v", id, err)
		}
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_plans SET status='rejected' WHERE id='plan_1'`); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteConnection(ctx, "org_1", ids[1]); err != nil {
		t.Fatalf("rejected plan retained builder: %v", err)
	}
	// A second database connection cannot delete a target while a reviewed
	// plan is committing its reference. A rollback retains neither use.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_plans SET status='ready', result=$1 WHERE id='plan_1'`, `{"actions":[{"connection_id":"`+ids[0]+`"}]}`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	err = store.DeleteConnection(blocked, "org_1", ids[0])
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		_ = tx.Rollback(ctx)
		t.Fatalf("connection delete did not wait for the uncommitted plan: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ids[0], ids[2]} {
		if _, err = store.GetConnection(ctx, "org_1", id); err != nil {
			t.Fatalf("connection missing after canceled deletion: %s %v", id, err)
		}
	}
	var uses int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE connection_id=$1`, ids[0]).Scan(&uses); err != nil || uses != 0 {
		t.Fatalf("rolled back use: %d %v", uses, err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_bindings (id,org_id,project_id,environment_id,subject,name,provider,engine,connection_id,created_at,updated_at,replaced)
		VALUES ('bind_1','org_1','proj_1','env_1','service','api','server','container',$1,now(),now(),$2)`, ids[0], `[{"connection_id":"`+ids[2]+`"}]`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ids[0], ids[2]} {
		if err = store.DeleteConnection(ctx, "org_1", id); !errors.Is(err, connections.ErrInUse) {
			t.Fatalf("deleted bound reference %s: %v", id, err)
		}
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_bindings SET replaced='[]' WHERE id='bind_1'`); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteConnection(ctx, "org_1", ids[2]); err != nil {
		t.Fatalf("cleaned replacement retained connection: %v", err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_bindings WHERE id='bind_1'`); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteConnection(ctx, "org_1", ids[0]); err != nil {
		t.Fatal(err)
	}
}
