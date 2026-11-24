//go:build integration

package migrations

import "testing"

func TestWorkspaceServicesMigration(t *testing.T) {
	conn, db := scratch(t)
	// From before the migration: one endpoint a person registered, and one
	// a provider resource's Connect made, named for the resource.
	up(t, db, "catalog_endpoints")
	provider := "resource-" + "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34" + "-3"
	exec(t, conn,
		`INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_1','o','orders','postgres','db.example',5432,'require','u',now())`,
		`INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_2','o','`+provider+`','postgres','db.example',5432,'require','u',now())`,
	)
	up(t, db, "workspace_services")
	// Running the up again changes nothing.
	up(t, db, "workspace_services")
	got := strs(t, conn, `SELECT id||'|'||origin||'|'||restricted||'|'||admin_set||'|'||description||'|'||environment_label||'|'||coalesce(last_check::text,'none')
		FROM loomplane_catalog_endpoints ORDER BY id`)
	want := []string{"endpoint_1|registered|false|false|||none", "endpoint_2|provider|false|false|||none"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rows:\n got %v\nwant %v", got, want)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_catalog_endpoints SET origin = 'elsewhere' WHERE id = 'endpoint_1'`); err == nil {
		t.Fatal("an unknown origin was accepted")
	}
	exec(t, conn,
		`INSERT INTO loomplane_catalog_endpoint_grants(endpoint_id,org_id,project_id,created_by,created_at) VALUES('endpoint_1','o','project_a','u',now())`,
	)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_catalog_endpoint_grants(endpoint_id,org_id,project_id,created_by,created_at) VALUES('endpoint_1','o','project_a','u',now())`); err == nil {
		t.Fatal("the same project was granted twice")
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_catalog_endpoint_grants(endpoint_id,org_id,project_id,created_by,created_at) VALUES('endpoint_9','o','project_a','u',now())`); err == nil {
		t.Fatal("a grant of an endpoint that isn't there was accepted")
	}
	// Grants go with their endpoint.
	exec(t, conn, `DELETE FROM loomplane_catalog_endpoints WHERE id = 'endpoint_1'`)
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_catalog_endpoint_grants`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("grants outlived their endpoint: %v", got)
	}
	down(t, db, "workspace_services")
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_catalog_endpoint_grants')::text,'gone')`); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("down left the grants: %v", got)
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM information_schema.columns WHERE table_name = 'loomplane_catalog_endpoints' AND column_name IN ('origin','restricted','last_check')`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("down left columns: %v", got)
	}
}
