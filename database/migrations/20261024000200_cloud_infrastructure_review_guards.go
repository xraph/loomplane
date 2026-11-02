package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// cloudInfrastructureReviewRetiringServerUseSQL is the connection use guard
// this migration installs. A server that is gone no longer blocks its
// runtime connection, and one that isn't ready yet still does.
const cloudInfrastructureReviewRetiringServerUseSQL = `CREATE OR REPLACE FUNCTION loomplane_retiring_server_use() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.kind IN ('cloud_server','infrastructure_operation') THEN RETURN NEW; END IF;

	PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id FOR UPDATE;
	IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND runtime_connection_id=NEW.connection_id AND status NOT IN ('ready','deleted','disconnected')) THEN
		RAISE EXCEPTION 'server is not available' USING ERRCODE='23503';
	END IF;

	RETURN NEW;
END $$`

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_cloud_connections").
				AddColumn("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(1), migrate.Check("generation>0")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, cloudInfrastructureReviewRetiringServerUseSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_retiring_server_use: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, cloudInfrastructureRetiringServerUseSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_retiring_server_use: %w", err)
			}

			return db.NewAlterTable("loomplane_cloud_connections").
				DropColumn("generation").IfExists().
				Exec(ctx)
		},
	)
}
