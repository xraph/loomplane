package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateIndex("loomplane_backup_retirements_project_history").
				On("loomplane_backup_retirements").
				Columns("org_id", "project_id").
				ColumnDesc("created_at").
				ColumnDesc("id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropIndex("loomplane_backup_retirements_project_history").Exec(ctx)
		},
	)
}
