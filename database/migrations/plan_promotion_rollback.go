package migrations

// A plan records its kind (release, promotion or rollback), the environment
// a promotion deploys from, and a rollback's spec. A plan source remembers
// the plan of the source environment that deployed its commit. Each
// environment also keeps the manifest revision its last release was planned
// from, which a promotion uses. Plans made before this read as releases.
var planPromotionRollbackUp = []string{
	`ALTER TABLE loomplane_plans ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'release'
		CHECK (kind IN ('release','promotion','rollback'))`,
	`ALTER TABLE loomplane_plans ADD COLUMN IF NOT EXISTS from_environment TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_plans ADD COLUMN IF NOT EXISTS from_environment_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_plans ADD COLUMN IF NOT EXISTS rollback JSONB`,
	`ALTER TABLE loomplane_plan_sources ADD COLUMN IF NOT EXISTS from_plan_id TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS loomplane_plans_rollback_target
		ON loomplane_plans (org_id, (rollback->>'target_plan_id')) WHERE kind = 'rollback'`,
	`CREATE TABLE IF NOT EXISTS loomplane_environment_manifests (
		environment_id    TEXT        PRIMARY KEY REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		org_id            TEXT        NOT NULL,
		manifest_id       TEXT        NOT NULL,
		manifest_revision INTEGER     NOT NULL,
		plan_id           TEXT        NOT NULL REFERENCES loomplane_plans (id) ON DELETE CASCADE,
		deployed_at       TIMESTAMPTZ NOT NULL
	)`,
	// Backfill: the manifest of each environment's newest succeeded deployment
	// of a non-catalog plan that isn't a Cloud sync, which is what a release
	// records from now on. A plan's Cloud sync ID lives in its result.
	`INSERT INTO loomplane_environment_manifests (environment_id, org_id, manifest_id, manifest_revision, plan_id, deployed_at)
		SELECT DISTINCT ON (d.environment_id) d.environment_id, d.org_id, p.manifest_id, p.manifest_revision, p.id, d.finished_at
		FROM loomplane_deployments d JOIN loomplane_plans p ON p.id = d.plan_id
		WHERE d.status = 'succeeded' AND d.finished_at IS NOT NULL
			AND coalesce(p.result->>'cloud_sync_id', '') = '' AND NOT p.catalog
		ORDER BY d.environment_id, d.finished_at DESC
		ON CONFLICT (environment_id) DO NOTHING`,
}

var planPromotionRollbackDown = []string{
	`DROP TABLE IF EXISTS loomplane_environment_manifests`,
	`DROP INDEX IF EXISTS loomplane_plans_rollback_target`,
	`ALTER TABLE loomplane_plan_sources DROP COLUMN IF EXISTS from_plan_id`,
	`ALTER TABLE loomplane_plans DROP COLUMN IF EXISTS rollback`,
	`ALTER TABLE loomplane_plans DROP COLUMN IF EXISTS from_environment_id`,
	`ALTER TABLE loomplane_plans DROP COLUMN IF EXISTS from_environment`,
	`ALTER TABLE loomplane_plans DROP COLUMN IF EXISTS kind`,
}
