//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
)

func TestBindingPhysicalIdentitySurvivesStoreReopen(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	raw, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','org','One','one','ready','user',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('environment','org','project','production','production',now(),now())`,
	} {
		if _, execErr := raw.Exec(ctx, sql); execErr != nil {
			t.Fatal(execErr)
		}
	}
	db := pgdriver.New()
	if openErr := db.Open(ctx, dsn); openErr != nil {
		t.Fatal(openErr)
	}
	now := time.Now().UTC()
	bound := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Subject: planning.ResourceSubject, Name: "database", Provider: "rds", ExternalID: "database", PhysicalID: "db-original", CreatedAt: now, UpdatedAt: now, Replaced: []planning.Binding{{ExternalID: "older", PhysicalID: "db-older"}}}
	if putErr := planning.NewPgStore(db).PutBinding(ctx, bound); putErr != nil {
		t.Fatal(putErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	reopened := pgdriver.New()
	if reopenErr := reopened.Open(ctx, dsn); reopenErr != nil {
		t.Fatal(reopenErr)
	}
	defer reopened.Close()
	st := planning.NewPgStore(reopened)
	list, err := st.ListBindings(ctx, "org", "environment")
	if err != nil || len(list) != 1 || list[0].PhysicalID != "db-original" || list[0].Replaced[0].PhysicalID != "db-older" {
		t.Fatalf("native identity was lost on reopen: %+v %v", list, err)
	}
	list[0].PhysicalID = "db-recreated"
	if putErr := st.PutBinding(ctx, list[0]); putErr != nil {
		t.Fatal(putErr)
	}
	list, err = st.ListBindings(ctx, "org", "environment")
	if err != nil || len(list) != 1 || list[0].PhysicalID != "db-recreated" {
		t.Fatalf("native identity update was not saved: %+v %v", list, err)
	}
	foreign, err := st.ListBindings(ctx, "other-org", "environment")
	if err != nil || len(foreign) != 0 {
		t.Fatal("physical identities escaped organization scope")
	}
}
