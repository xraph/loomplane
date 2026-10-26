package migrations

// A Fabriq key lock is a lease on one service's keys, so a release, a
// rotation and a revocation never interleave their calls to Fabriq. It
// holds no connection: holder names the process holding it, which renews
// expires_at while it works and deletes its own row when it is done. A
// holder that dies stops renewing, and the next writer takes the lease
// over once it expires.
var fabriqKeyLocksUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_fabriq_key_locks (
		org_id         TEXT        NOT NULL,
		environment_id TEXT        NOT NULL,
		resource       TEXT        NOT NULL,
		service        TEXT        NOT NULL,
		holder         TEXT        NOT NULL,
		expires_at     TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (org_id, environment_id, resource, service)
	)`,
}

var fabriqKeyLocksDown = []string{
	`DROP TABLE IF EXISTS loomplane_fabriq_key_locks`,
}
