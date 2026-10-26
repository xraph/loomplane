//go:build integration

package migrations

import "testing"

func TestDomainRegistrationsMigration(t *testing.T) {
	conn, db := scratch(t)
	up(t, db, "create_cloud_connections")
	exec(t, conn, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_connections_org_id ON loomplane_cloud_connections(org_id,id)`)
	exec(t, conn, `CREATE TABLE loomplane_connection_uses (org_id TEXT NOT NULL, connection_id TEXT NOT NULL, kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY(kind,owner_id,connection_id), FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT)`)
	// The orchestrator runs a migration in one transaction, so no part of it
	// is left behind on its own. A rerun still has to change nothing.
	up(t, db, "domain_registrations", "domain_registrations")
	exec(t, conn, `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('cf','o','cloudflare','Cloudflare','account','ready','u',now(),now())`)
	insert := func(id, status string) string {
		return `INSERT INTO loomplane_domain_registrations(id,org_id,connection_id,name,status,revision,body) VALUES('` + id + `','o','cf','acme.dev','` + status + `',1,'{}')`
	}
	uses := func() []string {
		return strs(t, conn, `SELECT owner_id FROM loomplane_connection_uses WHERE kind='domain_registration' ORDER BY owner_id`)
	}
	exec(t, conn, insert("r1", "preview"), insert("r2", "preview"))
	if got := uses(); len(got) != 0 {
		t.Fatalf("a preview holds the connection: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_domain_registrations SET status='approved' WHERE id='r1'`)
	if got := uses(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("approved: %v", got)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_domain_registrations SET status='approved' WHERE id='r2'`); err == nil {
		t.Fatal("two active registrations of one name")
	}
	if _, err := conn.Exec(t.Context(), `DELETE FROM loomplane_cloud_connections WHERE id='cf'`); err == nil {
		t.Fatal("an approved registration lost its connection")
	}
	for _, status := range []string{"registering", "zone_pending", "succeeded", "action_required", "blocked"} {
		exec(t, conn, `UPDATE loomplane_domain_registrations SET status='`+status+`' WHERE id='r1'`)
		if got := uses(); len(got) != 1 {
			t.Fatalf("%s: %v", status, got)
		}
	}
	for _, status := range []string{"failed", "price_changed", "unconfirmed", "expired"} {
		exec(t, conn, `UPDATE loomplane_domain_registrations SET status='`+status+`' WHERE id='r1'`)
		if got := uses(); len(got) != 0 {
			t.Fatalf("%s still holds the connection: %v", status, got)
		}
	}
	// A registration that ended without a domain frees the name.
	exec(t, conn, `UPDATE loomplane_domain_registrations SET status='approved' WHERE id='r2'`)
	exec(t, conn, `DELETE FROM loomplane_domain_registrations WHERE id='r2'`)
	if got := uses(); len(got) != 0 {
		t.Fatalf("deleted registration: %v", got)
	}
	exec(t, conn, insert("r3", "approved"))
	down(t, db, "domain_registrations", "domain_registrations")
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_domain_registrations')::text,'gone')`); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("down left the table: %v", got)
	}
	if got := uses(); len(got) != 0 {
		t.Fatalf("down left registration uses: %v", got)
	}
	up(t, db, "domain_registrations")
}

func TestNotificationKindsMigration(t *testing.T) {
	conn, db := scratch(t)
	exec(t, conn, `CREATE TABLE loomplane_notification_outbox (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, user_id TEXT NOT NULL, deployment_id TEXT NOT NULL, attempt INTEGER NOT NULL, payload JSONB NOT NULL)`)
	exec(t, conn, `INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload) VALUES('d:0','o','p','u','d',0,'{}')`)
	up(t, db, "notification_kinds", "notification_kinds")
	if got := strs(t, conn, `SELECT kind FROM loomplane_notification_outbox`); len(got) != 1 || got[0] != "deployment" {
		t.Fatalf("existing rows: %v", got)
	}
}

