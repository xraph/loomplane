package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A frontend intent is the approval of one plan's frontend demand. It stays
// immutable, but it goes with its plan: deleting a project or environment
// deletes its plans, and an intent that restricted that kept a project with
// any approved frontend plan forever, whatever its frontends' slots said.
// The guard still refuses an update, and a delete of an intent whose plan
// is still there.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_frontend_intent_retained() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	IF TG_OP = 'DELETE' AND NOT EXISTS(SELECT 1 FROM loomplane_plans WHERE id = OLD.plan_id) THEN
		RETURN OLD;
	END IF;
	RAISE EXCEPTION 'capacity approval and placement records are immutable' USING ERRCODE='23514';
END $$`); err != nil {
				return fmt.Errorf("create function loomplane_frontend_intent_retained: %w", err)
			}
			if _, err := db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_frontend_intent_immutable
BEFORE UPDATE OR DELETE ON loomplane_frontend_intents
FOR EACH ROW EXECUTE FUNCTION loomplane_frontend_intent_retained()`); err != nil {
				return fmt.Errorf("replace trigger loomplane_frontend_intent_immutable: %w", err)
			}
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_frontend_intents
	DROP CONSTRAINT IF EXISTS loomplane_frontend_intents_plan_id_fkey,
	ADD CONSTRAINT loomplane_frontend_intents_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES loomplane_plans(id) ON DELETE CASCADE`); err != nil {
				return fmt.Errorf("cascade loomplane_frontend_intents_plan_id_fkey: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_frontend_intents
	DROP CONSTRAINT IF EXISTS loomplane_frontend_intents_plan_id_fkey,
	ADD CONSTRAINT loomplane_frontend_intents_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES loomplane_plans(id) ON DELETE RESTRICT`); err != nil {
				return fmt.Errorf("restrict loomplane_frontend_intents_plan_id_fkey: %w", err)
			}
			if _, err := db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_frontend_intent_immutable
BEFORE UPDATE OR DELETE ON loomplane_frontend_intents
FOR EACH ROW EXECUTE FUNCTION loomplane_capacity_saved_immutable()`); err != nil {
				return fmt.Errorf("restore trigger loomplane_frontend_intent_immutable: %w", err)
			}
			if _, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_frontend_intent_retained()`); err != nil {
				return fmt.Errorf("drop function loomplane_frontend_intent_retained: %w", err)
			}
			return nil
		},
	)
}
