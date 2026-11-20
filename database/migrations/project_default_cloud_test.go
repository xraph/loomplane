//go:build integration

package migrations

import (
	"slices"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestProjectDefaultCloudMigration(t *testing.T) {
	conn, ex := scratch(t)
	ctx := t.Context()
	before := migrate.NewGroup(GroupName)
	for _, step := range Group().Migrations() {
		if step.Name != "project_default_cloud" {
			before.MustRegister(step)
		}
	}
	if _, err := migrate.NewOrchestrator(ex, before).Migrate(ctx); err != nil {
		t.Fatalf("up others: %v", err)
	}

	// Project p runs prod and staging on Cloud a and preview on Cloud b, so
	// a becomes its default. Project q's only assigned Cloud lost its grant,
	// so q gets no default.
	exec(t, conn,
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','P','p','ready','u',now(),now()),('q','o','Q','q','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','o','p','prod','production',now(),now()),('staging','o','p','staging','preview',now(),now()),('preview','o','p','preview','preview',now(),now()),('qprod','o','q','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('a','o','p','A',1,now(),now()),('b','o','p','B',1,now(),now())`,
		`INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id,target_ids,revoked) VALUES('o','a','p','{}',false),('o','b','p','{}',false),('o','a','q','{}',false)`,
		`INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('o','p','prod','a',1),('o','p','staging','a',1),('o','p','preview','b',1),('o','q','qprod','a',1)`,
		`UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE project_id='q'`,
	)
	revisions := func() []string {
		return strs(t, conn, `SELECT project_id||'/'||cloud_id||'='||revision FROM loomplane_cloud_project_grants ORDER BY 1`)
	}
	pinned := revisions()

	o := migrate.NewOrchestrator(ex, Group())
	if _, err := o.Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	defaults := func() []string {
		return strs(t, conn, `SELECT project_id||'/'||cloud_id FROM loomplane_cloud_project_grants WHERE is_default ORDER BY 1`)
	}
	if got := defaults(); !slices.Equal(got, []string{"p/a"}) {
		t.Fatalf("defaults = %v, want p/a only", got)
	}
	// Marking the default left every revision a plan pinned where it was.
	if got := revisions(); !slices.Equal(got, pinned) {
		t.Fatalf("revisions moved: %v, want %v", got, pinned)
	}
	if got := strs(t, conn, `SELECT table_name FROM information_schema.tables WHERE table_name='loomplane_cloud_assignments'`); len(got) != 0 {
		t.Fatalf("the assignment table survived: %v", got)
	}

	for _, sql := range []string{
		// One live default per project.
		`UPDATE loomplane_cloud_project_grants SET is_default=true,revision=revision+1 WHERE project_id='p' AND cloud_id='b'`,
		// A revoked grant can't be asked to be the default.
		`INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id,target_ids,revoked,is_default) VALUES('o','b','q','{}',true,true)`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	// Switching is one transaction: clear the old default, set the new.
	exec(t, conn, `BEGIN`,
		`UPDATE loomplane_cloud_project_grants SET is_default=false,revision=revision+1 WHERE project_id='p' AND cloud_id='a'`,
		`UPDATE loomplane_cloud_project_grants SET is_default=true,revision=revision+1 WHERE project_id='p' AND cloud_id='b'`,
		`COMMIT`)
	if got := defaults(); !slices.Equal(got, []string{"p/b"}) {
		t.Fatalf("after switch = %v, want p/b", got)
	}
	if got := strs(t, conn, `SELECT data->>'cloud_id' FROM loomplane_events WHERE type='project_cloud.updated' ORDER BY sequence DESC LIMIT 1`); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("last project_cloud.updated names %v, want b", got)
	}
	// Revoking the default grant, however it's written, ends the default.
	exec(t, conn, `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE project_id='p' AND cloud_id='b'`)
	if got := defaults(); len(got) != 0 {
		t.Fatalf("revoked grant stayed default: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_cloud_project_grants SET revoked=false,is_default=true,revision=revision+1 WHERE project_id='p' AND cloud_id='b'`)

	res, err := o.Rollback(ctx)
	if err != nil || len(res.Rollback) != 1 || res.Rollback[0].Name != "project_default_cloud" {
		t.Fatalf("down: %+v %v", res, err)
	}
	// Every environment of p takes p's default; q had none.
	if got := strs(t, conn, `SELECT environment_id||'='||cloud_id FROM loomplane_cloud_assignments ORDER BY 1`); !slices.Equal(got, []string{"preview=b", "prod=b", "staging=b"}) {
		t.Fatalf("assignments after down = %v", got)
	}
	if _, err = o.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := defaults(); !slices.Equal(got, []string{"p/b"}) {
		t.Fatalf("defaults after up again = %v, want p/b", got)
	}
}
