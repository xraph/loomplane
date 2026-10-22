//go:build integration

package migrations

import "testing"

func TestDomainRegistrationsMigration(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, cloudConnectionsUp...)
	exec(t, conn, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_connections_org_id ON loomplane_cloud_connections(org_id,id)`)
	exec(t, conn, `CREATE TABLE loomplane_connection_uses (org_id TEXT NOT NULL, connection_id TEXT NOT NULL, kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY(kind,owner_id,connection_id), FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT)`)
	// Central migrations are not transactional, so every prefix must repeat.
	for i := range domainRegistrationsUp {
		exec(t, conn, domainRegistrationsUp[:i+1]...)
	}
	exec(t, conn, domainRegistrationsUp...)
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
	exec(t, conn, domainRegistrationsDown...)
	exec(t, conn, domainRegistrationsDown...)
	exec(t, conn, domainRegistrationsUp...)
}

func TestNotificationKindsMigration(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, notificationsUp[1])
	exec(t, conn, `INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload) VALUES('d:0','o','p','u','d',0,'{}')`)
	exec(t, conn, notificationKindsUp...)
	exec(t, conn, notificationKindsUp...)
	if got := strs(t, conn, `SELECT kind FROM loomplane_notification_outbox`); len(got) != 1 || got[0] != "deployment" {
		t.Fatalf("existing rows: %v", got)
	}
}
