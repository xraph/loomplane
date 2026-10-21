package migrations

var retentionPoliciesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_retention_policies(
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,anchor_id TEXT NOT NULL,source_key TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),policy JSONB NOT NULL,enabled BOOLEAN NOT NULL DEFAULT false,
 created_by TEXT NOT NULL CHECK(created_by<>''),authorized_by TEXT NOT NULL CHECK(authorized_by<>''),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),next_at TIMESTAMPTZ NOT NULL,
 last_error TEXT NOT NULL DEFAULT '',last_retirement_id TEXT,
 UNIQUE(org_id,source_key),UNIQUE(org_id,project_id,id),
 FOREIGN KEY(org_id,project_id,anchor_id) REFERENCES loomplane_service_backups(org_id,project_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,project_id,last_retirement_id) REFERENCES loomplane_backup_retirements(org_id,project_id,id) ON DELETE RESTRICT)`,
	`CREATE INDEX IF NOT EXISTS loomplane_retention_policies_due ON loomplane_retention_policies(next_at,id) WHERE enabled=true`,
	`CREATE TABLE IF NOT EXISTS loomplane_retention_policy_revisions(
 org_id TEXT NOT NULL,project_id TEXT NOT NULL,policy_id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),
 policy JSONB NOT NULL,enabled BOOLEAN NOT NULL,authorized_by TEXT NOT NULL CHECK(authorized_by<>''),created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,project_id,policy_id,revision),
 FOREIGN KEY(org_id,project_id,policy_id) REFERENCES loomplane_retention_policies(org_id,project_id,id) ON DELETE RESTRICT)`,
	`ALTER TABLE loomplane_backup_retirements ADD COLUMN IF NOT EXISTS retention_policy_id TEXT, ADD COLUMN IF NOT EXISTS retention_revision INTEGER`,
	`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_backup_retirements'::regclass AND conname='loomplane_retirement_policy_reference') THEN
 ALTER TABLE loomplane_backup_retirements ADD CONSTRAINT loomplane_retirement_policy_reference CHECK((retention_policy_id IS NULL)=(retention_revision IS NULL)),
 ADD CONSTRAINT loomplane_retirement_policy_revision FOREIGN KEY(org_id,project_id,retention_policy_id,retention_revision) REFERENCES loomplane_retention_policy_revisions(org_id,project_id,policy_id,revision) ON DELETE RESTRICT;
 END IF; END $$`,
}
var retentionPoliciesDown = []string{
	// Keep the emptiness decision and destruction in one transaction. The table
	// lock also fences a policy writer that did not acquire the migration lock.
	`DO $$ BEGIN
 IF to_regclass('loomplane_retention_policies') IS NOT NULL THEN
 LOCK TABLE loomplane_retention_policies IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_retention_policies) THEN RAISE EXCEPTION 'retention policy authorization history must be preserved'; END IF;
 END IF;
 ALTER TABLE loomplane_backup_retirements DROP CONSTRAINT IF EXISTS loomplane_retirement_policy_reference,DROP CONSTRAINT IF EXISTS loomplane_retirement_policy_revision,DROP COLUMN IF EXISTS retention_policy_id,DROP COLUMN IF EXISTS retention_revision;
 DROP TABLE IF EXISTS loomplane_retention_policy_revisions;
 DROP TABLE IF EXISTS loomplane_retention_policies;
 END $$`,
}
