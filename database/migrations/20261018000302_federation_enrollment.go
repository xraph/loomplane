package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_federation_destination_org_id").IfNotExists().
				On("loomplane_federation_destinations").
				Columns("org_id", "id").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_enrollment_epochs").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("epoch", migrate.BigInt, migrate.NotNull(), migrate.Check("epoch>0")).
				PrimaryKey("org_id", "destination_id").
				ForeignKey("org_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_enrollments").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("destination_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("destination_generation>0")).
				Column("identity_revision", migrate.Text, migrate.NotNull()).
				Column("epoch", migrate.BigInt, migrate.NotNull(), migrate.Check("epoch>0")).
				Column("agent_id", migrate.Text, migrate.NotNull()).
				Column("entry_id", migrate.Text, migrate.NotNull()).
				Column("phase", migrate.Text, migrate.NotNull(),
					migrate.Check("phase IN ('pending','ready','consumed','revoking','revoked')")).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				Column("consumed_at", migrate.Timestamp).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Check("expires_at>created_at AND expires_at<=created_at+interval '5 minutes'").
				Unique("org_id", "destination_id", "epoch").
				Unique("entry_id").
				Unique("org_id", "agent_id").
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_federation_enrollment_cleanup").IfNotExists().
				On("loomplane_federation_enrollments").
				Columns("org_id", "phase", "expires_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_enrollment_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='trust' AND owner_id=OLD.id;
	ELSIF NEW.phase='revoked' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=NEW.org_id AND destination_id=NEW.destination_id AND kind='trust' AND owner_id=NEW.id;
	ELSE
		INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'trust',NEW.id)
		ON CONFLICT DO NOTHING;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_enrollment_reference_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_enrollment_reference
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_enrollments
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_enrollment_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_enrollment_reference: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
	SELECT org_id,project_id,environment_id,destination_id,'trust',id
	FROM loomplane_federation_enrollments
	WHERE phase<>'revoked'
	ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill enrollment destination references: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DELETE FROM loomplane_federation_destination_refs
	WHERE kind='trust' AND (org_id,owner_id) IN (SELECT org_id,id FROM loomplane_federation_enrollments)`)
			if err != nil {
				return fmt.Errorf("delete enrollment destination references: %w", err)
			}

			err = db.NewDropTable("loomplane_federation_enrollments").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_federation_enrollment_epochs").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_enrollment_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_enrollment_reference_trigger: %w", err)
			}
			return nil
		},
	)
}
