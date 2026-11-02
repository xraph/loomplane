package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// When a plan is approved, a trigger checks each service's frontend demand
// against the plan and copies it into loomplane_frontend_intents, with the
// actor who approved it. From then on the plan's decision fields can't
// change. loomplane_frontend_uses tracks which
// frontend slot each plan's service holds.
//
// A cloud target can't be deleted while a service binding, current or
// replaced, names it. It also can't be deleted or moved to another
// connection, provider or runtime while frontend bindings on its connection
// name no target at all, because those resolve to it implicitly.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_frontend_intents").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_plans", "id"), migrate.OnDelete(migrate.Restrict)).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("plan_revision", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("actor", migrate.JSON, migrate.NotNull()).
				PrimaryKey("org_id", "plan_id", "service").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_frontend_uses").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("slot_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("account_id", migrate.Text, migrate.NotNull()).
				Column("external_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('pending', 'accepted', 'active', 'removed', 'unused')")).
				Column("evidence", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "cloud_id", "slot_id", "plan_id").
				ForeignKey("org_id", "cloud_id", "slot_id").
				References("loomplane_frontend_slots", "org_id", "cloud_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "connection_id").
				References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("plan_id").
				References("loomplane_plans", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_frontend_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	a     JSONB;
	d     JSONB;
	actor JSONB;
BEGIN
	IF TG_OP = 'UPDATE'
		AND EXISTS (SELECT 1 FROM loomplane_frontend_intents WHERE org_id = OLD.org_id AND plan_id = OLD.id)
		AND (NEW.org_id, NEW.project_id, NEW.environment_id, NEW.revision, NEW.result, NEW.decided_by)
			IS DISTINCT FROM (OLD.org_id, OLD.project_id, OLD.environment_id, OLD.revision, OLD.result, OLD.decided_by)
	THEN
		RAISE EXCEPTION 'approved frontend demand is immutable' USING ERRCODE = '23514';
	END IF;

	IF NEW.status <> 'approved' OR (TG_OP = 'UPDATE' AND OLD.status = 'approved') THEN
		RETURN NEW;
	END IF;

	FOR a IN SELECT value FROM jsonb_array_elements(coalesce(NEW.result->'actions', '[]')) LOOP
		d := a->'service'->'frontend_demand';
		IF d IS NULL OR d = 'null'::jsonb THEN
			CONTINUE;
		END IF;

		IF coalesce(NEW.result->>'cloud_sync_id', '') <> '' THEN
			IF NOT loomplane_accepted_pool_sync(NEW) THEN
				RAISE EXCEPTION 'frontend sync requires accepted snapshot' USING ERRCODE = '23514';
			END IF;
			SELECT snapshot->'actor' INTO actor
			FROM loomplane_cloud_syncs
			WHERE org_id = NEW.org_id AND id = NEW.result->>'cloud_sync_id';
		ELSE
			actor := nullif(current_setting('loomplane.capacity_actor', true), '')::jsonb;
		END IF;

		IF actor IS NULL
			OR actor->>'OrgID' IS DISTINCT FROM NEW.org_id
			OR actor->>'UserID' IS DISTINCT FROM NEW.decided_by
			OR coalesce(actor->>'UserID', '') = ''
			OR coalesce((actor->>'ReadOnly')::boolean, true)
			OR d->>'org_id' IS DISTINCT FROM NEW.org_id
			OR d->>'project_id' IS DISTINCT FROM NEW.project_id
			OR d->>'environment_id' IS DISTINCT FROM NEW.environment_id
			OR d->>'plan_id' IS DISTINCT FROM NEW.id
			OR d->>'service' IS DISTINCT FROM a->>'name'
			OR d->>'provider' IS DISTINCT FROM a->>'provider'
			OR d->>'connection_id' IS DISTINCT FROM a->>'connection_id'
			OR coalesce(d->>'target_id', '') = ''
			OR coalesce(d->>'account_id', '') = ''
			OR a->>'subject' IS DISTINCT FROM 'service'
		THEN
			RAISE EXCEPTION 'invalid approved frontend demand' USING ERRCODE = '23514';
		END IF;

		INSERT INTO loomplane_frontend_intents (org_id, plan_id, service, plan_revision, body, actor)
		VALUES (NEW.org_id, NEW.id, a->>'name', NEW.revision, d, actor);
	END LOOP;

	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_frontend_intent_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_frontend_intent_approval
AFTER INSERT OR UPDATE ON loomplane_plans
FOR EACH ROW EXECUTE FUNCTION loomplane_frontend_intent_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_frontend_intent_approval: %w", err)
			}

			// loomplane_capacity_saved_immutable comes from cloud_capacity_intents.
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_frontend_intent_immutable
BEFORE UPDATE OR DELETE ON loomplane_frontend_intents
FOR EACH ROW EXECUTE FUNCTION loomplane_capacity_saved_immutable()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_frontend_intent_immutable: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_frontend_target_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP = 'UPDATE'
		AND (NEW.connection_id, NEW.provider, NEW.runtime) IS NOT DISTINCT FROM (OLD.connection_id, OLD.provider, OLD.runtime)
	THEN
		RETURN NEW;
	END IF;

	PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/' || OLD.org_id, 0));

	IF EXISTS (
		SELECT 1
		FROM (
			SELECT b.connection_id, b.config
			FROM loomplane_bindings b
			WHERE b.org_id = OLD.org_id AND b.subject = 'service'
			UNION ALL
			SELECT r->>'connection_id', r->'config'
			FROM loomplane_bindings b
			CROSS JOIN LATERAL jsonb_array_elements(b.replaced) r
			WHERE b.org_id = OLD.org_id AND b.subject = 'service'
		) b
		WHERE (
			TG_OP = 'DELETE'
			AND coalesce(b.config->'cloud_placement'->>'target_id', b.config->'frontend_demand'->>'target_id') = OLD.id
		) OR (
			b.connection_id = OLD.connection_id
			AND coalesce(b.config->'cloud_placement'->>'target_id', b.config->'frontend_demand'->>'target_id', '') = ''
			AND (b.config->>'workload' IN ('web-app', 'static-site') OR coalesce(b.config->>'workload', '') = '')
		)
	) THEN
		RAISE EXCEPTION 'resolve retained frontend target scope before changing this target' USING ERRCODE = '23514';
	END IF;

	IF TG_OP = 'DELETE' THEN
		RETURN OLD;
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_frontend_target_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_frontend_target_mutation
BEFORE UPDATE OR DELETE ON loomplane_cloud_targets
FOR EACH ROW EXECUTE FUNCTION loomplane_frontend_target_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_frontend_target_mutation: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			// Same rule as cloud_capacity_intents: you can't roll back past a
			// recorded approval, so clear those deliberately first.
			_, err := db.Exec(ctx, `DO $$ BEGIN
	IF EXISTS (SELECT 1 FROM loomplane_frontend_intents) THEN
		RAISE EXCEPTION 'frontend demand approvals must be retained';
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check loomplane_frontend_intents is empty: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_frontend_target_mutation ON loomplane_cloud_targets`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_frontend_target_mutation: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_frontend_target_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_frontend_target_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_frontend_intent_approval ON loomplane_plans`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_frontend_intent_approval: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_frontend_intent_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_frontend_intent_guard: %w", err)
			}

			if err := db.NewDropTable("loomplane_frontend_uses").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewDropTable("loomplane_frontend_intents").IfExists().Exec(ctx)
		},
	)
}
