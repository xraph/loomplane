package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		persistRealtimeEvents,
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_parent_event ON loomplane_project_sources`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_parent_event on loomplane_project_sources: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_parent_event ON loomplane_plan_sources`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_parent_event on loomplane_plan_sources: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_parent_event_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_parent_event_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DO $$
DECLARE
	name TEXT;
BEGIN
	FOR name IN SELECT unnest(ARRAY['loomplane_projects','loomplane_environments','loomplane_analyses','loomplane_project_sources','loomplane_manifests','loomplane_plans','loomplane_bindings','loomplane_cloud_connections','loomplane_deployments']) LOOP
		EXECUTE format('DROP TRIGGER IF EXISTS loomplane_state_event ON %I',name);
	END LOOP;
END $$`)
			if err != nil {
				return fmt.Errorf("drop triggers loomplane_state_event: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_state_event_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_state_event_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_append_event(TEXT,TEXT,TEXT,TEXT,JSONB)`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_append_event: %w", err)
			}

			err = db.NewDropTable("loomplane_log_checkpoints").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_build_logs").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_build_log_budgets").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_events").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_event_cursors").IfExists().Exec(ctx)
		},
	)
}

// persistRealtimeEvents is this migration's up. finalize_realtime_contracts
// runs it again before its own steps.
func persistRealtimeEvents(ctx context.Context, db *migrate.DB) error {
	err := db.NewCreateTable("loomplane_event_cursors").IfNotExists().
		Column("org_id", migrate.Text, migrate.PrimaryKey()).
		Column("next_seq", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
		Column("floor_seq", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
		Exec(ctx)
	if err != nil {
		return err
	}

	err = db.NewCreateTable("loomplane_events").IfNotExists().
		Column("org_id", migrate.Text, migrate.NotNull()).
		Column("sequence", migrate.BigInt, migrate.NotNull()).
		Column("project_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
		Column("type", migrate.Text, migrate.NotNull()).
		Column("entity_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
		Column("data", migrate.JSON, migrate.NotNull()).
		Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
		PrimaryKey("org_id", "sequence").
		Exec(ctx)
	if err != nil {
		return err
	}

	err = db.NewCreateIndex("loomplane_events_retention").IfNotExists().
		On("loomplane_events").
		Columns("created_at").
		Exec(ctx)
	if err != nil {
		return err
	}

	_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_append_event(org TEXT,project TEXT,event_type TEXT,entity TEXT,payload JSONB) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
	seq BIGINT;
BEGIN
	IF org IS NULL OR org='' OR octet_length(payload::text)>4194304 THEN RAISE EXCEPTION 'invalid realtime event'; END IF;

	INSERT INTO loomplane_event_cursors (org_id,next_seq) VALUES (org,1)
		ON CONFLICT (org_id) DO UPDATE SET next_seq=loomplane_event_cursors.next_seq+1
		RETURNING next_seq INTO seq;
	INSERT INTO loomplane_events (org_id,sequence,project_id,type,entity_id,data) VALUES (org,seq,project,event_type,entity,payload);
	PERFORM pg_notify('loomplane_events',json_build_object('org_id',org,'cursor',org||':'||seq)::text);

	RETURN seq;
END $$`)
	if err != nil {
		return fmt.Errorf("create function loomplane_append_event: %w", err)
	}

	_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_state_event_trigger() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE
	doc JSONB;
	project TEXT;
	entity_type TEXT;
	event_type TEXT;
BEGIN
	IF TG_OP='UPDATE' AND to_jsonb(OLD)=to_jsonb(NEW) THEN RETURN NULL; END IF;
	IF TG_OP='DELETE' THEN doc:=to_jsonb(OLD); ELSE doc:=to_jsonb(NEW); END IF;
	entity_type:=TG_ARGV[0];
	project:=coalesce(doc->>'project_id','');

	IF entity_type='project' THEN
		project:=doc->>'id';
		doc:=doc-'repository_url' || jsonb_build_object('repositories',coalesce((
			SELECT jsonb_agg(repository ORDER BY alias)
			FROM (
				SELECT DISTINCT ON(repository) repository,alias
				FROM loomplane_project_sources
				WHERE project_id=project AND removed_at IS NULL
				ORDER BY repository,alias
			) repositories),'[]'));
	ELSIF entity_type='analysis' THEN
		doc:=doc-'graph'-'attempts' || jsonb_build_object('source',coalesce((SELECT alias FROM loomplane_project_sources WHERE id=doc->>'source_id'),''));
	ELSIF entity_type='plan' THEN
		doc:=doc-'result' || jsonb_build_object('actions','[]'::jsonb,'problems','[]'::jsonb,'warnings','[]'::jsonb,'connections','{}'::jsonb,'summary','{}'::jsonb)
			|| coalesce(nullif(doc->'result','null'::jsonb),'{}')
			|| jsonb_build_object('sources',coalesce((SELECT jsonb_agg(to_jsonb(ps)-'plan_id' ORDER BY alias) FROM loomplane_plan_sources ps WHERE plan_id=doc->>'id'),'[]'));
	ELSIF entity_type='binding' THEN
		doc:=doc-'replaced';
	ELSIF entity_type='manifest' THEN
		doc:=doc-'body' || jsonb_build_object('yaml',doc->'body');
	END IF;

	event_type:=entity_type||'.updated';
	IF TG_OP='DELETE' THEN event_type:=entity_type||'.deleted'; END IF;
	PERFORM loomplane_append_event(doc->>'org_id',project,event_type,doc->>'id',doc);

	RETURN NULL;
END $$`)
	if err != nil {
		return fmt.Errorf("create function loomplane_state_event_trigger: %w", err)
	}

	_, err = db.Exec(ctx, `DO $$
DECLARE
	table_name TEXT;
	entity TEXT;
BEGIN
	FOR table_name,entity IN
		SELECT * FROM (VALUES
			('loomplane_projects','project'),('loomplane_environments','environment'),
			('loomplane_analyses','analysis'),('loomplane_project_sources','source'),('loomplane_manifests','manifest'),
			('loomplane_plans','plan'),('loomplane_bindings','binding'),('loomplane_cloud_connections','connection'),('loomplane_deployments','deployment')
		) names(table_name,entity)
	LOOP
		EXECUTE format('CREATE OR REPLACE TRIGGER loomplane_state_event AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger(%L)',table_name,entity);
	END LOOP;
END $$`)
	if err != nil {
		return fmt.Errorf("create triggers loomplane_state_event: %w", err)
	}

	_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_parent_event_trigger() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE
	child JSONB;
	doc JSONB;
	parent TEXT;
BEGIN
	IF TG_OP='DELETE' THEN child:=to_jsonb(OLD); ELSE child:=to_jsonb(NEW); END IF;

	IF TG_TABLE_NAME='loomplane_project_sources' THEN
		parent:=child->>'project_id';
		SELECT to_jsonb(p) INTO doc FROM loomplane_projects p WHERE id=parent;
		IF doc IS NULL THEN RETURN NULL; END IF;
		doc:=doc-'repository_url' || jsonb_build_object('repositories',coalesce((
			SELECT jsonb_agg(repository ORDER BY alias)
			FROM (
				SELECT DISTINCT ON(repository) repository,alias
				FROM loomplane_project_sources
				WHERE project_id=parent AND removed_at IS NULL
				ORDER BY repository,alias
			) sources),'[]'));
		PERFORM loomplane_append_event(doc->>'org_id',parent,'project.updated',parent,doc);
	ELSE
		parent:=child->>'plan_id';
		SELECT to_jsonb(p) INTO doc FROM loomplane_plans p WHERE id=parent;
		IF doc IS NULL THEN RETURN NULL; END IF;
		doc:=doc-'result' || jsonb_build_object('actions','[]'::jsonb,'problems','[]'::jsonb,'warnings','[]'::jsonb,'connections','{}'::jsonb,'summary','{}'::jsonb)
			|| coalesce(nullif(doc->'result','null'::jsonb),'{}')
			|| jsonb_build_object('sources',coalesce((SELECT jsonb_agg(to_jsonb(ps)-'plan_id' ORDER BY alias) FROM loomplane_plan_sources ps WHERE plan_id=parent),'[]'));
		PERFORM loomplane_append_event(doc->>'org_id',doc->>'project_id','plan.updated',parent,doc);
	END IF;

	RETURN NULL;
END $$`)
	if err != nil {
		return fmt.Errorf("create function loomplane_parent_event_trigger: %w", err)
	}

	_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_parent_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_project_sources
FOR EACH ROW EXECUTE FUNCTION loomplane_parent_event_trigger()`)
	if err != nil {
		return fmt.Errorf("create trigger loomplane_parent_event on loomplane_project_sources: %w", err)
	}

	_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_parent_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_plan_sources
FOR EACH ROW EXECUTE FUNCTION loomplane_parent_event_trigger()`)
	if err != nil {
		return fmt.Errorf("create trigger loomplane_parent_event on loomplane_plan_sources: %w", err)
	}

	err = db.NewCreateTable("loomplane_build_log_budgets").IfNotExists().
		Column("org_id", migrate.Text, migrate.NotNull()).
		Column("deployment_id", migrate.Text, migrate.NotNull()).
		Column("action_id", migrate.Text, migrate.NotNull()).
		Column("attempt", migrate.Text, migrate.NotNull()).
		Column("bytes", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
		Column("truncated", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
		Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
		PrimaryKey("org_id", "deployment_id", "action_id", "attempt").
		Exec(ctx)
	if err != nil {
		return err
	}

	err = db.NewCreateTable("loomplane_build_logs").IfNotExists().
		Column("org_id", migrate.Text, migrate.NotNull()).
		Column("sequence", migrate.BigInt, migrate.NotNull()).
		Column("project_id", migrate.Text, migrate.NotNull()).
		Column("deployment_id", migrate.Text, migrate.NotNull()).
		Column("action_id", migrate.Text, migrate.NotNull()).
		Column("attempt", migrate.Text, migrate.NotNull()).
		Column("operation_id", migrate.Text, migrate.NotNull()).
		Column("provider_id", migrate.Text, migrate.NotNull()).
		Column("stream", migrate.Text, migrate.NotNull()).
		Column("source", migrate.Text, migrate.NotNull()).
		Column("level", migrate.Text, migrate.NotNull()).
		Column("message", migrate.Text, migrate.NotNull()).
		Column("time", migrate.Timestamp, migrate.NotNull()).
		Column("truncated", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
		PrimaryKey("org_id", "sequence").
		Unique("org_id", "deployment_id", "action_id", "attempt", "operation_id", "stream", "provider_id").
		ForeignKey("org_id", "sequence").References("loomplane_events", "org_id", "sequence").OnDelete(migrate.Cascade).
		Exec(ctx)
	if err != nil {
		return err
	}

	err = db.NewCreateIndex("loomplane_build_logs_deployment").IfNotExists().
		On("loomplane_build_logs").
		Columns("org_id", "deployment_id", "sequence").
		Exec(ctx)
	if err != nil {
		return err
	}

	return db.NewCreateTable("loomplane_log_checkpoints").IfNotExists().
		Column("org_id", migrate.Text, migrate.NotNull()).
		Column("project_id", migrate.Text, migrate.NotNull()).
		Column("deployment_id", migrate.Text, migrate.NotNull()).
		Column("action_id", migrate.Text, migrate.NotNull()).
		Column("attempt", migrate.Text, migrate.NotNull()).
		Column("operation_id", migrate.Text, migrate.NotNull()).
		Column("cursor", migrate.Text, migrate.NotNull(), migrate.Default("")).
		Column("state", migrate.Text, migrate.NotNull(), migrate.Default("collecting")).
		Column("message", migrate.Text, migrate.NotNull(), migrate.Default("")).
		Column("updated_at", migrate.Timestamp, migrate.NotNull()).
		PrimaryKey("org_id", "deployment_id", "action_id", "attempt", "operation_id").
		Exec(ctx)
}
