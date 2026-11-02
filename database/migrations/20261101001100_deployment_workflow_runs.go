package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_deployment_workflow_runs").IfNotExists().
				Column("sequence", migrate.BigSerial, migrate.Unique()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("workflow_id", migrate.Text, migrate.PrimaryKey()).
				Column("request", migrate.JSON, migrate.NotNull()).
				Column("start_delivered", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("start_attempted_at", migrate.Timestamp).
				Check("coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3'),false)").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_deployment_workflow_plan").IfNotExists().
				On("loomplane_deployment_workflow_runs").
				Columns("plan_id").
				ColumnDesc("sequence").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_deployment_workflow_pending").IfNotExists().
				On("loomplane_deployment_workflow_runs").
				Columns("start_attempted_at", "sequence").
				Where("NOT start_delivered").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_deployment_workflow_participants").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("workflow_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_deployment_workflow_runs", "workflow_id")).
				PrimaryKey("org_id", "plan_id", "workflow_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$ BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs)
	THEN RAISE EXCEPTION 'workflow routing must be retained'; END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check workflow runs are empty: %w", err)
			}

			err = db.NewDropTable("loomplane_deployment_workflow_participants").Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_deployment_workflow_runs").Exec(ctx)
		},
	)
}
