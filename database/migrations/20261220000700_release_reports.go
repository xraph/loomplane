package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Release readiness reports, one row per release preparation a person asked
// for. With AI off the table exists and nothing writes to it.
//
// A report is the realtime release_report event through a trigger of its
// own. The event carries the report without its evidence and its concerns,
// which hold commit, diff and model text, and without the notice and sweep
// bookkeeping; a change to only those emits nothing. Clients refetch the
// report for the rest.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_release_reports").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(), migrate.Check("project_id <> ''")).
				Column("from_environment_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("from_environment", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("to_environment_id", migrate.Text, migrate.NotNull(), migrate.Check("to_environment_id <> ''")).
				Column("to_environment", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("scope", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('planning','reviewing','done','failed','cancelled')")).
				Column("state", migrate.Text, migrate.NotNull(),
					migrate.Check("state IN ('planning','reviewing','promoting','promoted','waiting_for_person','approved','rejected','stale','expired','superseded','blocked','stopped','failed','cancelled')")).
				Column("state_reason", migrate.Text, migrate.NotNull(), migrate.Default("")).
				// Every preparation is a person's request.
				Column("requested_by", migrate.Text, migrate.NotNull(), migrate.Check("requested_by <> ''")).
				Column("via", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("plan_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("plan_revision", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("reused_plan", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("task_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("run_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("evidence", migrate.JSON).
				Column("gates", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("preview", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("decision", migrate.Text, migrate.NotNull(), migrate.Default(""),
					migrate.Check("decision IN ('','promote','ask')")).
				Column("evaluated_at", migrate.Timestamp).
				Column("concerns", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("notified_state", migrate.Text, migrate.NotNull(), migrate.Default("")).
				// When the settle sweep last read the report, so it reaches
				// every unsettled report in turn.
				Column("swept_at", migrate.Timestamp).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Column("finished_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}

			// One preparation in progress per environment.
			err = db.NewCreateIndex("loomplane_release_reports_active").IfNotExists().Unique().
				On("loomplane_release_reports").
				Columns("org_id", "to_environment_id").
				Where("status IN ('planning','reviewing')").
				Exec(ctx)
			if err != nil {
				return err
			}
			// A project's reports, newest first.
			err = db.NewCreateIndex("loomplane_release_reports_project").IfNotExists().
				On("loomplane_release_reports").
				Columns("org_id", "project_id").ColumnDesc("created_at").
				Exec(ctx)
			if err != nil {
				return err
			}
			// The promotion gate finds a plan's report.
			err = db.NewCreateIndex("loomplane_release_reports_plan").IfNotExists().
				On("loomplane_release_reports").
				Columns("org_id", "plan_id").
				Exec(ctx)
			if err != nil {
				return err
			}
			// The settle sweep reads every workspace's unfinished reports,
			// least recently swept first.
			_, err = db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_release_reports_unsettled
ON loomplane_release_reports (swept_at NULLS FIRST, updated_at, id)
WHERE status IN ('planning','reviewing') OR state IN ('planning','reviewing','promoting','waiting_for_person')`)
			if err != nil {
				return fmt.Errorf("create index loomplane_release_reports_unsettled: %w", err)
			}

			// The event is a projection of the report's columns, so a change
			// to evidence, concerns, notified_state or swept_at compares
			// equal and emits nothing.
			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_release_report_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	payload JSONB;
	previous JSONB;
BEGIN
	IF TG_OP<>'DELETE' THEN
		payload:=jsonb_build_object(
			'id',NEW.id,'org_id',NEW.org_id,'project_id',NEW.project_id,
			'from_environment_id',NEW.from_environment_id,'from_environment',NEW.from_environment,
			'to_environment_id',NEW.to_environment_id,'to_environment',NEW.to_environment,'scope',NEW.scope,
			'status',NEW.status,'state',NEW.state,'state_reason',NEW.state_reason,'requested_by',NEW.requested_by,'via',NEW.via,
			'plan_id',NEW.plan_id,'plan_revision',NEW.plan_revision,'reused_plan',NEW.reused_plan,'task_id',NEW.task_id,'run_id',NEW.run_id,
			'gates',NEW.gates,'preview',NEW.preview,'decision',NEW.decision,'evaluated_at',NEW.evaluated_at,'error',NEW.error,
			'created_at',NEW.created_at,'updated_at',NEW.updated_at,'finished_at',NEW.finished_at);
	END IF;
	IF TG_OP<>'INSERT' THEN
		previous:=jsonb_build_object(
			'id',OLD.id,'org_id',OLD.org_id,'project_id',OLD.project_id,
			'from_environment_id',OLD.from_environment_id,'from_environment',OLD.from_environment,
			'to_environment_id',OLD.to_environment_id,'to_environment',OLD.to_environment,'scope',OLD.scope,
			'status',OLD.status,'state',OLD.state,'state_reason',OLD.state_reason,'requested_by',OLD.requested_by,'via',OLD.via,
			'plan_id',OLD.plan_id,'plan_revision',OLD.plan_revision,'reused_plan',OLD.reused_plan,'task_id',OLD.task_id,'run_id',OLD.run_id,
			'gates',OLD.gates,'preview',OLD.preview,'decision',OLD.decision,'evaluated_at',OLD.evaluated_at,'error',OLD.error,
			'created_at',OLD.created_at,'updated_at',OLD.updated_at,'finished_at',OLD.finished_at);
	END IF;
	IF TG_OP='UPDATE' AND previous IS NOT DISTINCT FROM payload THEN RETURN NULL; END IF;

	IF TG_OP='DELETE' THEN
		PERFORM loomplane_append_event(OLD.org_id,OLD.project_id,'release_report.deleted',OLD.id,previous);
	ELSE
		PERFORM loomplane_append_event(NEW.org_id,NEW.project_id,'release_report.updated',NEW.id,payload);
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_release_report_event_trigger: %w", err)
			}
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_release_report_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_release_reports
FOR EACH ROW EXECUTE FUNCTION loomplane_release_report_event_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_release_report_event: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			// Dropping the table drops its trigger.
			if err := db.NewDropTable("loomplane_release_reports").IfExists().Exec(ctx); err != nil {
				return err
			}
			if _, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_release_report_event_trigger()`); err != nil {
				return fmt.Errorf("drop function loomplane_release_report_event_trigger: %w", err)
			}
			return nil
		},
	)
}
