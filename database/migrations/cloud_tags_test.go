//go:build integration

package migrations

import "testing"

func TestCloudTagsMigration(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, cloudConnectionsUp...)
	exec(t, conn, `CREATE UNIQUE INDEX loomplane_environments_scope_id ON loomplane_environments(org_id,project_id,id)`, `CREATE UNIQUE INDEX loomplane_connections_org_id ON loomplane_cloud_connections(org_id,id)`)
	exec(t, conn, cloudProfilesUp...)
	exec(t, conn, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','Project','project','ready','u',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','gcp','Account','project','ready','u',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('cloud','o','p','Production',1,now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_targets(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,revision,created_at,updated_at) VALUES('target','o','p','cloud','primary','Primary','cloud-run','c',1,now(),now())`)
	// Central migrations are not transactional, so every prefix must repeat.
	for i := range cloudTagsUp {
		exec(t, conn, cloudTagsUp[:i+1]...)
	}
	exec(t, conn, cloudTagsUp...)
	if got := strs(t, conn, `SELECT coalesce(array_to_string(tags,','),'') FROM loomplane_cloud_targets`); len(got) != 1 || got[0] != "" {
		t.Fatalf("existing target tags: %v", got)
	}
	if got := strs(t, conn, `SELECT coalesce(default_provider,'none') FROM loomplane_cloud_profiles`); len(got) != 1 || got[0] != "none" {
		t.Fatalf("existing Cloud gained a default: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_cloud_targets SET tags='{eu,gpu}',revision=2 WHERE id='target'`)
	exec(t, conn, `UPDATE loomplane_cloud_profiles SET default_provider='cloud-run',default_connection_id='c',default_region='us-central1',revision=2 WHERE id='cloud'`)
	for _, sql := range []string{
		`UPDATE loomplane_cloud_targets SET tags='{EU}',revision=3 WHERE id='target'`,
		`UPDATE loomplane_cloud_targets SET tags='{eu,eu}',revision=3 WHERE id='target'`,
		`UPDATE loomplane_cloud_targets SET tags=ARRAY[NULL]::TEXT[],revision=3 WHERE id='target'`,
		`UPDATE loomplane_cloud_targets SET tags=ARRAY['a','b','c','d','e','f','g','h','i','j','k','l','m','n','o','p','q'],revision=3 WHERE id='target'`,
		`UPDATE loomplane_cloud_profiles SET default_connection_id=NULL,revision=3 WHERE id='cloud'`,
		`UPDATE loomplane_cloud_profiles SET default_provider=NULL,default_connection_id=NULL,revision=3 WHERE id='cloud'`,
		`UPDATE loomplane_cloud_profiles SET default_connection_id='missing',revision=3 WHERE id='cloud'`,
		`DELETE FROM loomplane_cloud_targets WHERE id='target'; DELETE FROM loomplane_cloud_connections WHERE id='c'`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	exec(t, conn, `UPDATE loomplane_cloud_profiles SET default_provider=NULL,default_connection_id=NULL,default_region='',revision=3 WHERE id='cloud'`)
	exec(t, conn, cloudTagsDown...)
	exec(t, conn, cloudTagsDown...)
	exec(t, conn, cloudTagsUp...)
}
