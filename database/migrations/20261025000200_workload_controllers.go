package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// workload_controllers retains mutation receipts separately from service
// registrations. Receipts contain no credentials and are scoped to their parents.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_workload_controllers").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("instance_id", migrate.Text, migrate.NotNull()).
				Column("runtime_ref", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("org_id", "connection_id", "instance_id", "runtime_ref").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Cascade).
				ForeignKey("org_id", "connection_id").
				References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $controllers_down$ BEGIN
	IF to_regclass('loomplane_workload_controllers') IS NOT NULL THEN
		LOCK TABLE loomplane_workload_controllers IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_workload_controllers) THEN
			RAISE EXCEPTION 'controller receipts are still retained' USING ERRCODE='55000';
		END IF;
		DROP TABLE loomplane_workload_controllers;
	END IF;
END $controllers_down$`)
			if err != nil {
				return fmt.Errorf("drop loomplane_workload_controllers: %w", err)
			}
			return nil
		},
	)
}
