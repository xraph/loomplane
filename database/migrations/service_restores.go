package migrations

var serviceRestoresUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_service_backups_scope_id ON loomplane_service_backups(org_id,project_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_service_restores(
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,backup_id TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','deleting','cleanup_failed','deleted')),
 external_id TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',request_id TEXT NOT NULL,
 created_by TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL,expires_at TIMESTAMPTZ NOT NULL,
 verified_at TIMESTAMPTZ,duration_ms BIGINT NOT NULL DEFAULT 0,lease_until TIMESTAMPTZ,
 UNIQUE(org_id,project_id,request_id),
 FOREIGN KEY(org_id,project_id,backup_id) REFERENCES loomplane_service_backups(org_id,project_id,id) ON DELETE RESTRICT)`,
	`CREATE INDEX IF NOT EXISTS loomplane_service_restores_scope ON loomplane_service_restores(org_id,project_id,created_at DESC)`,
}
var serviceRestoresDown = []string{`DROP TABLE IF EXISTS loomplane_service_restores`}
