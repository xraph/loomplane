package migrations

// An external endpoint a catalog service can connect to instead of a
// dependency Loomplane provisions. The DSN, password included, is in Vault
// under the endpoint's ID; the row holds only what isn't secret.
var catalogEndpointsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_catalog_endpoints (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		name       TEXT        NOT NULL,
		engine     TEXT        NOT NULL,
		host       TEXT        NOT NULL,
		port       INTEGER     NOT NULL CHECK (port BETWEEN 1 AND 65535),
		database   TEXT        NOT NULL DEFAULT '',
		username   TEXT        NOT NULL DEFAULT '',
		tls        TEXT        NOT NULL,
		created_by TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		revision   INTEGER     NOT NULL DEFAULT 1,
		UNIQUE (org_id, name)
	)`,
}

var catalogEndpointsDown = []string{
	`DROP TABLE IF EXISTS loomplane_catalog_endpoints`,
}
