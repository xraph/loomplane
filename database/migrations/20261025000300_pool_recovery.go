package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_pool_execution").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("phase", migrate.Text, migrate.NotNull(), migrate.Default("reserved")).
				Column("started", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("active", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("checkpoint", migrate.Bytes).
				Column("storage_retained", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				PrimaryKey("org_id", "plan_id", "service").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_pool_execution(org_id,plan_id,service,started,active)
SELECT r.org_id,r.plan_id,r.service,
	EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=r.plan_id),
	EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=r.plan_id AND d.status='running')
FROM loomplane_capacity_reservations r
ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill loomplane_pool_execution: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_pool_execution_create() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	INSERT INTO loomplane_pool_execution(org_id,plan_id,service) VALUES(NEW.org_id,NEW.plan_id,NEW.service) ON CONFLICT DO NOTHING;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_pool_execution_create: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_pool_execution_create
AFTER INSERT ON loomplane_capacity_reservations
FOR EACH ROW EXECUTE FUNCTION loomplane_pool_execution_create()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_pool_execution_create: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_pool_execution_create ON loomplane_capacity_reservations`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_pool_execution_create: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_pool_execution_create()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_pool_execution_create: %w", err)
			}

			return db.NewDropTable("loomplane_pool_execution").IfExists().Exec(ctx)
		},
	)
}
