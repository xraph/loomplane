package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_federation_deliveries").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("agent_id", migrate.Text, migrate.NotNull()).
				Column("state", migrate.JSON, migrate.NotNull()).
				PrimaryKey("org_id", "agent_id").
				ForeignKey("org_id", "agent_id").
				References("loomplane_federation_observers", "org_id", "agent_id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_federation_delivery_retention() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF (OLD.state->>'lease_until') IS NULL OR (OLD.state->>'lease_until')::timestamptz>clock_timestamp() THEN
		RAISE EXCEPTION 'federation delivery lease is still retained' USING ERRCODE='23503';
	END IF;
	RETURN OLD;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_federation_delivery_retention: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_federation_delivery_retention
	BEFORE DELETE ON loomplane_federation_deliveries
	FOR EACH ROW EXECUTE FUNCTION loomplane_federation_delivery_retention()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_federation_delivery_retention: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropTable("loomplane_federation_deliveries").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_federation_delivery_retention()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_federation_delivery_retention: %w", err)
			}
			return nil
		},
	)
}
