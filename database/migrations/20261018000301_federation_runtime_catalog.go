package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_federation_observers").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("agent_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("destination_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("destination_generation>0")).
				Column("active", migrate.Boolean, migrate.NotNull(), migrate.Default(true)).
				Column("state", migrate.JSON, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "agent_id").
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_federation_active_observer").IfNotExists().
				On("loomplane_federation_observers").
				Columns("org_id", "destination_id").
				Where("active").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_observer_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='INSERT' THEN
		INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'agent',NEW.agent_id);
	ELSIF TG_OP='DELETE' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='agent' AND owner_id=OLD.agent_id;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_observer_reference_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_observer_reference
	AFTER INSERT OR DELETE ON loomplane_federation_observers
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_observer_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_observer_reference: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
	SELECT org_id,project_id,environment_id,destination_id,'agent',agent_id
	FROM loomplane_federation_observers
	ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill observer destination references: %w", err)
			}

			err = db.NewCreateTable("loomplane_federation_catalog_views").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("agent_id", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("issued_at", migrate.Timestamp, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull(), migrate.Check("expires_at>issued_at")).
				ForeignKey("org_id", "agent_id").
				References("loomplane_federation_observers", "org_id", "agent_id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_federation_catalog_view_expiry").IfNotExists().
				On("loomplane_federation_catalog_views").
				Columns("org_id", "agent_id", "expires_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropTable("loomplane_federation_catalog_views").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DELETE FROM loomplane_federation_destination_refs
	WHERE kind='agent' AND (org_id,owner_id) IN (SELECT org_id,agent_id FROM loomplane_federation_observers)`)
			if err != nil {
				return fmt.Errorf("delete observer destination references: %w", err)
			}

			err = db.NewDropTable("loomplane_federation_observers").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_observer_reference_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_observer_reference_trigger: %w", err)
			}
			return nil
		},
	)
}
