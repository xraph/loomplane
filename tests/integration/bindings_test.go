//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
)

// A binding keeps what its action ran as before it moved until that's
// removed, so the environment knows about it whatever happens to the
// deployment that moved it.
func TestBindings_keepWhatAMoveLeftToRemove(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := context.Background()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	now := time.Now().UTC()
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_projects (id, org_id, name, slug, status, created_by, created_at, updated_at)
		VALUES ('proj_1', 'org_1', 'Mono', 'mono', 'active', 'usr_1', $1, $1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at)
		VALUES ('env_1', 'org_1', 'proj_1', 'production', 'production', $1, $1)`, now); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := planning.NewPgStore(pg)

	old := planning.Binding{
		Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Region: "us-central1",
		ExternalID: "inst_old", ConnectionID: "conn_old", Config: []byte(`{"provider":"cloud-run"}`),
	}
	b := &planning.Binding{
		ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: "api", Provider: "vercel", Engine: "container", ExternalID: "prj_1", ConnectionID: "conn_v",
		CreatedAt: now, UpdatedAt: now, Replaced: []planning.Binding{old},
	}
	if err = store.PutBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	read := func() *planning.Binding {
		t.Helper()
		list, lerr := store.ListBindings(ctx, "org_1", "env_1")
		if lerr != nil || len(list) != 1 {
			t.Fatalf("bindings %+v, %v", list, lerr)
		}
		return list[0]
	}
	got := read()
	if len(got.Replaced) != 1 {
		t.Fatalf("replaced %+v", got.Replaced)
	}
	var cfg map[string]string
	if r := got.Replaced[0]; r.ExternalID != "inst_old" || r.ConnectionID != "conn_old" || r.Region != "us-central1" ||
		json.Unmarshal(r.Config, &cfg) != nil || cfg["provider"] != "cloud-run" {
		t.Fatalf("replaced %+v", r)
	}

	// Removing it clears it, and nothing else changes.
	if err = store.SetReplaced(ctx, "org_1", "bind_api", nil); err != nil {
		t.Fatal(err)
	}
	if got = read(); len(got.Replaced) != 0 || got.ExternalID != "prj_1" {
		t.Fatalf("after removing it %+v", got)
	}

	// A binding from before the column reads as replacing nothing.
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_bindings (id, org_id, project_id, environment_id, subject, name, provider, engine, created_at, updated_at)
		VALUES ('bind_web', 'org_1', 'proj_1', 'env_1', 'service', 'web', 'vercel', 'container', $1, $1)`, now); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListBindings(ctx, "org_1", "env_1")
	if err != nil || len(list) != 2 || list[1].Name != "web" || len(list[1].Replaced) != 0 {
		t.Fatalf("bindings %+v, %v", list, err)
	}
}
