package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// loomplane_instance_settings holds the settings that belong to the whole
// instance rather than to an organization, one row per setting. The value
// is JSON, so a setting can grow past a single flag. The first is
// "waitlist": whether sign-up needs an invitation or an approved waitlist
// entry.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_instance_settings").IfNotExists().
				Column("key", migrate.Text, migrate.PrimaryKey()).
				Column("value", migrate.JSON, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_instance_settings").IfExists().Exec(ctx)
		},
	)
}
