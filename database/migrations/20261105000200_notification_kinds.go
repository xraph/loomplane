package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Outbox rows were all deployment notices until domain notices joined them.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_notification_outbox").
				AddColumn("kind", migrate.Text, migrate.NotNull(), migrate.Default("deployment")).IfNotExists().
				Exec(ctx)
		},
		nil,
	)
}
