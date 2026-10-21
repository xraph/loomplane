package migrations

var backupRetirementHistoryUp = []string{
	`ALTER TABLE loomplane_backup_retirements ADD COLUMN execution_by TEXT, ADD COLUMN attempt_limit INTEGER NOT NULL DEFAULT 3 CHECK(attempt_limit>=3 AND attempt_limit>=attempt)`,
	`UPDATE loomplane_backup_retirements SET execution_by=created_by`,
	`ALTER TABLE loomplane_backup_retirements ALTER COLUMN execution_by SET NOT NULL, ADD CONSTRAINT loomplane_backup_retirements_execution CHECK(execution_by<>''), ADD CONSTRAINT loomplane_backup_retirements_scope UNIQUE(org_id,project_id,id)`,
	`CREATE TABLE loomplane_backup_retirement_events(
 sequence BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 org_id TEXT NOT NULL,project_id TEXT NOT NULL,retirement_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('imported','requested','retry','claimed','failed','succeeded','expired')),
 status TEXT NOT NULL CHECK(status IN ('queued','running','failed','succeeded')),
 attempt INTEGER NOT NULL CHECK(attempt>=0),attempt_limit INTEGER NOT NULL CHECK(attempt_limit>=3),
 actor TEXT NOT NULL CHECK(actor<>''),previous_actor TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',previous_error TEXT NOT NULL DEFAULT '',request_id TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(org_id,project_id,request_id),CHECK((kind='retry')=(request_id IS NOT NULL)),
 FOREIGN KEY(org_id,project_id,retirement_id) REFERENCES loomplane_backup_retirements(org_id,project_id,id) ON DELETE RESTRICT)`,
	`CREATE INDEX loomplane_backup_retirement_events_history ON loomplane_backup_retirement_events(org_id,retirement_id,sequence DESC)`,
	// Existing records establish current state, not a reconstructed attempt log.
	`INSERT INTO loomplane_backup_retirement_events(org_id,project_id,retirement_id,kind,status,attempt,attempt_limit,actor,error)
 SELECT org_id,project_id,id,'imported',status,attempt,attempt_limit,execution_by,error FROM loomplane_backup_retirements`,
}

var backupRetirementHistoryDown = []string{
	`DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_backup_retirement_events) THEN RAISE EXCEPTION 'backup retirement execution history must be preserved'; END IF; END $$`,
	`DROP TABLE loomplane_backup_retirement_events`,
	`ALTER TABLE loomplane_backup_retirements DROP CONSTRAINT loomplane_backup_retirements_scope,DROP CONSTRAINT loomplane_backup_retirements_execution,DROP COLUMN execution_by,DROP COLUMN attempt_limit`,
}
