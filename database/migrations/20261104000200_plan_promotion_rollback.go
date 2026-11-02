package migrations

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/grove/migrate"
)

// A plan records its kind (release, promotion or rollback), the environment
// a promotion deploys from, and a rollback's spec. A plan source remembers
// the plan of the source environment that deployed its commit, and an
// analysis whether a promotion pinned it to one commit. Each
// environment also keeps the manifest revision its last release was planned
// from, which a promotion uses. Plans made before this read as releases.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_plans").
				AddColumn("kind", migrate.Text, migrate.NotNull(), migrate.Default("release"),
					migrate.Check("kind IN ('release','promotion','rollback')")).IfNotExists().
				AddColumn("from_environment", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("from_environment_id", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("rollback", migrate.JSON).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_plan_sources").
				AddColumn("from_plan_id", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_analyses").
				AddColumn("pinned", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_plans_rollback_target").IfNotExists().
				On("loomplane_plans").
				Columns("org_id").
				ColumnExpr("rollback->>'target_plan_id'").
				Where("kind = 'rollback'").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_environment_manifests").IfNotExists().
				Column("environment_id", migrate.Text, migrate.PrimaryKey(),
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("manifest_id", migrate.Text, migrate.NotNull()).
				Column("manifest_revision", migrate.Integer, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_plans", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("deployed_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			// Backfill: the manifest of each environment's newest succeeded
			// deployment of a non-catalog plan that isn't a Cloud sync, which is
			// what a release records from now on. A plan's Cloud sync ID lives in
			// its result.
			_, err = db.Exec(ctx, `INSERT INTO loomplane_environment_manifests (environment_id, org_id, manifest_id, manifest_revision, plan_id, deployed_at)
				SELECT DISTINCT ON (d.environment_id) d.environment_id, d.org_id, p.manifest_id, p.manifest_revision, p.id, d.finished_at
				FROM loomplane_deployments d JOIN loomplane_plans p ON p.id = d.plan_id
				WHERE d.status = 'succeeded' AND d.finished_at IS NOT NULL
					AND coalesce(p.result->>'cloud_sync_id', '') = '' AND NOT p.catalog
				ORDER BY d.environment_id, d.finished_at DESC
				ON CONFLICT (environment_id) DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill environment manifests: %w", err)
			}

			return execPlanEventFunctions(ctx, db, true)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := execPlanEventFunctions(ctx, db, false)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_environment_manifests").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropIndex("loomplane_plans_rollback_target").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_analyses").
				DropColumn("pinned").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_plan_sources").
				DropColumn("from_plan_id").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_plans").
				DropColumn("rollback").IfExists().
				DropColumn("from_environment_id").IfExists().
				DropColumn("from_environment").IfExists().
				DropColumn("kind").IfExists().
				Exec(ctx)
		},
	)
}

// execPlanEventFunctions replaces the two realtime trigger functions that
// build a plan event with planEventFunctions(leftovers).
func execPlanEventFunctions(ctx context.Context, db *migrate.DB, leftovers bool) error {
	stmts, err := planEventFunctions(leftovers)
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("replace a plan event trigger function: %w", err)
		}
	}
	return nil
}

// planEventFunctions returns the CREATE OR REPLACE FUNCTION statements of
// the two realtime trigger functions that build a plan event,
// loomplane_state_event_trigger and loomplane_parent_event_trigger, as
// persist_realtime_events defines them. With leftovers, a plan event
// defaults leftovers and artifacts to [] as it does actions and problems,
// because a result leaves them out when empty. Without, the functions are
// as persist_realtime_events defines them. It fails if either function no
// longer builds a plan event the way this expects, rather than migrate a
// database without the defaults.
func planEventFunctions(leftovers bool) ([]string, error) {
	const defaults = `'summary','{}'::jsonb)`
	out := []string{stateEventTriggerFunction, parentEventTriggerFunction}
	if !leftovers {
		return out, nil
	}
	for i, stmt := range out {
		if !strings.Contains(stmt, defaults) {
			return nil, fmt.Errorf("plan_promotion_rollback: a realtime trigger function no longer builds a plan event with %s", defaults)
		}
		out[i] = strings.ReplaceAll(stmt, defaults, `'summary','{}'::jsonb,'leftovers','[]'::jsonb,'artifacts','[]'::jsonb)`)
	}
	return out, nil
}
