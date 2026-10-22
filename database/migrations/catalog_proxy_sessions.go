package migrations

// A session on a catalog service's admin, opened through Loomplane's
// proxy. Only the SHA-256 of the session's token is kept; the token itself
// is in the URL the browser holds and nowhere else. expires_at moves with
// each use: IdleTTL after last_used_at, never past the absolute lifetime
// from created_at.
var catalogProxySessionsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_catalog_proxy_sessions (
		token_hash     TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL,
		environment_id TEXT        NOT NULL,
		resource       TEXT        NOT NULL,
		kind           TEXT        NOT NULL,
		user_id        TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL,
		last_used_at   TIMESTAMPTZ NOT NULL,
		expires_at     TIMESTAMPTZ NOT NULL
	)`,
	// Revoking a resource's sessions, and pruning expired ones.
	`CREATE INDEX IF NOT EXISTS loomplane_catalog_proxy_sessions_resource
		ON loomplane_catalog_proxy_sessions (org_id, environment_id, resource)`,
	`CREATE INDEX IF NOT EXISTS loomplane_catalog_proxy_sessions_expiry
		ON loomplane_catalog_proxy_sessions (expires_at)`,
}

var catalogProxySessionsDown = []string{
	`DROP TABLE IF EXISTS loomplane_catalog_proxy_sessions`,
}
