//go:build integration

package migrations

import "testing"

func TestFederationMigration(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, cloudConnectionsUp...)
	exec(t, conn, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_connections_org_id ON loomplane_cloud_connections(org_id,id)`)
	exec(t, conn, `CREATE TABLE loomplane_connection_uses (org_id TEXT NOT NULL, connection_id TEXT NOT NULL, kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY(kind,owner_id,connection_id), FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT)`)
	for i := range federationUp {
		exec(t, conn, federationUp[:i+1]...)
	}
	exec(t, conn, federationUp...)
	for i := range federationLifecycleUp {
		exec(t, conn, federationLifecycleUp[:i+1]...)
	}
	exec(t, conn, federationLifecycleUp...)
	for i := range federationCatalogUp {
		exec(t, conn, federationCatalogUp[:i+1]...)
	}
	exec(t, conn, federationCatalogUp...)
	exec(t, conn, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','project','project','ready','u',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`)
	exec(t, conn, `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','server','daemon','ready','u',now(),now())`)
	insert := `INSERT INTO loomplane_federation_destinations(id,org_id,project_id,environment_id,name,connection_id,identity,identity_revision,generation,created_at,updated_at) VALUES('d','o','p','e','primary','c','{}','physical',1,now(),now())`
	exec(t, conn, insert)
	if _, err := conn.Exec(t.Context(), `DELETE FROM loomplane_cloud_connections WHERE id='c'`); err == nil {
		t.Fatal("destination failed to retain its connection")
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_federation_destinations SET org_id='foreign' WHERE id='d'`); err == nil {
		t.Fatal("cross tenant reference accepted")
	}
	if _, err := conn.Exec(t.Context(), `DELETE FROM loomplane_environments WHERE id='e'`); err == nil {
		t.Fatal("environment deletion orphaned destination")
	}
	exec(t, conn, `INSERT INTO loomplane_federation_connection_refs(org_id,connection_id,kind,owner_id) VALUES('o','c','cleanup','cleanup1'),('o','c','trust','trust1')`)
	exec(t, conn, `DELETE FROM loomplane_federation_destinations WHERE id='d'`)
	if _, err := conn.Exec(t.Context(), `DELETE FROM loomplane_cloud_connections WHERE id='c'`); err == nil {
		t.Fatal("cleanup and trust failed to retain connection")
	}
	exec(t, conn, `DELETE FROM loomplane_federation_connection_refs WHERE owner_id='cleanup1'`)
	if _, err := conn.Exec(t.Context(), `DELETE FROM loomplane_cloud_connections WHERE id='c'`); err == nil {
		t.Fatal("trust failed to retain connection")
	}
	exec(t, conn, `DELETE FROM loomplane_federation_connection_refs WHERE owner_id='trust1'`)
	exec(t, conn, `DELETE FROM loomplane_cloud_connections WHERE id='c'`)
}
