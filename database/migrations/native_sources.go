package migrations

var nativeSourcesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_native_sources (
 id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 binding_id TEXT NOT NULL, name TEXT NOT NULL, provider TEXT NOT NULL,
 connection_id TEXT NOT NULL, region TEXT NOT NULL, physical_id TEXT NOT NULL CHECK(physical_id<>''),
 source JSONB NOT NULL CHECK(jsonb_typeof(source)='object' AND source->>'physical_id'=physical_id),
 captured_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(org_id,binding_id,provider,connection_id,region,physical_id),
 UNIQUE(org_id,id),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE CASCADE
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_native_sources_project ON loomplane_native_sources(org_id,project_id,captured_at,id)`,
	`CREATE OR REPLACE FUNCTION loomplane_capture_native_sources(root JSONB) RETURNS void LANGUAGE sql AS $capture_native_sources$
 WITH RECURSIVE history(source) AS (
 SELECT root
 UNION ALL
 SELECT child.value FROM history h CROSS JOIN LATERAL jsonb_array_elements(
 CASE WHEN jsonb_typeof(h.source->'replaced')='array' THEN h.source->'replaced' ELSE '[]'::jsonb END) child
 )
 INSERT INTO loomplane_native_sources(org_id,project_id,environment_id,binding_id,name,provider,connection_id,region,physical_id,source)
 SELECT source->>'org_id',source->>'project_id',source->>'environment_id',source->>'id',source->>'name',source->>'provider',source->>'connection_id',source->>'region',source->>'physical_id',source-'url'-'replaced'-'drift_message'
 FROM history WHERE source->>'subject'='resource' AND source->>'provider' IN ('rds','cloud-sql') AND coalesce(source->>'physical_id','')<>''
 AND source->>'org_id'=root->>'org_id' AND source->>'project_id'=root->>'project_id' AND source->>'environment_id'=root->>'environment_id'
 ON CONFLICT(org_id,binding_id,provider,connection_id,region,physical_id) DO NOTHING
 $capture_native_sources$`,
	`CREATE OR REPLACE FUNCTION loomplane_binding_native_sources() RETURNS trigger LANGUAGE plpgsql AS $binding_native_sources$
 BEGIN
 IF TG_OP='UPDATE' THEN PERFORM loomplane_capture_native_sources(to_jsonb(OLD)); END IF;
 PERFORM loomplane_capture_native_sources(to_jsonb(NEW));
 RETURN NEW;
 END $binding_native_sources$`,
	`DROP TRIGGER IF EXISTS loomplane_binding_native_sources ON loomplane_bindings`,
	`CREATE TRIGGER loomplane_binding_native_sources AFTER INSERT OR UPDATE ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION loomplane_binding_native_sources()`,
	`SELECT loomplane_capture_native_sources(to_jsonb(b)) FROM loomplane_bindings b`,
}

var nativeSourcesDown = []string{`DO $native_sources_down$ BEGIN
 IF to_regclass('loomplane_native_sources') IS NOT NULL THEN
 LOCK TABLE loomplane_native_sources IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_native_sources) THEN RAISE EXCEPTION 'native recovery sources are still retained' USING ERRCODE='55000'; END IF;
 DROP TRIGGER IF EXISTS loomplane_binding_native_sources ON loomplane_bindings;
 DROP FUNCTION IF EXISTS loomplane_binding_native_sources();
 DROP FUNCTION IF EXISTS loomplane_capture_native_sources(JSONB);
 DROP TABLE loomplane_native_sources;
 END IF; END $native_sources_down$`}