func TestZoneChallengeUsesMigration(t *testing.T) {
	conn, db := scratch(t)
	up(t, db, "create_cloud_connections")
	exec(t, conn, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_connections_org_id ON loomplane_cloud_connections(org_id,id)`)
	exec(t, conn, `CREATE TABLE loomplane_connection_uses (org_id TEXT NOT NULL, connection_id TEXT NOT NULL, kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY(kind,owner_id,connection_id), FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT)`)
	// The cloud_domains tables these migrations change, as cloud_domains
	// makes them.
	exec(t, conn,
		`CREATE TABLE loomplane_dns_zones (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL, connection_id TEXT NOT NULL DEFAULT '', revision BIGINT NOT NULL, body JSONB NOT NULL, UNIQUE(org_id,name))`,
		`CREATE TABLE loomplane_domain_routes (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, cloud_id TEXT NOT NULL, server_id TEXT NOT NULL, connection_id TEXT NOT NULL, zone_id TEXT NOT NULL, hostname TEXT NOT NULL, status TEXT NOT NULL, revision BIGINT NOT NULL, body JSONB NOT NULL)`,
		`CREATE TABLE loomplane_route_operations (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, route_id TEXT NOT NULL, server_id TEXT NOT NULL, cloud_id TEXT NOT NULL, status TEXT NOT NULL, revision BIGINT NOT NULL, generation BIGINT NOT NULL DEFAULT 0, body JSONB NOT NULL, principal JSONB NOT NULL, checkpoint JSONB NOT NULL DEFAULT '{}')`)
	// The orchestrator runs a migration in one transaction, so no part of it
	// is left behind on its own. A rerun still has to change nothing.
	up(t, db, "kubernetes_routes", "kubernetes_routes", "zone_challenge_uses", "zone_challenge_uses")
	if got := strs(t, conn, `SELECT table_name||':'||column_default FROM information_schema.columns WHERE column_name='backend' AND is_nullable='NO' ORDER BY 1`); len(got) != 2 || got[0] != "loomplane_domain_routes:'server'::text" || got[1] != "loomplane_route_operations:'server'::text" {
		t.Fatalf("backend columns: %v", got)
	}
	exec(t, conn,
		`CREATE TRIGGER loomplane_dns_zone_scope AFTER INSERT OR UPDATE ON loomplane_dns_zones FOR EACH ROW EXECUTE FUNCTION loomplane_domain_scope()`,
		`CREATE TRIGGER loomplane_domain_zone_delete BEFORE DELETE ON loomplane_dns_zones FOR EACH ROW EXECUTE FUNCTION loomplane_domain_delete_guard()`)
	for _, id := range []string{"cf", "cf-dns", "cf-other"} {
		exec(t, conn, `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('`+id+`','o','cloudflare','`+id+`','account','ready','u',now(),now())`)
	}
	uses := func() []string {
		return strs(t, conn, `SELECT kind||':'||owner_id||':'||connection_id FROM loomplane_connection_uses ORDER BY 1`)
	}
	same := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	exec(t, conn, `INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('z1','o','acme.dev','cf',1,'{}'),('z2','o','other.dev','cf-other',1,'{}')`)
	if got := uses(); !same(got, "dns_zone:z1:cf", "dns_zone:z2:cf-other") {
		t.Fatalf("zones: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_dns_zones SET body='{"challenge_connection_id":"cf-dns"}' WHERE id='z1'`)
	if got := uses(); !same(got, "dns_challenge:z1:cf-dns", "dns_zone:z1:cf", "dns_zone:z2:cf-other") {
		t.Fatalf("attached: %v", got)
	}
	if _, err := conn.Exec(t.Context(), `DELETE FROM loomplane_cloud_connections WHERE id='cf-dns'`); err == nil {
		t.Fatal("an attached challenge connection was deleted")
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_dns_zones SET body='{"challenge_connection_id":"cf-dns"}' WHERE id='z2'`); err == nil {
		t.Fatal("two zones share a challenge connection")
	}
	exec(t, conn, `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body) VALUES('r1','o','p','e','c','s','srv','z1','*.acme.dev','ready',1,'{}')`)
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_dns_zones SET body='{}' WHERE id='z1'`); err == nil {
		t.Fatal("cleared the challenge connection under a wildcard route")
	}
	exec(t, conn, `UPDATE loomplane_domain_routes SET status='removed' WHERE id='r1'`)
	exec(t, conn, `UPDATE loomplane_dns_zones SET body='{}' WHERE id='z1'`)
	if got := uses(); !same(got, "dns_zone:z1:cf", "dns_zone:z2:cf-other") {
		t.Fatalf("detached: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_dns_zones SET body='{"challenge_connection_id":"cf-dns"}' WHERE id='z2'`)
	exec(t, conn, `DELETE FROM loomplane_dns_zones WHERE id='z2'`)
	if got := uses(); !same(got, "dns_zone:z1:cf") {
		t.Fatalf("deleted zone: %v", got)
	}
	up(t, db, "zone_challenge_uses")
	if got := uses(); !same(got, "dns_zone:z1:cf") {
		t.Fatalf("rerun: %v", got)
	}
}
