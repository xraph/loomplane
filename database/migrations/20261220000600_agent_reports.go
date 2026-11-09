package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// The agent's reports, one row per incident, and the watcher's state, one row
// per workspace. Reports are written by detectors and by the agent's
// open_report tool; the watcher row holds its event cursor, its lease and the
// last result of each release check. With AI off the tables exist and nothing
// writes to them.
//
// A report is the realtime agent_report event through a trigger of its own.
// The event carries exactly the fields a person sees, with the issue columns
// folded into an issue object. Delivery, lease and queue columns, the
// occurrence keys and the notice bookkeeping never reach it, and a change to
// only those emits nothing.
//
// The installations table gains what the connected GitHub App was granted, so
// Loomplane can tell whether Issues are accepted before it tries to open one.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_agent_reports").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("environment", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("signal", migrate.Text, migrate.NotNull(),
					migrate.Check("signal IN ('deployment_failed','canary_rolled_back','canary_revert_failed','watch_rolled_back','watch_rollback_blocked','watch_rollback_failed','drift','error_rate','analysis_failing','manual')")).
				Column("component", migrate.Text, migrate.NotNull()).
				Column("fingerprint", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('open','resolved','closed')")).
				Column("severity", migrate.Text, migrate.NotNull(),
					migrate.Check("severity IN ('critical','high','medium','low')")).
				Column("title", migrate.Text, migrate.NotNull()).
				Column("facts", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("evidence", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("findings", migrate.JSON).
				Column("repository", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("occurrences", migrate.Integer, migrate.NotNull(), migrate.Default(1)).
				Column("occurrence_keys", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("first_seen_at", migrate.Timestamp, migrate.NotNull()).
				Column("last_seen_at", migrate.Timestamp, migrate.NotNull()).
				Column("resolved_at", migrate.Timestamp).
				Column("closed_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("resolution", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("taken_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("taken_at", migrate.Timestamp).
				Column("taken_notice_seq", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("investigation", migrate.Text, migrate.NotNull(), migrate.Default("none"),
					migrate.Check("investigation IN ('none','queued','running','done','failed','skipped')")).
				Column("investigation_note", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("task_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("run_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("investigation_started_at", migrate.Timestamp).
				Column("revision", migrate.Integer, migrate.NotNull(), migrate.Default(1)).
				Column("notified_revision", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("issue_revision", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("pending_notice", migrate.Text, migrate.NotNull(), migrate.Default(""),
					migrate.Check("pending_notice IN ('','opened','reopened','repeated')")).
				Column("notice_seq", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("last_notice_at", migrate.Timestamp).
				// Pending comment kinds: repeated, reopened, findings, resolved, closed.
				Column("issue_comments", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				// The last repeat comment, for the hourly throttle.
				Column("issue_comment_at", migrate.Timestamp).
				Column("issue_number", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("issue_url", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("issue_state", migrate.Text, migrate.NotNull(), migrate.Default("none"),
					migrate.Check("issue_state IN ('none','open','closed','waiting_for_permission','no_repository','failed')")).
				Column("issue_error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("delivery_queued_at", migrate.Timestamp).
				Column("delivery_owner", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("delivery_until", migrate.Timestamp).
				Column("checked_at", migrate.Timestamp).
				Column("opened_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("opened_by_task", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			// A problem has one open report. A cleared one can be followed by
			// a new one with the same fingerprint.
			err = db.NewCreateIndex("loomplane_agent_reports_open").IfNotExists().Unique().
				On("loomplane_agent_reports").
				Columns("org_id", "fingerprint").
				Where("status='open'").
				Exec(ctx)
			if err != nil {
				return err
			}
			// Lists read a workspace's or a project's newest reports; a
			// reopen looks for the latest report that resolved.
			err = db.NewCreateIndex("loomplane_agent_reports_status").IfNotExists().
				On("loomplane_agent_reports").
				Columns("org_id", "status").ColumnDesc("last_seen_at").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_agent_reports_project").IfNotExists().
				On("loomplane_agent_reports").
				Columns("org_id", "project_id").ColumnDesc("last_seen_at").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_agent_reports_fingerprint").IfNotExists().
				On("loomplane_agent_reports").
				Columns("org_id", "fingerprint").ColumnDesc("resolved_at").
				Exec(ctx)
			if err != nil {
				return err
			}
			// Delivery reads the reports whose notice or issue is behind.
			err = db.NewCreateIndex("loomplane_agent_reports_undelivered").IfNotExists().
				On("loomplane_agent_reports").
				Columns("org_id").
				Where("revision > notified_revision OR revision > issue_revision").
				Exec(ctx)
			if err != nil {
				return err
			}
			// A person's notice finds the reports about a deployment or a plan.
			_, err = db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_agent_reports_evidence_deployment
ON loomplane_agent_reports (org_id, (evidence->>'deployment_id')) WHERE evidence->>'deployment_id' <> ''`)
			if err != nil {
				return fmt.Errorf("create index loomplane_agent_reports_evidence_deployment: %w", err)
			}
			_, err = db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_agent_reports_evidence_plan
ON loomplane_agent_reports (org_id, (evidence->>'plan_id')) WHERE evidence->>'plan_id' <> ''`)
			if err != nil {
				return fmt.Errorf("create index loomplane_agent_reports_evidence_plan: %w", err)
			}

			err = db.NewCreateTable("loomplane_agent_watchers").IfNotExists().
				Column("org_id", migrate.Text, migrate.PrimaryKey()).
				Column("event_cursor", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("lease_owner", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("lease_until", migrate.Timestamp).
				Column("paused", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("paused_reason", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("paused_at", migrate.Timestamp).
				// Binding ID to {"checked_at","passes","failing"}.
				Column("checks", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("swept_at", migrate.Timestamp).
				Column("rechecked_at", migrate.Timestamp).
				Column("ticked_at", migrate.Timestamp).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_github_installations").
				AddColumn("permissions", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).IfNotExists().
				AddColumn("permissions_checked_at", migrate.Timestamp).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			// The event is a projection of the Report's JSON fields, so a
			// change to a delivery or lease column compares equal and emits
			// nothing. taken_notice_seq stays out: it is bookkeeping.
			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_agent_report_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	payload JSONB;
	previous JSONB;
BEGIN
	IF TG_OP<>'DELETE' THEN
		payload:=jsonb_build_object(
			'id',NEW.id,'org_id',NEW.org_id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,'environment',NEW.environment,
			'signal',NEW.signal,'component',NEW.component,'fingerprint',NEW.fingerprint,'status',NEW.status,'severity',NEW.severity,
			'title',NEW.title,'facts',NEW.facts,'evidence',NEW.evidence,'findings',NEW.findings,'repository',NEW.repository,
			'occurrences',NEW.occurrences,'first_seen_at',NEW.first_seen_at,'last_seen_at',NEW.last_seen_at,'resolved_at',NEW.resolved_at,
			'closed_by',NEW.closed_by,'resolution',NEW.resolution,'taken_by',NEW.taken_by,'taken_at',NEW.taken_at,
			'investigation',NEW.investigation,'investigation_note',NEW.investigation_note,'task_id',NEW.task_id,
			'issue',jsonb_build_object('number',NEW.issue_number,'url',NEW.issue_url,'state',NEW.issue_state,'error',NEW.issue_error),
			'revision',NEW.revision,'opened_by',NEW.opened_by,'created_at',NEW.created_at,'updated_at',NEW.updated_at);
	END IF;
	IF TG_OP<>'INSERT' THEN
		previous:=jsonb_build_object(
			'id',OLD.id,'org_id',OLD.org_id,'project_id',OLD.project_id,'environment_id',OLD.environment_id,'environment',OLD.environment,
			'signal',OLD.signal,'component',OLD.component,'fingerprint',OLD.fingerprint,'status',OLD.status,'severity',OLD.severity,
			'title',OLD.title,'facts',OLD.facts,'evidence',OLD.evidence,'findings',OLD.findings,'repository',OLD.repository,
			'occurrences',OLD.occurrences,'first_seen_at',OLD.first_seen_at,'last_seen_at',OLD.last_seen_at,'resolved_at',OLD.resolved_at,
			'closed_by',OLD.closed_by,'resolution',OLD.resolution,'taken_by',OLD.taken_by,'taken_at',OLD.taken_at,
			'investigation',OLD.investigation,'investigation_note',OLD.investigation_note,'task_id',OLD.task_id,
			'issue',jsonb_build_object('number',OLD.issue_number,'url',OLD.issue_url,'state',OLD.issue_state,'error',OLD.issue_error),
			'revision',OLD.revision,'opened_by',OLD.opened_by,'created_at',OLD.created_at,'updated_at',OLD.updated_at);
	END IF;
	IF TG_OP='UPDATE' AND previous IS NOT DISTINCT FROM payload THEN RETURN NULL; END IF;

	IF TG_OP='DELETE' THEN
		PERFORM loomplane_append_event(OLD.org_id,OLD.project_id,'agent_report.deleted',OLD.id,previous);
	ELSE
		PERFORM loomplane_append_event(NEW.org_id,NEW.project_id,'agent_report.updated',NEW.id,payload);
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_agent_report_event_trigger: %w", err)
			}
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_agent_report_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_agent_reports
FOR EACH ROW EXECUTE FUNCTION loomplane_agent_report_event_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_agent_report_event: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			// Dropping a table drops its trigger.
			for _, table := range []string{"loomplane_agent_reports", "loomplane_agent_watchers"} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			if _, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_agent_report_event_trigger()`); err != nil {
				return fmt.Errorf("drop function loomplane_agent_report_event_trigger: %w", err)
			}
			_, err := db.Exec(ctx, `ALTER TABLE IF EXISTS loomplane_github_installations DROP COLUMN IF EXISTS permissions, DROP COLUMN IF EXISTS permissions_checked_at`)
			if err != nil {
				return fmt.Errorf("drop installation permissions: %w", err)
			}
			return nil
		},
	)
}
