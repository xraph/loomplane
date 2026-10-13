package migrations

// Development servers may already have applied the initial event schema.
// Refresh the finalized projections and add the collection's project scope
// through a forward migration without rewriting their migration history.
func realtimeFinalizeUp() []string {
	up := append([]string{}, realtimeUp...)
	return append(up,
		`ALTER TABLE loomplane_log_checkpoints ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT ''`,
		`UPDATE loomplane_log_checkpoints c SET project_id=d.project_id FROM loomplane_deployments d WHERE c.org_id=d.org_id AND c.deployment_id=d.id AND c.project_id=''`,
	)
}

var realtimeUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_event_cursors (org_id TEXT PRIMARY KEY,next_seq BIGINT NOT NULL DEFAULT 0,floor_seq BIGINT NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS loomplane_events (
		org_id TEXT NOT NULL,sequence BIGINT NOT NULL,project_id TEXT NOT NULL DEFAULT '',type TEXT NOT NULL,
		entity_id TEXT NOT NULL DEFAULT '',data JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
		PRIMARY KEY (org_id,sequence)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_events_retention ON loomplane_events (created_at)`,
	`CREATE OR REPLACE FUNCTION loomplane_append_event(org TEXT,project TEXT,event_type TEXT,entity TEXT,payload JSONB) RETURNS BIGINT
		LANGUAGE plpgsql AS $$ DECLARE seq BIGINT; BEGIN
			IF org IS NULL OR org='' OR octet_length(payload::text)>4194304 THEN RAISE EXCEPTION 'invalid realtime event'; END IF;
			INSERT INTO loomplane_event_cursors (org_id,next_seq) VALUES (org,1)
				ON CONFLICT (org_id) DO UPDATE SET next_seq=loomplane_event_cursors.next_seq+1 RETURNING next_seq INTO seq;
			INSERT INTO loomplane_events (org_id,sequence,project_id,type,entity_id,data) VALUES (org,seq,project,event_type,entity,payload);
			PERFORM pg_notify('loomplane_events',json_build_object('org_id',org,'cursor',org||':'||seq)::text);
			RETURN seq;
		END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_state_event_trigger() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$ DECLARE doc JSONB; project TEXT; entity_type TEXT; event_type TEXT; BEGIN
			IF TG_OP='UPDATE' AND to_jsonb(OLD)=to_jsonb(NEW) THEN RETURN NULL; END IF;
			IF TG_OP='DELETE' THEN doc:=to_jsonb(OLD); ELSE doc:=to_jsonb(NEW); END IF;
			entity_type:=TG_ARGV[0]; project:=coalesce(doc->>'project_id','');
			IF entity_type='project' THEN
				project:=doc->>'id'; doc:=doc-'repository_url' || jsonb_build_object('repositories',coalesce((
					SELECT jsonb_agg(repository ORDER BY alias) FROM (SELECT DISTINCT ON(repository) repository,alias
					FROM loomplane_project_sources WHERE project_id=project AND removed_at IS NULL ORDER BY repository,alias) repositories),'[]'));
			ELSIF entity_type='analysis' THEN
				doc:=doc-'graph'-'attempts' || jsonb_build_object('source',coalesce((SELECT alias FROM loomplane_project_sources WHERE id=doc->>'source_id'),''));
			ELSIF entity_type='plan' THEN
				doc:=doc-'result' || jsonb_build_object('actions','[]'::jsonb,'problems','[]'::jsonb,'warnings','[]'::jsonb,'connections','{}'::jsonb,'summary','{}'::jsonb)
					|| coalesce(nullif(doc->'result','null'::jsonb),'{}') || jsonb_build_object('sources',coalesce((SELECT jsonb_agg(to_jsonb(ps)-'plan_id' ORDER BY alias) FROM loomplane_plan_sources ps WHERE plan_id=doc->>'id'),'[]'));
			ELSIF entity_type='binding' THEN doc:=doc-'replaced';
			ELSIF entity_type='manifest' THEN doc:=doc-'body' || jsonb_build_object('yaml',doc->'body');
			END IF;
			event_type:=entity_type||'.updated';
			IF TG_OP='DELETE' THEN event_type:=entity_type||'.deleted'; END IF;
			PERFORM loomplane_append_event(doc->>'org_id',project,event_type,doc->>'id',doc);
			RETURN NULL;
		END $$`,
	`DO $$ DECLARE table_name TEXT; entity TEXT; BEGIN
		FOR table_name,entity IN SELECT * FROM (VALUES ('loomplane_projects','project'),('loomplane_environments','environment'),
			('loomplane_analyses','analysis'),('loomplane_project_sources','source'),('loomplane_manifests','manifest'),
			('loomplane_plans','plan'),('loomplane_bindings','binding'),('loomplane_cloud_connections','connection'),('loomplane_deployments','deployment')) names(table_name,entity)
		LOOP
			EXECUTE format('CREATE OR REPLACE TRIGGER loomplane_state_event AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger(%L)',table_name,entity);
		END LOOP;
	END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_parent_event_trigger() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$ DECLARE child JSONB; doc JSONB; parent TEXT; BEGIN
			IF TG_OP='DELETE' THEN child:=to_jsonb(OLD); ELSE child:=to_jsonb(NEW); END IF;
			IF TG_TABLE_NAME='loomplane_project_sources' THEN
				parent:=child->>'project_id'; SELECT to_jsonb(p) INTO doc FROM loomplane_projects p WHERE id=parent;
				IF doc IS NULL THEN RETURN NULL; END IF;
				doc:=doc-'repository_url' || jsonb_build_object('repositories',coalesce((SELECT jsonb_agg(repository ORDER BY alias)
					FROM (SELECT DISTINCT ON(repository) repository,alias FROM loomplane_project_sources WHERE project_id=parent AND removed_at IS NULL ORDER BY repository,alias) sources),'[]'));
				PERFORM loomplane_append_event(doc->>'org_id',parent,'project.updated',parent,doc);
			ELSE
				parent:=child->>'plan_id'; SELECT to_jsonb(p) INTO doc FROM loomplane_plans p WHERE id=parent;
				IF doc IS NULL THEN RETURN NULL; END IF;
				doc:=doc-'result' || jsonb_build_object('actions','[]'::jsonb,'problems','[]'::jsonb,'warnings','[]'::jsonb,'connections','{}'::jsonb,'summary','{}'::jsonb)
					|| coalesce(nullif(doc->'result','null'::jsonb),'{}') || jsonb_build_object('sources',coalesce((SELECT jsonb_agg(to_jsonb(ps)-'plan_id' ORDER BY alias) FROM loomplane_plan_sources ps WHERE plan_id=parent),'[]'));
				PERFORM loomplane_append_event(doc->>'org_id',doc->>'project_id','plan.updated',parent,doc);
			END IF;
			RETURN NULL;
		END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_parent_event AFTER INSERT OR UPDATE OR DELETE ON loomplane_project_sources
		FOR EACH ROW EXECUTE FUNCTION loomplane_parent_event_trigger()`,
	`CREATE OR REPLACE TRIGGER loomplane_parent_event AFTER INSERT OR UPDATE OR DELETE ON loomplane_plan_sources
		FOR EACH ROW EXECUTE FUNCTION loomplane_parent_event_trigger()`,
	`CREATE TABLE IF NOT EXISTS loomplane_build_log_budgets (
		org_id TEXT NOT NULL,deployment_id TEXT NOT NULL,action_id TEXT NOT NULL,attempt TEXT NOT NULL,
		bytes BIGINT NOT NULL DEFAULT 0,truncated BOOLEAN NOT NULL DEFAULT FALSE,updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
		PRIMARY KEY(org_id,deployment_id,action_id,attempt)
	)`,
	`CREATE TABLE IF NOT EXISTS loomplane_build_logs (
		org_id TEXT NOT NULL,sequence BIGINT NOT NULL,project_id TEXT NOT NULL,deployment_id TEXT NOT NULL,
		action_id TEXT NOT NULL,attempt TEXT NOT NULL,operation_id TEXT NOT NULL,provider_id TEXT NOT NULL,
		stream TEXT NOT NULL,source TEXT NOT NULL,level TEXT NOT NULL,message TEXT NOT NULL,time TIMESTAMPTZ NOT NULL,
		truncated BOOLEAN NOT NULL DEFAULT FALSE,
		PRIMARY KEY(org_id,sequence),
		UNIQUE(org_id,deployment_id,action_id,attempt,operation_id,stream,provider_id),
		FOREIGN KEY(org_id,sequence) REFERENCES loomplane_events(org_id,sequence) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_build_logs_deployment ON loomplane_build_logs(org_id,deployment_id,sequence)`,
	`CREATE TABLE IF NOT EXISTS loomplane_log_checkpoints (
		org_id TEXT NOT NULL,project_id TEXT NOT NULL,deployment_id TEXT NOT NULL,action_id TEXT NOT NULL,attempt TEXT NOT NULL,operation_id TEXT NOT NULL,
		cursor TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'collecting',message TEXT NOT NULL DEFAULT '',updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY(org_id,deployment_id,action_id,attempt,operation_id)
	)`,
}

var realtimeDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_parent_event ON loomplane_project_sources`,
	`DROP TRIGGER IF EXISTS loomplane_parent_event ON loomplane_plan_sources`,
	`DROP FUNCTION IF EXISTS loomplane_parent_event_trigger()`,
	`DO $$ DECLARE name TEXT; BEGIN FOR name IN SELECT unnest(ARRAY['loomplane_projects','loomplane_environments','loomplane_analyses','loomplane_project_sources','loomplane_manifests','loomplane_plans','loomplane_bindings','loomplane_cloud_connections','loomplane_deployments']) LOOP
		EXECUTE format('DROP TRIGGER IF EXISTS loomplane_state_event ON %I',name); END LOOP; END $$`,
	`DROP FUNCTION IF EXISTS loomplane_state_event_trigger()`,
	`DROP FUNCTION IF EXISTS loomplane_append_event(TEXT,TEXT,TEXT,TEXT,JSONB)`,
	`DROP TABLE IF EXISTS loomplane_log_checkpoints`,
	`DROP TABLE IF EXISTS loomplane_build_logs`,
	`DROP TABLE IF EXISTS loomplane_build_log_budgets`,
	`DROP TABLE IF EXISTS loomplane_events`,
	`DROP TABLE IF EXISTS loomplane_event_cursors`,
}
