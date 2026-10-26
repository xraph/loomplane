//go:build integration

package migrations

import "testing"

func TestCloudProfilesMigration(t *testing.T) {
	conn, db := scratch(t)
	up(t, db, "create_projects", "create_cloud_connections")
	exec(t, conn, `CREATE UNIQUE INDEX loomplane_environments_scope_id ON loomplane_environments(org_id,project_id,id)`, `CREATE UNIQUE INDEX loomplane_connections_org_id ON loomplane_cloud_connections(org_id,id)`)
	// Running the up again changes nothing, and down must permit a clean up.
	up(t, db, "create_cloud_profiles", "create_cloud_profiles")
	exec(t, conn, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','Project','project','ready','u',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','gcp','Account','project','ready','u',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('cloud','o','p','Production',1,now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_targets(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,revision,created_at,updated_at) VALUES('target','o','p','cloud','primary','Primary','cloud-run','c',1,now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('o','p','e','cloud',1)`)
	for _, sql := range []string{
		`UPDATE loomplane_cloud_profiles SET revision=3 WHERE id='cloud'`,
		`UPDATE loomplane_cloud_targets SET revision=1 WHERE id='target'`,
		`UPDATE loomplane_cloud_targets SET name='renamed',revision=2 WHERE id='target'`,
		`UPDATE loomplane_cloud_assignments SET revision=5 WHERE environment_id='e'`,
		`UPDATE loomplane_cloud_profiles SET org_id='foreign',revision=2 WHERE id='cloud'`,
		`DELETE FROM loomplane_cloud_targets WHERE id='target'`,
		`DELETE FROM loomplane_cloud_profiles WHERE id='cloud'`,
		`DELETE FROM loomplane_cloud_connections WHERE id='c'`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	exec(t, conn, `UPDATE loomplane_cloud_profiles SET name='Renamed',revision=2 WHERE id='cloud'`)
	exec(t, conn, `UPDATE loomplane_cloud_targets SET display_name='Renamed',revision=2 WHERE id='target'`)
	exec(t, conn, `UPDATE loomplane_cloud_assignments SET revision=2 WHERE environment_id='e'`)
	down(t, db, "create_cloud_profiles", "create_cloud_profiles")
	up(t, db, "create_cloud_profiles")
}
