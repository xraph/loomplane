//go:build integration

package migrations

import "testing"

func TestDNSZoneScopeIdentityForeignKey(t *testing.T) {
	conn, ex := scratch(t)
	exec(t, conn, `CREATE TABLE loomplane_dns_zones(id text PRIMARY KEY,org_id text NOT NULL); INSERT INTO loomplane_dns_zones VALUES('zone','owner')`)
	up(t, ex, "dns_zone_scope_identity", "dns_zone_scope_identity")
	exec(t, conn, `CREATE TABLE zone_consumer(org_id text,zone_id text,FOREIGN KEY(org_id,zone_id) REFERENCES loomplane_dns_zones(org_id,id)); INSERT INTO zone_consumer VALUES('owner','zone')`)
	if _, err := conn.Exec(t.Context(), `INSERT INTO zone_consumer VALUES('foreign','zone')`); err == nil {
		t.Fatal("cross-organization zone reference accepted")
	}
	if _, err := conn.Exec(t.Context(), `DROP INDEX loomplane_dns_zones_scope_identity`); err == nil {
		t.Fatal("dependent scoped identity index was dropped")
	}
	exec(t, conn, `DROP TABLE zone_consumer`)
	down(t, ex, "dns_zone_scope_identity", "dns_zone_scope_identity")
}
