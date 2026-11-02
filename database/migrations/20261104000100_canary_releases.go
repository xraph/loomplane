package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A canary keeps its state on the deployment it ramps (status, the step it
// is on, the candidate's weight and each service's baseline and candidate
// revisions) and a verdict per judged window in loomplane_canary_verdicts.
// Verdict IDs are "<deployment>/<retry>/<action>/<step>/<attempt>", so a
// repeated judgement writes the row it already wrote while a retried
// deployment judges afresh. Each verdict row is a realtime
// canary_verdict event. Deployment workflow build deployment-v4 ramps
// canaries, so its runs are accepted alongside v2 and v3.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_deployments").
				AddColumn("canary", migrate.JSON).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_canary_verdicts").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("deployment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_deployments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("retry", migrate.Integer, migrate.NotNull(), migrate.Default(0), migrate.Check("retry >= 0")).
				Column("action_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("step", migrate.Integer, migrate.NotNull(), migrate.Check("step >= 0")).
				Column("attempt", migrate.Integer, migrate.NotNull(), migrate.Check("attempt >= 0")).
				Column("weight", migrate.Integer, migrate.NotNull(), migrate.Check("weight BETWEEN 0 AND 100")).
				Column("verdict", migrate.Text, migrate.NotNull(), migrate.Check("verdict IN ('pass','fail','inconclusive')")).
				Column("reason", migrate.Text, migrate.NotNull()).
				Column("baseline_fallback", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("baseline", migrate.JSON, migrate.NotNull()).
				Column("candidate", migrate.JSON, migrate.NotNull()).
				Column("thresholds", migrate.JSON, migrate.NotNull()).
				Column("window_start", migrate.Timestamp, migrate.NotNull()).
				Column("window_end", migrate.Timestamp, migrate.NotNull()).
				Column("decided_at", migrate.Timestamp, migrate.NotNull()).
				Unique("deployment_id", "retry", "action_id", "step", "attempt").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_canary_verdicts_deployment").IfNotExists().
				On("loomplane_canary_verdicts").
				Columns("org_id", "deployment_id", "retry", "step", "attempt").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_state_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_canary_verdicts
FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger('canary_verdict')`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_state_event on loomplane_canary_verdicts: %w", err)
			}

			// The two-build check drops by what it checks, whatever name it
			// was given, and the three-build check goes on once.
			_, err = db.Exec(ctx, `DO $$ DECLARE name TEXT; BEGIN
	FOR name IN SELECT conname FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND contype='c'
		AND conname<>'loomplane_deployment_workflow_runs_build' AND pg_get_constraintdef(oid) LIKE '%build_id%'
	LOOP EXECUTE format('ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT %I',name); END LOOP;
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND conname='loomplane_deployment_workflow_runs_build') THEN
		ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4'), false));
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("accept deployment-v4 workflow runs: %w", err)
			}
			return nil
		},
		// Going down would lose verdicts and strand deployment-v4 runs that
		// only a canary-aware worker can replay, so it refuses while either
		// exists.
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$ BEGIN
	IF to_regclass('loomplane_canary_verdicts') IS NOT NULL THEN
		IF EXISTS(SELECT 1 FROM loomplane_canary_verdicts) THEN RAISE EXCEPTION 'canary verdicts must be retained'; END IF;
	END IF;
	IF EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs WHERE request->>'build_id'='deployment-v4') THEN
		RAISE EXCEPTION 'deployment-v4 workflow runs must finish first';
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check canary verdicts and deployment-v4 runs are gone: %w", err)
			}

			_, err = db.Exec(ctx, `DO $$ BEGIN
	IF to_regclass('loomplane_canary_verdicts') IS NOT NULL THEN
		DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_canary_verdicts;
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_state_event on loomplane_canary_verdicts: %w", err)
			}

			err = db.NewDropTable("loomplane_canary_verdicts").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_deployments").
				DropColumn("canary").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_deployment_workflow_runs").
				DropConstraint("loomplane_deployment_workflow_runs_build").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			// The two-build check comes back under the name Postgres first
			// gave it.
			_, err = db.Exec(ctx, `DO $$ BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND conname='loomplane_deployment_workflow_runs_request_check') THEN
		ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_request_check CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3'), false));
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("accept only deployment-v2 and v3 workflow runs: %w", err)
			}
			return nil
		},
	)
}
