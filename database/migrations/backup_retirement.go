package migrations

var backupRetirementUp = []string{
	`ALTER TABLE loomplane_service_backups DROP CONSTRAINT loomplane_service_backups_status_check`,
	`ALTER TABLE loomplane_service_backups ADD CONSTRAINT loomplane_service_backups_status_check CHECK(status IN ('queued','running','succeeded','failed','retiring','retired'))`,
	`CREATE TABLE loomplane_backup_retirements(
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,backup_id TEXT NOT NULL,
 source_key TEXT NOT NULL,witness_id TEXT NOT NULL,request_id TEXT NOT NULL,policy JSONB NOT NULL,
 object JSONB NOT NULL,witness_object JSONB NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','failed','succeeded')),
 attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt>=0),token TEXT NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,next_at TIMESTAMPTZ NOT NULL DEFAULT now(),error TEXT NOT NULL DEFAULT '',
 created_by TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),completed_at TIMESTAMPTZ,
 UNIQUE(org_id,backup_id),UNIQUE(org_id,project_id,request_id),
 CHECK(backup_id<>witness_id),
 FOREIGN KEY(org_id,project_id,backup_id) REFERENCES loomplane_service_backups(org_id,project_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,project_id,witness_id) REFERENCES loomplane_service_backups(org_id,project_id,id) ON DELETE RESTRICT)`,
	`CREATE UNIQUE INDEX loomplane_backup_retirements_source_active ON loomplane_backup_retirements(org_id,source_key) WHERE status<>'succeeded'`,
	`CREATE INDEX loomplane_backup_retirements_pending ON loomplane_backup_retirements(next_at,created_at) WHERE status<>'succeeded'`,
}

var backupRetirementDown = []string{
	`DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_service_backups WHERE status IN ('retiring','retired')) THEN RAISE EXCEPTION 'backup retirement history must be preserved'; END IF; END $$`,
	`DROP TABLE loomplane_backup_retirements`,
	`ALTER TABLE loomplane_service_backups DROP CONSTRAINT loomplane_service_backups_status_check`,
	`ALTER TABLE loomplane_service_backups ADD CONSTRAINT loomplane_service_backups_status_check CHECK(status IN ('queued','running','succeeded','failed'))`,
}
