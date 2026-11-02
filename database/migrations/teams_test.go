//go:build integration

package migrations

import (
	"strings"
	"testing"
)

func TestTeamsMigration(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "teams"); m.Version != "20261208000900" {
		t.Fatalf("version %q", m.Version)
	}
	conn, db := scratch(t)
	// The plan notice function as 0300 wrote it, which the down restores.
	exec(t, conn, planNoticeFunction0300)
	planNotice := `SELECT pg_get_functiondef('loomplane_plan_notice'::regproc)`
	before := strs(t, conn, planNotice)
	// Running the up again changes nothing.
	up(t, db, "create_projects", "teams", "teams")
	if got := strs(t, conn, planNotice); len(got) != 1 || !strings.Contains(got[0], "loomplane_team_members") {
		t.Fatalf("up left the plan notice without teams: %v", got)
	}

	exec(t, conn,
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj_a','o','A','a','ready','u',now(),now())`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES('team_a','o','Payments','payments','u')`,
		`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES('team_a','o','ausr_1','amem_1','u')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES('g1','team_a','o','admin',NULL,'u')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES('g2','team_a','o','developer','proj_a','u')`,
	)

	for _, sql := range []string{
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES('g3','team_a','o','owner','proj_a','u')`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES('team_x','o','','x','u')`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES('team_x','o','` + strings.Repeat("é", 81) + `','x','u')`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,description,created_by) VALUES('team_x','o','X','x','` + strings.Repeat("é", 501) + `','u')`,
		`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES('team_x','o','Other','payments','u')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES('g4','team_a','o','viewer',NULL,'u')`,
		`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES('g5','team_a','o','viewer','proj_a','u')`,
		`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES('team_a','o','ausr_1','amem_1','u')`,
		// A membership row names the authsome member it was added for.
		`INSERT INTO loomplane_team_members(team_id,org_id,user_id,added_by) VALUES('team_a','o','ausr_2','u')`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	// Another workspace may reuse the slug.
	exec(t, conn, `INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES('team_p','p','Payments','payments','u')`)

	exec(t, conn, `UPDATE loomplane_projects SET responsible_team_id='team_a'`)
	exec(t, conn, `DELETE FROM loomplane_projects WHERE id='proj_a'`)
	if got := strs(t, conn, `SELECT id FROM loomplane_team_grants ORDER BY id`); len(got) != 1 || got[0] != "g1" {
		t.Fatalf("grants after project delete: %v", got)
	}

	exec(t, conn,
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at,responsible_team_id) VALUES('proj_b','o','B','b','ready','u',now(),now(),'team_a')`,
		`DELETE FROM loomplane_teams WHERE id='team_a'`,
	)
	if got := strs(t, conn, `SELECT (SELECT count(*) FROM loomplane_team_members)||'|'||(SELECT count(*) FROM loomplane_team_grants)||'|'||(SELECT responsible_team_id IS NULL FROM loomplane_projects WHERE id='proj_b')`); len(got) != 1 || got[0] != "0|0|true" {
		t.Fatalf("after team delete: %v", got)
	}

	exec(t, conn,
		`INSERT INTO loomplane_authz_sync(org_id,user_id) VALUES('o','ausr_1')`,
	)
	first := strs(t, conn, `SELECT queued_at::text FROM loomplane_authz_sync`)
	exec(t, conn, `INSERT INTO loomplane_authz_sync(org_id,user_id) VALUES('o','ausr_1') ON CONFLICT (org_id,user_id) DO UPDATE SET queued_at=clock_timestamp()`)
	second := strs(t, conn, `SELECT queued_at::text FROM loomplane_authz_sync`)
	if len(second) != 1 || len(first) != 1 || second[0] <= first[0] {
		t.Fatalf("queue rows: %v then %v", first, second)
	}

	down(t, db, "teams")
	gone := `SELECT coalesce(to_regclass('loomplane_teams')::text,'gone')||'|'||coalesce(to_regclass('loomplane_team_members')::text,'gone')||'|'||coalesce(to_regclass('loomplane_team_grants')::text,'gone')||'|'||coalesce(to_regclass('loomplane_authz_sync')::text,'gone')`
	if got := strs(t, conn, gone); len(got) != 1 || got[0] != "gone|gone|gone|gone" {
		t.Fatalf("down left tables: %v", got)
	}
	if got := strs(t, conn, `SELECT count(*)||'' FROM information_schema.columns WHERE table_name='loomplane_projects' AND column_name='responsible_team_id'`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("down left the column: %v", got)
	}
	if got := strs(t, conn, planNotice); len(got) != 1 || strings.Contains(got[0], "loomplane_team_members") || got[0] != before[0] {
		t.Fatalf("down didn't restore the 0300 plan notice: %v", got)
	}
	up(t, db, "teams")
}
