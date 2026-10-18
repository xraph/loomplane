package migrations

var serviceRecoveryUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_environments_scope_id ON loomplane_environments(org_id,project_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_service_backups(
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,
 connection_id TEXT NOT NULL,name TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',request_id TEXT NOT NULL,
 source JSONB NOT NULL,object JSONB NOT NULL DEFAULT '{}',token TEXT NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,created_by TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL,
 completed_at TIMESTAMPTZ,restored_at TIMESTAMPTZ,
 UNIQUE(org_id,project_id,request_id),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_service_backups_scope ON loomplane_service_backups(org_id,project_id,created_at DESC)`,
}
var serviceRecoveryDown = []string{`DROP TABLE IF EXISTS loomplane_service_backups`}
