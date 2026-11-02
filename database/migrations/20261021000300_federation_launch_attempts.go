package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_federation_launch_attempts").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("destination_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("destination_generation>0")).
				Column("identity_revision", migrate.Text, migrate.NotNull()).
				Column("enrollment_id", migrate.Text, migrate.NotNull()).
				Column("intent", migrate.JSON, migrate.NotNull()).
				Column("state", migrate.Text, migrate.NotNull(), migrate.Check("state IN ('pending','delivered')")).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
				Column("config_map_uid", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("recorded_by", migrate.Text, migrate.NotNull(), migrate.Check("recorded_by<>''")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Check("(state='pending' AND config_map_uid='') OR (state='delivered' AND config_map_uid<>'')").
				PrimaryKey("org_id", "id").
				Unique("org_id", "enrollment_id").
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_federation_launch_one_pending").IfNotExists().
				On("loomplane_federation_launch_attempts").
				Columns("org_id", "destination_id").
				Where("state='pending'").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_federation_launch_pending").IfNotExists().
				On("loomplane_federation_launch_attempts").
				Columns("org_id", "destination_id", "state").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_launch_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='cleanup' AND owner_id='launch:'||OLD.id;
	ELSIF NEW.state='pending' THEN
		INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'cleanup','launch:'||NEW.id)
		ON CONFLICT DO NOTHING;
	ELSE
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=NEW.org_id AND destination_id=NEW.destination_id AND kind='cleanup' AND owner_id='launch:'||NEW.id;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_launch_reference_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_launch_reference
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_launch_attempts
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_launch_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_launch_reference: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
	SELECT org_id,project_id,environment_id,destination_id,'cleanup','launch:'||id
	FROM loomplane_federation_launch_attempts
	WHERE state='pending'
	ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill launch attempt destination references: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DELETE FROM loomplane_federation_destination_refs
	WHERE kind='cleanup' AND (org_id,owner_id) IN (SELECT org_id,'launch:'||id FROM loomplane_federation_launch_attempts)`)
			if err != nil {
				return fmt.Errorf("delete launch attempt destination references: %w", err)
			}

			err = db.NewDropTable("loomplane_federation_launch_attempts").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_launch_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_launch_reference_trigger: %w", err)
			}
			return nil
		},
	)
}
