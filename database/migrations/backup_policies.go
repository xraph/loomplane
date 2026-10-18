package migrations

var backupPoliciesUp = []string{`CREATE TABLE IF NOT EXISTS loomplane_backup_policies(
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,
 environment TEXT NOT NULL,resource TEXT NOT NULL,every_hours INTEGER NOT NULL CHECK(every_hours BETWEEN 1 AND 168),
 enabled BOOLEAN NOT NULL,next_at TIMESTAMPTZ NOT NULL,last_error TEXT NOT NULL DEFAULT '',created_by TEXT NOT NULL,
 UNIQUE(org_id,environment_id,resource),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE CASCADE)`,
	`CREATE INDEX IF NOT EXISTS loomplane_backup_policies_due ON loomplane_backup_policies(next_at) WHERE enabled=true`,
}
var backupPoliciesDown = []string{`DROP TABLE IF EXISTS loomplane_backup_policies`}
