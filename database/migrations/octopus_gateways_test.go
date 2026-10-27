//go:build integration

package migrations

import "testing"

func TestOctopusGatewaysMigration(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "octopus_gateways"); m.Version != "20261110000100" {
		t.Fatalf("version %q", m.Version)
	}
	conn, db := scratch(t)
	// Running the up again changes nothing.
	up(t, db, "octopus_gateways", "octopus_gateways")
	exec(t, conn, `INSERT INTO loomplane_octopus_gateways(org_id,project_id,environment_id,resource,provider,updated_at) VALUES('o','p','e','octopus','server',now())`)
	if got := strs(t, conn, `SELECT generation||'|'||applied_generation||'|'||revision||'|'||cardinality(farp_hostnames)||'|'||state::text||'|'||namespace FROM loomplane_octopus_gateways`); len(got) != 1 || got[0] != "1|0|1|0|{}|" {
		t.Fatalf("gateway defaults: %v", got)
	}
	// Nobody holds a new gateway's sync lease.
	if got := strs(t, conn, `SELECT '['||sync_lease_token||']'||coalesce(sync_lease_until::text,'null') FROM loomplane_octopus_gateways`); len(got) != 1 || got[0] != "[]null" {
		t.Fatalf("lease defaults: %v", got)
	}
	// A table made before the lease columns gets them on a rerun.
	exec(t, conn, `ALTER TABLE loomplane_octopus_gateways DROP COLUMN sync_lease_token, DROP COLUMN sync_lease_until`)
	up(t, db, "octopus_gateways", "octopus_gateways")
	if got := strs(t, conn, `SELECT '['||sync_lease_token||']'||coalesce(sync_lease_until::text,'null') FROM loomplane_octopus_gateways`); len(got) != 1 || got[0] != "[]null" {
		t.Fatalf("lease after a rerun: %v", got)
	}
	insert := func(id, host, path string, port string) string {
		return `INSERT INTO loomplane_octopus_routes(id,org_id,project_id,environment_id,resource,hostname,path,service,port,created_by,created_at,updated_at) VALUES('` +
			id + `','o','p','e','octopus','` + host + `','` + path + `','svc',` + port + `,'u',now(),now())`
	}
	exec(t, conn, insert("octoroute_1", "api.acme.dev", "/auth/*", "8080"))
	for _, sql := range []string{
		insert("octoroute_2", "api.acme.dev", "/auth/*", "8080"), // same host and path
		insert("octoroute_3", "", "/x", "0"),
		insert("octoroute_4", "", "/x", "65536"),
		`INSERT INTO loomplane_octopus_routes(id,org_id,project_id,environment_id,resource,path,service,port,created_by,created_at,updated_at) VALUES('octoroute_5','o','p','missing','octopus','/x','svc',80,'u',now(),now())`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	exec(t, conn, insert("octoroute_6", "", "/auth/*", "8080"))
	exec(t, conn, `DELETE FROM loomplane_octopus_gateways`)
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_octopus_routes`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("routes survived their gateway: %v", got)
	}
	down(t, db, "octopus_gateways", "octopus_gateways")
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_octopus_routes')::text,'gone')||'|'||coalesce(to_regclass('loomplane_octopus_gateways')::text,'gone')`); len(got) != 1 || got[0] != "gone|gone" {
		t.Fatalf("down left tables: %v", got)
	}
}
