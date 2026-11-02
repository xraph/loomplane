package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A saved capacity policy or a moved expansion appends cloud_capacity.updated
// to the event log. The payload names what changed and nothing else: no
// policy body, no approver and no saved authority. An update that leaves the
// payload as it was appends nothing, so recording an approval's principal
// after its status change doesn't announce the same revision twice.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_cloud_capacity_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	payload JSONB;
	previous JSONB;
BEGIN
	IF TG_TABLE_NAME='loomplane_cloud_capacity_policies' THEN
		payload:=jsonb_build_object('org_id',NEW.org_id,'cloud_id',NEW.cloud_id,'kind','policy','entity_id',NEW.cloud_id,'revision',NEW.revision);
		IF TG_OP='UPDATE' THEN
			previous:=jsonb_build_object('org_id',OLD.org_id,'cloud_id',OLD.cloud_id,'kind','policy','entity_id',OLD.cloud_id,'revision',OLD.revision);
		END IF;
	ELSE
		payload:=jsonb_build_object('org_id',NEW.org_id,'cloud_id',NEW.cloud_id,'kind','expansion','entity_id',NEW.id,'revision',NEW.revision,'status',NEW.status);
		IF TG_OP='UPDATE' THEN
			previous:=jsonb_build_object('org_id',OLD.org_id,'cloud_id',OLD.cloud_id,'kind','expansion','entity_id',OLD.id,'revision',OLD.revision,'status',OLD.status);
		END IF;
	END IF;
	IF previous IS NOT DISTINCT FROM payload THEN RETURN NULL; END IF;

	PERFORM loomplane_append_event(NEW.org_id,'','cloud_capacity.updated',NEW.cloud_id,payload);
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_capacity_event_trigger: %w", err)
			}

			for _, table := range []string{"loomplane_cloud_capacity_policies", "loomplane_cloud_expansions"} {
				_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_capacity_event
AFTER INSERT OR UPDATE ON `+table+`
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_capacity_event_trigger()`)
				if err != nil {
					return fmt.Errorf("create trigger loomplane_cloud_capacity_event on %s: %w", table, err)
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, table := range []string{"loomplane_cloud_expansions", "loomplane_cloud_capacity_policies"} {
				_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_cloud_capacity_event ON `+table)
				if err != nil {
					return fmt.Errorf("drop trigger loomplane_cloud_capacity_event on %s: %w", table, err)
				}
			}

			_, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_cloud_capacity_event_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_cloud_capacity_event_trigger: %w", err)
			}
			return nil
		},
	)
}
