package migrations

var ownedReservationsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_host_ports (
	 scope TEXT NOT NULL, port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
	 owner TEXT NOT NULL, org_id TEXT NOT NULL, project_id TEXT NOT NULL,
	 environment_id TEXT NOT NULL, connection_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	 PRIMARY KEY(scope,port), FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_host_ports_owner ON loomplane_host_ports(org_id,owner)`,
}
