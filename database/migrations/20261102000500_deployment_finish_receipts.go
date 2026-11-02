package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_deployment_finishes").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("deployment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_deployments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("activity_key", migrate.Text, migrate.NotNull()).
				Column("retries", migrate.Integer, migrate.NotNull(), migrate.Check("retries >= 0")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "deployment_id", "activity_key").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$ BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_deployment_finishes) THEN
		RAISE EXCEPTION 'deployment finish receipts must be retained';
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check loomplane_deployment_finishes is empty: %w", err)
			}

			return db.NewDropTable("loomplane_deployment_finishes").IfExists().Exec(ctx)
		},
	)
}
