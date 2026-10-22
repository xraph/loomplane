//go:build integration

package migrations

import "testing"

func TestCatalogEndpointsMigration(t *testing.T) {
	conn := scratch(t)
	// Central migrations are not transactional, so the up statements repeat.
	exec(t, conn, catalogEndpointsUp...)
	exec(t, conn, catalogEndpointsUp...)
	exec(t, conn, `INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_1','o','prod-pg','postgres','db.example',5432,'require','u',now())`)
	if got := strs(t, conn, `SELECT database||'|'||username||'|'||revision FROM loomplane_catalog_endpoints`); len(got) != 1 || got[0] != "||1" {
		t.Fatalf("defaults: %v", got)
	}
	for _, sql := range []string{
		`INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_2','o','prod-pg','postgres','db.example',5432,'require','u',now())`,
		`INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_3','o','other','postgres','db.example',0,'require','u',now())`,
		`INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_4','o','big','postgres','db.example',65536,'require','u',now())`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	exec(t, conn, `INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,tls,created_by,created_at) VALUES('endpoint_5','p','prod-pg','postgres','db.example',5432,'require','u',now())`)
	exec(t, conn, catalogEndpointsDown...)
	exec(t, conn, catalogEndpointsDown...)
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_catalog_endpoints')::text,'gone')`); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("down left the table: %v", got)
	}
}
