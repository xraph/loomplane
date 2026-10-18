//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudAdoptionAtomicServingComparison(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','One','one','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env','org','proj','production','production',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	store := planning.NewPgStore(db)
	now := time.Now().UTC()
	binding := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ConnectionID: "account", ExternalID: "serving", Config: json.RawMessage(`{"provider":"vercel","service":"web","future_metadata":{"keep":true}}`), CreatedAt: now, UpdatedAt: now}
	if err = store.PutBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListBindings(ctx, "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	reviewed := rows[0]
	placement := &planning.CloudPlacement{CloudID: "cloud", TargetID: "target", ConnectionID: "account"}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE loomplane_bindings SET external_id='new-serving' WHERE id='binding'`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		updated bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		ok, cerr := store.AdoptCloudPlacement(ctx, reviewed, placement, now.Add(time.Second))
		done <- result{ok, cerr}
	}()
	select {
	case got := <-done:
		t.Fatalf("CAS did not wait for concurrent row update: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.updated {
			t.Fatalf("concurrent serving replacement relabeled: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CAS did not finish")
	}
	rows, err = store.ListBindings(ctx, "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].ExternalID != "new-serving" {
		t.Fatal("concurrent resource overwritten")
	}
	var config map[string]json.RawMessage
	if err = json.Unmarshal(rows[0].Config, &config); err != nil {
		t.Fatal(err)
	}
	if config["cloud_placement"] != nil {
		t.Fatal("stale adoption persisted")
	}
	// A new review can adopt and keeps fields unknown to this server.
	ok, err := store.AdoptCloudPlacement(ctx, rows[0], placement, now.Add(2*time.Second))
	if err != nil || !ok {
		t.Fatalf("current binding adoption failed: %v %v", ok, err)
	}
	rows, err = store.ListBindings(ctx, "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(rows[0].Config, &config); err != nil {
		t.Fatal(err)
	}
	if config["cloud_placement"] == nil || config["future_metadata"] == nil || rows[0].ExternalID != "new-serving" {
		t.Fatalf("metadata adoption rewrote applied resource: %+v", rows[0])
	}
}
