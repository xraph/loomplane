package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_bindings").
				AddColumn("owned_resources", migrate.JSON, migrate.NotNull(), migrate.Default("{}"),
					migrate.Check("jsonb_typeof(owned_resources)='object' AND (owned_resources='{}'::jsonb OR physical_id<>'')")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_binding_ownership_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF OLD.owned_resources<>'{}'::jsonb AND NOT NEW.owned_resources @> OLD.owned_resources
		AND (NEW.provider,NEW.region,NEW.external_id,NEW.physical_id)
			IS NOT DISTINCT FROM (OLD.provider,OLD.region,OLD.external_id,OLD.physical_id) THEN
		RAISE EXCEPTION 'binding update discarded existing resource ownership' USING ERRCODE='23514';
	END IF;

	IF OLD.owned_resources<>'{}'::jsonb
		AND (NEW.provider,NEW.region,NEW.external_id,NEW.physical_id)
			IS DISTINCT FROM (OLD.provider,OLD.region,OLD.external_id,OLD.physical_id)
		AND NOT EXISTS (
			SELECT 1 FROM jsonb_array_elements(NEW.replaced) AS prior
			WHERE COALESCE(prior->>'provider','')=OLD.provider
				AND COALESCE(prior->>'connection_id','')=OLD.connection_id
				AND COALESCE(prior->>'region','')=OLD.region
				AND COALESCE(prior->>'external_id','')=OLD.external_id
				AND COALESCE(prior->>'physical_id','')=OLD.physical_id
				AND prior->'owned_resources'=OLD.owned_resources
		) THEN
		RAISE EXCEPTION 'binding replacement omitted original resource ownership' USING ERRCODE='23514';
	END IF;

	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_binding_ownership_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_binding_ownership_guard
BEFORE UPDATE ON loomplane_bindings
FOR EACH ROW EXECUTE FUNCTION loomplane_binding_ownership_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_binding_ownership_guard: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_binding_ownership_guard ON loomplane_bindings`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_binding_ownership_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_binding_ownership_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_binding_ownership_guard: %w", err)
			}

			return db.NewAlterTable("loomplane_bindings").
				DropColumn("owned_resources").IfExists().
				Exec(ctx)
		},
	)
}
