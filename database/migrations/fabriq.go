package migrations

// The Fabriq API keys Loomplane issues, one per service that needs a
// hosted Fabriq. id is Fabriq's key id; the plaintext is in Vault, never
// here. A service has at most one active key (the one its running release
// holds) and one pending key (a rotation waiting for its next release);
// retiring keys stay valid in Fabriq until that release has written the
// service's binding.
//
// A rebuild re-creates a FalkorDB or Elasticsearch projection from
// Postgres. A resource has at most one queued or running rebuild of each
// projection; attempt names the claim running it.
var fabriqUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_fabriq_keys (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL,
		environment_id TEXT        NOT NULL,
		resource       TEXT        NOT NULL,
		service        TEXT        NOT NULL,
		tenant         TEXT        NOT NULL,
		prefix         TEXT        NOT NULL,
		state          TEXT        NOT NULL CHECK (state IN ('pending','active','retiring','revoked')),
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		revoked_at     TIMESTAMPTZ
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_fabriq_keys_active
		ON loomplane_fabriq_keys (org_id, environment_id, resource, service) WHERE state = 'active'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_fabriq_keys_pending
		ON loomplane_fabriq_keys (org_id, environment_id, resource, service) WHERE state = 'pending'`,
	`CREATE TABLE IF NOT EXISTS loomplane_fabriq_rebuilds (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL,
		environment_id TEXT        NOT NULL,
		resource       TEXT        NOT NULL,
		projection     TEXT        NOT NULL CHECK (projection IN ('graph','search')),
		request_id     TEXT        NOT NULL,
		status         TEXT        NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
		attempt        TEXT        NOT NULL DEFAULT '',
		output         TEXT        NOT NULL DEFAULT '',
		created_by     TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL,
		finished_at    TIMESTAMPTZ,
		UNIQUE (org_id, request_id)
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_fabriq_rebuilds_active
		ON loomplane_fabriq_rebuilds (org_id, environment_id, resource, projection) WHERE status IN ('queued','running')`,
	`CREATE INDEX IF NOT EXISTS loomplane_fabriq_rebuilds_resource
		ON loomplane_fabriq_rebuilds (org_id, environment_id, resource, created_at DESC)`,
}

var fabriqDown = []string{
	`DROP TABLE IF EXISTS loomplane_fabriq_rebuilds`,
	`DROP TABLE IF EXISTS loomplane_fabriq_keys`,
}
