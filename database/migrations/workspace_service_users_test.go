//go:build integration

package migrations

import "testing"

func TestWorkspaceServiceUsersMigration(t *testing.T) {
	conn, db := scratch(t)
	up(t, db, "catalog_endpoints")
	up(t, db, "workspace_services")
	up(t, db, "workspace_service_users")
	up(t, db, "workspace_service_users")
	exec(t, conn,
		`INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_1','o','orders','postgres','db.example',5432,'require','u',now())`,
		`INSERT INTO loomplane_catalog_endpoint_users(endpoint_id,org_id,name,database_name,project_id,service,created_at) VALUES('endpoint_1','o','lp_shop_api','api','p','api',now())`,
	)
	if got := strs(t, conn, `SELECT name||'|'||created_database||'|'||cardinality(environments)||'|'||coalesce(revoked_at::text,'in use') FROM loomplane_catalog_endpoint_users`); len(got) != 1 || got[0] != "lp_shop_api|false|0|in use" {
		t.Fatalf("rows: %v", got)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_catalog_endpoint_users(endpoint_id,org_id,name,database_name,project_id,service,created_at) VALUES('endpoint_1','o','lp_shop_api','other','p','api',now())`); err == nil {
		t.Fatal("the same user was recorded twice")
	}
	// Users go with their endpoint.
	exec(t, conn, `DELETE FROM loomplane_catalog_endpoints WHERE id = 'endpoint_1'`)
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_catalog_endpoint_users`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("users outlived their endpoint: %v", got)
	}
	down(t, db, "workspace_service_users")
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_catalog_endpoint_users')::text,'gone')`); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("down left the table: %v", got)
	}
}
