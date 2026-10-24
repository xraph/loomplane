package migrations

var nativeRetentionPoliciesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_native_retention_policies(
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,source_id TEXT NOT NULL,revision BIGINT NOT NULL CHECK(revision>0 AND revision<2147483647),policy JSONB NOT NULL CHECK(jsonb_typeof(policy)='object'),enabled BOOLEAN NOT NULL DEFAULT false,
 created_by TEXT NOT NULL,authorized_by TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),next_at TIMESTAMPTZ NOT NULL DEFAULT now(),last_error TEXT NOT NULL DEFAULT '',last_retirement_id TEXT,
 UNIQUE(org_id,source_id),FOREIGN KEY(org_id,source_id) REFERENCES loomplane_native_sources(org_id,id) ON DELETE RESTRICT,FOREIGN KEY(last_retirement_id) REFERENCES loomplane_native_retirements(id) ON DELETE RESTRICT)`,
	`CREATE INDEX IF NOT EXISTS loomplane_native_retention_due ON loomplane_native_retention_policies(next_at,id) WHERE enabled`,
	`CREATE TABLE IF NOT EXISTS loomplane_native_retention_revisions(org_id TEXT NOT NULL,policy_id TEXT NOT NULL REFERENCES loomplane_native_retention_policies(id) ON DELETE RESTRICT,revision BIGINT NOT NULL,policy JSONB NOT NULL,enabled BOOLEAN NOT NULL,authorized_by TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(policy_id,revision))`,
}
var nativeRetentionPoliciesDown = []string{`DO $native_retention_down$ BEGIN
 IF to_regclass('loomplane_native_retention_policies') IS NOT NULL THEN
 LOCK TABLE loomplane_native_retention_policies IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_native_retention_policies) THEN RAISE EXCEPTION 'native retention policy history is retained' USING ERRCODE='55000';END IF;
 END IF;
 DROP TABLE IF EXISTS loomplane_native_retention_revisions;
 DROP TABLE IF EXISTS loomplane_native_retention_policies;
 END $native_retention_down$`}
