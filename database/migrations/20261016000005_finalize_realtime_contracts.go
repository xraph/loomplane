package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Development servers may already have applied the initial event schema.
// Refresh the finalized projections and add the collection's project scope
// through a forward migration without rewriting their migration history.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := persistRealtimeEvents(ctx, db)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_log_checkpoints").
				AddColumn("project_id", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_log_checkpoints c
SET project_id=d.project_id
FROM loomplane_deployments d
WHERE c.org_id=d.org_id AND c.deployment_id=d.id AND c.project_id=''`)
			if err != nil {
				return fmt.Errorf("backfill loomplane_log_checkpoints project_id: %w", err)
			}
			return nil
		},
		nil,
	)
}
