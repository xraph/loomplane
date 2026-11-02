package migrations

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/grove/migrate"
)

// deploymentCapacityGuard is the deployment guard cloud_sync installs. With
// paused set, a paused deployment holds the environment as a running one does.
func deploymentCapacityGuard(paused bool) string {
	sql := deploymentGuardSQL
	if paused {
		sql = strings.ReplaceAll(sql, "NEW.status<>'running'", "NEW.status NOT IN ('running','paused')")
		sql = strings.ReplaceAll(sql, "status='running'", "status IN ('running','paused')")
	}
	return sql
}

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_deployments").
				AddColumn("capacity_revision", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("capacity_revision>=0")).
				AddColumn("capacity_wait", migrate.JSON).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_deployments").
				AddCheck("status IN ('running','paused','succeeded','failed','cancelled')").Named("loomplane_deployment_status").
				AddCheck("coalesce((status='paused' AND capacity_wait IS NOT NULL AND finished_at IS NULL AND capacity_revision>0 AND (capacity_wait->>'revision')::bigint=capacity_revision AND jsonb_array_length(capacity_wait->'blocks')>0) OR (status<>'paused' AND capacity_wait IS NULL),false)").Named("loomplane_deployment_wait").
				AddCheck("status<>'cancelled' OR finished_at IS NOT NULL").Named("loomplane_deployment_cancelled").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_deployment_capacity_events").
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("deployment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_deployments", "id"), migrate.OnDelete(migrate.Restrict)).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("workflow_delivered", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("notification_delivered", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				PrimaryKey("org_id", "deployment_id", "revision").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_capacity_workflow_pending").
				On("loomplane_deployment_capacity_events").
				Columns("org_id", "created_at").
				Where("NOT workflow_delivered").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_capacity_notification_pending").
				On("loomplane_deployment_capacity_events").
				Columns("org_id", "created_at").
				Where("NOT notification_delivered").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, deploymentCapacityGuard(true))
			if err != nil {
				return fmt.Errorf("create function loomplane_deployment_guard: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$ BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_deployment_capacity_events)
	THEN RAISE EXCEPTION 'capacity transition history must be retained'; END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check capacity events are empty: %w", err)
			}

			err = db.NewDropTable("loomplane_deployment_capacity_events").Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_deployments").
				DropConstraint("loomplane_deployment_status").
				DropConstraint("loomplane_deployment_wait").
				DropConstraint("loomplane_deployment_cancelled").
				DropColumn("capacity_revision").
				DropColumn("capacity_wait").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, deploymentCapacityGuard(false))
			if err != nil {
				return fmt.Errorf("create function loomplane_deployment_guard: %w", err)
			}
			return nil
		},
	)
}
