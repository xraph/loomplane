package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A binding's alias is the name people gave what it runs. It names nothing
// in the cloud. Empty means none, and two bindings of one environment can't
// share one that's set.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_bindings").
				AddColumn("alias", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			if _, err = db.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_bindings_alias
	ON loomplane_bindings (environment_id, alias) WHERE alias <> ''`); err != nil {
				return fmt.Errorf("create index loomplane_bindings_alias: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_bindings_alias`); err != nil {
				return fmt.Errorf("drop index loomplane_bindings_alias: %w", err)
			}
			return db.NewAlterTable("loomplane_bindings").
				DropColumn("alias").IfExists().
				Exec(ctx)
		},
	)
}
