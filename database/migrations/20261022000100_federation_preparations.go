package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_federation_preparations").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("destination_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("destination_generation>0")).
				Column("identity_revision", migrate.Text, migrate.NotNull()).
				Column("intent", migrate.JSON, migrate.NotNull()).
				Column("state", migrate.Text, migrate.NotNull(),
					migrate.Check("state IN ('reserved','account_pending','account_created','pod_pending','prepared')")).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
				Column("account_uid", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("pod_uid", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("recorded_by", migrate.Text, migrate.NotNull(), migrate.Check("recorded_by<>''")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Check("(state IN ('reserved','account_pending') AND account_uid='' AND pod_uid='') OR "+
					"(state IN ('account_created','pod_pending') AND account_uid<>'' AND pod_uid='') OR "+
					"(state='prepared' AND account_uid<>'' AND pod_uid<>'')").
				PrimaryKey("org_id", "id").
				Unique("org_id", "destination_id").
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_preparation_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='agent' AND owner_id='preparation:'||OLD.id;
	ELSE
		INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'agent','preparation:'||NEW.id)
		ON CONFLICT DO NOTHING;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_preparation_reference_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_preparation_reference
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_preparations
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_preparation_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_preparation_reference: %w", err)
			}
			return nil
		},
		// Ownership records must be retired before a downgrade can remove their schema.
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $preparation_down$
BEGIN
	IF to_regclass('loomplane_federation_preparations') IS NOT NULL THEN
		LOCK TABLE loomplane_federation_preparations IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_federation_preparations) THEN
			RAISE EXCEPTION 'preparation ownership is still retained' USING ERRCODE='55000';
		END IF;
		DROP TABLE loomplane_federation_preparations;
	END IF;
	DROP FUNCTION IF EXISTS loomplane_federation_preparation_reference_trigger();
END $preparation_down$`)
			if err != nil {
				return fmt.Errorf("drop federation preparations: %w", err)
			}
			return nil
		},
	)
}
