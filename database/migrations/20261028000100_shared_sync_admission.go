package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_accepted_pool_sync(p loomplane_plans) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
	SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs s CROSS JOIN LATERAL jsonb_array_elements(coalesce(nullif(s.snapshot->'environments','null'),'[]')) e
		WHERE s.org_id=p.org_id AND s.id=p.result->>'cloud_sync_id' AND s.status IN ('pending','running')
			AND e->>'environment_id'=p.environment_id AND coalesce(nullif(e->>'project_id',''),s.project_id)=p.project_id
			AND e->'plan'->>'org_id'=p.org_id AND e->'plan'->>'project_id'=p.project_id AND e->'plan'->>'environment_id'=p.environment_id
			AND e->'accepted_plan'->>'id'=p.id AND p.id=s.id||'.'||p.environment_id
			AND e->'accepted_plan'->>'revision'=p.revision AND e->'plan'->>'revision'=p.revision
			AND e->'plan'->>'environment'=p.environment AND (e->'accepted_plan'->'result'->>'executable')::boolean=p.executable
			AND e->'accepted_plan'->>'manifest_id'=p.manifest_id AND (e->'accepted_plan'->>'manifest_revision')::int=p.manifest_revision
			AND e->'accepted_plan'->'result'=p.result AND e->'plan'->'actions'=p.result->'actions'
			AND EXISTS(SELECT 1 FROM loomplane_environment_sync_guards g WHERE g.org_id=p.org_id AND g.project_id=p.project_id AND g.environment_id=p.environment_id AND g.sync_id=s.id)
			AND NOT EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=p.org_id AND b.project_id=p.project_id AND b.environment_id=p.environment_id AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(e->'bindings') source WHERE source=to_jsonb(b)-'observed_at'-'drift_message')))
$$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_accepted_pool_sync: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_sync_snapshot_immutable() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	IF NEW.org_id<>OLD.org_id OR NEW.project_id<>OLD.project_id OR NEW.preview_id<>OLD.preview_id
		OR ((NEW.snapshot-'actor' IS DISTINCT FROM OLD.snapshot-'actor')
			AND EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(nullif(OLD.snapshot->'environments','null'),'[]')||coalesce(nullif(NEW.snapshot->'environments','null'),'[]')) e WHERE e->'accepted_plan' IS NOT NULL AND e->'accepted_plan'<>'null'))
	THEN
		RAISE EXCEPTION 'accepted Cloud sync snapshots are immutable' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_sync_snapshot_immutable: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_sync_snapshot_immutable
BEFORE UPDATE ON loomplane_cloud_syncs
FOR EACH ROW EXECUTE FUNCTION loomplane_sync_snapshot_immutable()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_sync_snapshot_immutable: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_sync_plan_immutable() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	IF (NEW.org_id,NEW.project_id,NEW.environment_id,NEW.environment,NEW.revision,NEW.manifest_id,NEW.manifest_revision,NEW.result,NEW.executable)
			IS DISTINCT FROM (OLD.org_id,OLD.project_id,OLD.environment_id,OLD.environment,OLD.revision,OLD.manifest_id,OLD.manifest_revision,OLD.result,OLD.executable)
		AND EXISTS(SELECT 1 FROM loomplane_cloud_syncs s CROSS JOIN LATERAL jsonb_array_elements(coalesce(nullif(s.snapshot->'environments','null'),'[]')) e WHERE s.org_id=OLD.org_id AND e->'accepted_plan'->>'id'=OLD.id)
	THEN
		RAISE EXCEPTION 'accepted Cloud participant plans are immutable' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_sync_plan_immutable: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_sync_plan_immutable
BEFORE UPDATE ON loomplane_plans
FOR EACH ROW EXECUTE FUNCTION loomplane_sync_plan_immutable()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_sync_plan_immutable: %w", err)
			}

			// Shared acceptance and ordinary approval serialize before either takes an environment lock.
			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_approval_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	IF NEW.status='approved' THEN
		PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
		PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND id=NEW.environment_id FOR UPDATE;
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_approval_guard: %w", err)
			}

			_, err = db.Exec(ctx, reservePoolPlanSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_reserve_pool_plan: %w", err)
			}
			return nil
		},
		nil,
	)
}
