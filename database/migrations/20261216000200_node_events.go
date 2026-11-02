package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A new or changed node appends node.updated to the event log. The payload
// leaves out last_seen_at and usage, so a heartbeat that only touches those
// appends nothing. A node whose payload is unchanged appends nothing either.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_node_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	payload JSONB;
	previous JSONB;
BEGIN
	payload:=jsonb_build_object('id',NEW.id,'name',NEW.name,'status',NEW.status,'version',NEW.version,'endpoints',NEW.endpoints,'ingress_mode',NEW.ingress_mode,'labels',NEW.labels,'role',NEW.role,'identity_generation',NEW.identity_generation);
	IF TG_OP='UPDATE' THEN
		previous:=jsonb_build_object('id',OLD.id,'name',OLD.name,'status',OLD.status,'version',OLD.version,'endpoints',OLD.endpoints,'ingress_mode',OLD.ingress_mode,'labels',OLD.labels,'role',OLD.role,'identity_generation',OLD.identity_generation);
	END IF;
	IF previous IS NOT DISTINCT FROM payload THEN RETURN NULL; END IF;

	PERFORM loomplane_append_event(NEW.org_id,'','node.updated',NEW.id,payload);
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_node_event_trigger: %w", err)
			}
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_node_event
AFTER INSERT OR UPDATE ON loomplane_nodes
FOR EACH ROW EXECUTE FUNCTION loomplane_node_event_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_node_event: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_node_event ON loomplane_nodes`); err != nil {
				return fmt.Errorf("drop trigger loomplane_node_event: %w", err)
			}
			if _, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_node_event_trigger()`); err != nil {
				return fmt.Errorf("drop function loomplane_node_event_trigger: %w", err)
			}
			return nil
		},
	)
}
