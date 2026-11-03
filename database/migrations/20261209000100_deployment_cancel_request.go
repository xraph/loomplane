package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// cancel_requested_at records that someone cancelled a running deployment.
// The deployment stops before its next action, so the action that's running
// when the request lands is never fenced halfway through.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_deployments").
				AddColumn("cancel_requested_at", migrate.Timestamp).
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_deployments").
				DropColumn("cancel_requested_at").IfExists().
				Exec(ctx)
		},
	)
}
