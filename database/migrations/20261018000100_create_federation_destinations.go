package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Destinations retain parent resources until explicit federation cleanup. This
// prevents environment deletion from losing the credentials needed to revoke access.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_environments_scope_id").IfNotExists().
				On("loomplane_environments").
				Columns("org_id", "project_id", "id").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_destinations").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("ownership", migrate.Text, migrate.NotNull(), migrate.Default("imported"),
					migrate.Check("ownership='imported'")).
				Column("identity", migrate.JSON, migrate.NotNull()).
				Column("identity_revision", migrate.Text, migrate.NotNull(), migrate.Check("identity_revision<>''")).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
				Column("capabilities", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("quarantined", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "environment_id", "name").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "connection_id").
				References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_federation_physical").IfNotExists().
				On("loomplane_federation_destinations").
				Columns("org_id", "identity_revision").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_federation_connection_refs").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull(), migrate.Check("kind IN ('cleanup','trust')")).
				Column("owner_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("kind", "owner_id", "connection_id").
				ForeignKey("org_id", "connection_id").
				References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_destination_uses_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='federation_destination' AND owner_id=OLD.id;
	ELSE
		DELETE FROM loomplane_connection_uses WHERE kind='federation_destination' AND owner_id=NEW.id;
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.connection_id,'federation_destination',NEW.id);
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_destination_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_destination_uses
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_destinations
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_destination_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_destination_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_reference_uses_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP IN ('DELETE','UPDATE') THEN
		DELETE FROM loomplane_connection_uses
		WHERE kind='federation_'||OLD.kind AND owner_id=OLD.owner_id AND connection_id=OLD.connection_id;
	END IF;

	IF TG_OP IN ('INSERT','UPDATE') THEN
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.connection_id,'federation_'||NEW.kind,NEW.owner_id);
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_reference_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_reference_uses
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_connection_refs
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_reference_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_reference_uses: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
	SELECT org_id,connection_id,'federation_destination',id
	FROM loomplane_federation_destinations
	ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill federation destination connection uses: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
	SELECT org_id,connection_id,'federation_'||kind,owner_id
	FROM loomplane_federation_connection_refs
	ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill federation reference connection uses: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropTable("loomplane_federation_connection_refs").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_federation_destinations").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_reference_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_reference_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_destination_uses_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_destination_uses_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DELETE FROM loomplane_connection_uses
	WHERE kind IN ('federation_destination','federation_cleanup','federation_trust')`)
			if err != nil {
				return fmt.Errorf("delete federation connection uses: %w", err)
			}

			return db.NewDropIndex("loomplane_environments_scope_id").IfExists().Exec(ctx)
		},
	)
}
