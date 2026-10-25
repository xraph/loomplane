package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	run := func(stmts []string) migrate.Func {
		return func(ctx context.Context, db *migrate.DB) error {
			for i, s := range stmts {
				if _, err := db.Exec(ctx, s); err != nil {
					return fmt.Errorf("canary_releases statement %d: %w", i+1, err)
				}
			}
			return nil
		}
	}
	App.MustRegister(migrate.Define("20261104000100", "canary_releases", run(canaryReleasesUp), run(canaryReleasesDown)))
}

// A canary keeps its state on the deployment it ramps (status, the step it
// is on, the candidate's weight and each service's baseline and candidate
// revisions) and a verdict per judged window in loomplane_canary_verdicts.
// Verdict IDs are "<deployment>/<action>/<step>/<attempt>", so a retried
// judgement writes the row it already wrote. Each verdict row is a realtime
// canary_verdict event. Deployment workflow build deployment-v4 ramps
// canaries, so its runs are accepted alongside v2 and v3.
var canaryReleasesUp = []string{
	`ALTER TABLE loomplane_deployments ADD COLUMN IF NOT EXISTS canary JSONB`,
	`CREATE TABLE IF NOT EXISTS loomplane_canary_verdicts (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL,
 deployment_id TEXT NOT NULL REFERENCES loomplane_deployments(id) ON DELETE CASCADE,
 action_id TEXT NOT NULL, service TEXT NOT NULL,
 step INT NOT NULL CHECK (step >= 0), attempt INT NOT NULL CHECK (attempt >= 0),
 weight INT NOT NULL CHECK (weight BETWEEN 0 AND 100),
 verdict TEXT NOT NULL CHECK (verdict IN ('pass','fail','inconclusive')), reason TEXT NOT NULL,
 baseline JSONB NOT NULL, candidate JSONB NOT NULL, thresholds JSONB NOT NULL,
 window_start TIMESTAMPTZ NOT NULL, window_end TIMESTAMPTZ NOT NULL, decided_at TIMESTAMPTZ NOT NULL,
 UNIQUE (deployment_id, action_id, step, attempt))`,
	`CREATE INDEX IF NOT EXISTS loomplane_canary_verdicts_deployment ON loomplane_canary_verdicts (org_id, deployment_id, step, attempt)`,
	`CREATE OR REPLACE TRIGGER loomplane_state_event AFTER INSERT OR UPDATE OR DELETE ON loomplane_canary_verdicts FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger('canary_verdict')`,
	`DO $$ DECLARE name TEXT; BEGIN
 FOR name IN SELECT conname FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND contype='c'
  AND conname<>'loomplane_deployment_workflow_runs_build' AND pg_get_constraintdef(oid) LIKE '%build_id%'
 LOOP EXECUTE format('ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT %I',name); END LOOP;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND conname='loomplane_deployment_workflow_runs_build') THEN
  ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4'), false));
 END IF; END $$`,
}

// Going down would lose verdicts and strand deployment-v4 runs that only a
// canary-aware worker can replay, so it refuses while either exists.
var canaryReleasesDown = []string{
	`DO $$ BEGIN
 IF to_regclass('loomplane_canary_verdicts') IS NOT NULL THEN
  IF EXISTS(SELECT 1 FROM loomplane_canary_verdicts) THEN RAISE EXCEPTION 'canary verdicts must be retained'; END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs WHERE request->>'build_id'='deployment-v4') THEN RAISE EXCEPTION 'deployment-v4 workflow runs must finish first'; END IF;
 END $$`,
	`DO $$ BEGIN
 IF to_regclass('loomplane_canary_verdicts') IS NOT NULL THEN DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_canary_verdicts; END IF;
 END $$`,
	`DROP TABLE IF EXISTS loomplane_canary_verdicts`,
	`ALTER TABLE loomplane_deployments DROP COLUMN IF EXISTS canary`,
	`ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT IF EXISTS loomplane_deployment_workflow_runs_build`,
	`DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND conname='loomplane_deployment_workflow_runs_check') THEN
  ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_check CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3'), false));
 END IF; END $$`,
}
