package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_federation_installations").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
				Column("profile", migrate.JSON, migrate.NotNull()).
				Column("recorded_by", migrate.Text, migrate.NotNull(), migrate.Check("recorded_by<>''")).
				Column("checked_at", migrate.Timestamp, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "destination_id").
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_installation_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='agent' AND owner_id='installation:'||OLD.destination_id;
	ELSE
		INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'agent','installation:'||NEW.destination_id)
		ON CONFLICT DO NOTHING;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_installation_reference_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_installation_reference
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_installations
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_installation_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_installation_reference: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
	SELECT org_id,project_id,environment_id,destination_id,'agent','installation:'||destination_id
	FROM loomplane_federation_installations
	ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill installation destination references: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DELETE FROM loomplane_federation_destination_refs
	WHERE kind='agent' AND owner_id='installation:'||destination_id
		AND (org_id,destination_id) IN (SELECT org_id,destination_id FROM loomplane_federation_installations)`)
			if err != nil {
				return fmt.Errorf("delete installation destination references: %w", err)
			}

			err = db.NewDropTable("loomplane_federation_installations").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_installation_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_installation_reference_trigger: %w", err)
			}
			return nil
		},
	)
}
