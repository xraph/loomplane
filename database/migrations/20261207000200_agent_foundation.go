package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// The agent's tasks, the events of their runs, and the requests a run makes
// of a person. Each table belongs to one workspace: tasks and requests carry
// org_id, and run events reach theirs through their task. With AI off the
// tables exist and nothing writes to them.
//
// A task's realtime event comes from a trigger of its own that copies an
// allowlist of columns. It never carries input (a chat message is the
// person's), principal or metadata, and a change to only those emits
// nothing. A request is a realtime agent_request event through the generic
// state event trigger, whose function this leaves as it is.
//
// The route columns on requests (team_id to route_note) belong to teams,
// whose tables come in a later migration, so they hold plain text with no
// foreign key.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_agent_tasks").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("environment_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("kind", migrate.Text, migrate.NotNull(),
					migrate.Check("kind IN ('chat','investigation','release_preparation')")).
				Column("run_id", migrate.Text, migrate.NotNull(), migrate.Unique()).
				Column("session_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','waiting','completed','failed','cancelled')")).
				Column("requested_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("principal", migrate.JSON, migrate.NotNull()).
				Column("input", migrate.Text, migrate.NotNull()).
				Column("max_tokens", migrate.Integer, migrate.NotNull(), migrate.Check("max_tokens > 0")).
				Column("tokens", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("event_seq", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("continue_queued_at", migrate.Timestamp).
				Column("enqueued_at", migrate.Timestamp).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("metadata", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Column("finished_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}

			// Lists read a workspace's or a project's newest tasks; the
			// dispatcher reads queued ones oldest first.
			err = db.NewCreateIndex("loomplane_agent_tasks_org").IfNotExists().
				On("loomplane_agent_tasks").
				Columns("org_id").ColumnDesc("created_at").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_agent_tasks_project").IfNotExists().
				On("loomplane_agent_tasks").
				Columns("org_id", "project_id").ColumnDesc("created_at").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_agent_tasks_queued").IfNotExists().
				On("loomplane_agent_tasks").
				Columns("status", "enqueued_at").
				Where("status='queued'").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_agent_run_events").IfNotExists().
				Column("task_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_agent_tasks", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("seq", migrate.BigInt, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull()).
				Column("data", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("task_id", "seq").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_agent_requests").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("environment_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("task_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_agent_tasks", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("run_id", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull(),
					migrate.Check("kind IN ('approve_plan','connect_github','provide_secret','create_connection','answer')")).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('pending','answered','declined','expired','cancelled')")).
				Column("question", migrate.Text, migrate.NotNull()).
				Column("context", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("options", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("target", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("suspension_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("checkpoint_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("tool_call_id", migrate.Text, migrate.NotNull()).
				Column("asked_for", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("answered_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("answer", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("outcome", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("team_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("team_name", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("route_source", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("team_reason", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("route_note", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Column("answered_at", migrate.Timestamp).
				Unique("run_id", "tool_call_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			// Lists read a workspace's newest requests by status; the sweep
			// reads pending ones by when they expire.
			err = db.NewCreateIndex("loomplane_agent_requests_org").IfNotExists().
				On("loomplane_agent_requests").
				Columns("org_id", "status").ColumnDesc("created_at").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_agent_requests_pending").IfNotExists().
				On("loomplane_agent_requests").
				Columns("status", "expires_at").
				Where("status='pending'").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_agent_task_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	shown CONSTANT TEXT[] := ARRAY['id','org_id','project_id','environment_id','kind','run_id','status','requested_by','event_seq','tokens','max_tokens','error','created_at','updated_at','finished_at'];
	payload JSONB;
	previous JSONB;
BEGIN
	IF TG_OP<>'DELETE' THEN
		SELECT jsonb_object_agg(key,value) INTO payload FROM jsonb_each(to_jsonb(NEW)) WHERE key=ANY(shown);
	END IF;
	IF TG_OP<>'INSERT' THEN
		SELECT jsonb_object_agg(key,value) INTO previous FROM jsonb_each(to_jsonb(OLD)) WHERE key=ANY(shown);
	END IF;
	IF TG_OP='UPDATE' AND previous IS NOT DISTINCT FROM payload THEN RETURN NULL; END IF;

	IF TG_OP='DELETE' THEN
		PERFORM loomplane_append_event(OLD.org_id,OLD.project_id,'agent_task.deleted',OLD.id,previous);
	ELSE
		PERFORM loomplane_append_event(NEW.org_id,NEW.project_id,'agent_task.updated',NEW.id,payload);
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_agent_task_event_trigger: %w", err)
			}
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_agent_task_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_agent_tasks
FOR EACH ROW EXECUTE FUNCTION loomplane_agent_task_event_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_agent_task_event on loomplane_agent_tasks: %w", err)
			}
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_state_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_agent_requests
FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger('agent_request')`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_state_event on loomplane_agent_requests: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			// Dropping a table drops its triggers.
			for _, table := range []string{"loomplane_agent_requests", "loomplane_agent_run_events", "loomplane_agent_tasks"} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			_, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_agent_task_event_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_agent_task_event_trigger: %w", err)
			}
			return nil
		},
	)
}
