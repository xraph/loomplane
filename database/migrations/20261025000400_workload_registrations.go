package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// workload_registrations retains application issuance and cleanup intent.
// Unknown creation outcomes keep their parents even after a successful delete.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_workload_registrations").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("destination_generation", migrate.BigInt, migrate.NotNull()).
				Column("identity_revision", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("instance_id", migrate.Text, migrate.NotNull()).
				Column("runtime_ref", migrate.Text, migrate.NotNull()).
				Column("binding_id", migrate.Text, migrate.NotNull()).
				Column("service_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("image", migrate.Text, migrate.NotNull()).
				Column("entry_id", migrate.Text, migrate.NotNull(), migrate.Unique()).
				Column("phase", migrate.Text, migrate.NotNull(),
					migrate.Check("phase IN ('pending','issuing','ready','revoking','draining')")).
				Column("settled", migrate.Boolean, migrate.NotNull(), migrate.Default(true)).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("revoke_after", migrate.Timestamp, migrate.NotNull()).
				Column("release_after", migrate.Timestamp).
				Column("next_attempt", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Check("phase<>'draining' OR (settled AND release_after IS NOT NULL)").
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "connection_id", "instance_id", "runtime_ref").
				References("loomplane_workload_controllers", "org_id", "connection_id", "instance_id", "runtime_ref").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_workload_registration_cleanup").IfNotExists().
				On("loomplane_workload_registrations").
				Columns("org_id", "next_attempt").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_workload_registration_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $reference$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='trust' AND owner_id='workload:'||OLD.id;
	ELSE
		INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'trust','workload:'||NEW.id)
		ON CONFLICT DO NOTHING;
	END IF;

	RETURN NULL;
END $reference$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_workload_registration_reference_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_workload_registration_reference
AFTER INSERT OR DELETE ON loomplane_workload_registrations
FOR EACH ROW EXECUTE FUNCTION loomplane_workload_registration_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_workload_registration_reference: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
SELECT org_id,project_id,environment_id,destination_id,'trust','workload:'||id
FROM loomplane_workload_registrations
ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill loomplane_federation_destination_refs: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $workloads_down$ BEGIN
	IF to_regclass('loomplane_workload_registrations') IS NOT NULL THEN
		LOCK TABLE loomplane_workload_registrations IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_workload_registrations) THEN
			RAISE EXCEPTION 'workload registration cleanup is incomplete' USING ERRCODE='55000';
		END IF;
		DROP TABLE loomplane_workload_registrations;
	END IF;
	DROP FUNCTION IF EXISTS loomplane_workload_registration_reference_trigger();
END $workloads_down$`)
			if err != nil {
				return fmt.Errorf("drop loomplane_workload_registrations: %w", err)
			}
			return nil
		},
	)
}
