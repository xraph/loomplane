//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
)

// Metrics read every service an organization runs, across its projects
// and environments, and nothing of another organization's.
func TestOrgServiceBindings_areTheOrganizationsServicesOnly(t *testing.T) {
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
	for _, row := range [][3]string{{"proj_1", "org_1", "env_1"}, {"proj_2", "org_1", "env_2"}, {"proj_3", "org_2", "env_3"}} {
		if _, err = conn.Exec(ctx, `INSERT INTO loomplane_projects (id, org_id, name, slug, status, created_by, created_at, updated_at)
			VALUES ($1, $2, $1, $1, 'active', 'usr_1', $3, $3)`, row[0], row[1], now); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, `INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at)
			VALUES ($1, $2, $3, 'production', 'production', $4, $4)`, row[2], row[1], row[0], now); err != nil {
			t.Fatal(err)
		}
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := planning.NewPgStore(pg)
	put := func(id, org, project, env string, subject planning.Subject) {
		t.Helper()
		if perr := store.PutBinding(ctx, &planning.Binding{
			ID: id, OrgID: org, ProjectID: project, EnvironmentID: env, Subject: subject, Name: id,
			Provider: "cloud-run", Engine: "container", ConnectionID: "conn_1", CreatedAt: now, UpdatedAt: now,
		}); perr != nil {
			t.Fatal(perr)
		}
	}
	put("bind_web", "org_1", "proj_1", "env_1", planning.ServiceSubject)
	put("bind_api", "org_1", "proj_2", "env_2", planning.ServiceSubject)
	put("bind_db", "org_1", "proj_1", "env_1", planning.ResourceSubject)
	put("bind_other", "org_2", "proj_3", "env_3", planning.ServiceSubject)

	got, err := store.OrgServiceBindings(ctx, "org_1")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	if len(ids) != 2 || ids[0] != "bind_api" || ids[1] != "bind_web" {
		t.Fatalf("org_1's services = %v, want [bind_api bind_web]", ids)
	}
}
