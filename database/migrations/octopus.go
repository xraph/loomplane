package migrations

// The Octopus gateway Loomplane runs for an environment, and the manual
// routes people add to it. generation counts every change to what the
// gateway should serve; applied_generation is the one the syncer last
// pushed to Octopus, and state is what that sync saw. Tokens never live
// here, only in Vault.
var octopusUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_octopus_gateways (
		org_id             TEXT        NOT NULL,
		project_id         TEXT        NOT NULL,
		environment_id     TEXT        NOT NULL,
		resource           TEXT        NOT NULL,
		provider           TEXT        NOT NULL,
		namespace          TEXT        NOT NULL DEFAULT '',
		farp_hostnames     TEXT[]      NOT NULL DEFAULT '{}',
		generation         BIGINT      NOT NULL DEFAULT 1,
		applied_generation BIGINT      NOT NULL DEFAULT 0,
		state              JSONB       NOT NULL DEFAULT '{}',
		revision           BIGINT      NOT NULL DEFAULT 1,
		updated_at         TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (org_id, environment_id, resource)
	)`,
	`CREATE TABLE IF NOT EXISTS loomplane_octopus_routes (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL,
		environment_id TEXT        NOT NULL,
		resource       TEXT        NOT NULL,
		hostname       TEXT        NOT NULL DEFAULT '',
		path           TEXT        NOT NULL,
		methods        TEXT[]      NOT NULL DEFAULT '{}',
		service        TEXT        NOT NULL,
		port           INTEGER     NOT NULL CHECK (port BETWEEN 1 AND 65535),
		strip_prefix   BOOLEAN     NOT NULL DEFAULT false,
		note           TEXT        NOT NULL DEFAULT '',
		revision       BIGINT      NOT NULL DEFAULT 1,
		created_by     TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		FOREIGN KEY (org_id, environment_id, resource)
			REFERENCES loomplane_octopus_gateways ON DELETE CASCADE
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_octopus_route_path
		ON loomplane_octopus_routes (org_id, environment_id, resource, hostname, path)`,
}

// Routes first: they reference the gateways.
var octopusDown = []string{
	`DROP TABLE IF EXISTS loomplane_octopus_routes`,
	`DROP TABLE IF EXISTS loomplane_octopus_gateways`,
}
