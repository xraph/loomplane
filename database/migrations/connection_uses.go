package migrations

// Foreign keys serialize connection deletion with plan/binding writes. The
// retained uses include targets, builders, registries and replacement cleanup.
var connectionUsesUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_connections_org_id ON loomplane_cloud_connections (org_id, id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_connection_uses (
		org_id TEXT NOT NULL, connection_id TEXT NOT NULL, kind TEXT NOT NULL, owner_id TEXT NOT NULL,
		PRIMARY KEY (kind, owner_id, connection_id),
		FOREIGN KEY (org_id, connection_id) REFERENCES loomplane_cloud_connections (org_id,id) ON DELETE RESTRICT
	)`,
	`CREATE OR REPLACE FUNCTION loomplane_connection_ids(doc JSONB) RETURNS SETOF TEXT
		LANGUAGE SQL IMMUTABLE AS $$
		SELECT DISTINCT value #>> '{}' FROM (
			SELECT jsonb_path_query(doc, '$.**.connection_id') AS value
			UNION ALL SELECT jsonb_path_query(doc, '$.**.target')
			UNION ALL SELECT jsonb_path_query(doc, '$.**.builder')
			UNION ALL SELECT jsonb_path_query(doc, '$.**.registry')
		) refs WHERE jsonb_typeof(value) = 'string' AND value #>> '{}' ~ '^conn_[0-7][0-9a-hjkmnp-tv-z]{25}$'
		$$`,
	`CREATE OR REPLACE FUNCTION loomplane_sync_plan_connection_uses(plan TEXT) RETURNS VOID
		LANGUAGE plpgsql AS $$
		DECLARE p loomplane_plans%ROWTYPE; doc JSONB;
		BEGIN
			DELETE FROM loomplane_connection_uses WHERE kind='plan' AND owner_id=plan;
			SELECT * INTO p FROM loomplane_plans WHERE id=plan;
			IF NOT FOUND OR p.status NOT IN ('planning','ready','approved') THEN RETURN; END IF;
			IF p.status='approved' AND EXISTS (SELECT 1 FROM loomplane_deployments WHERE plan_id=plan AND status='succeeded') THEN RETURN; END IF;
			doc := coalesce(p.result, '{}') || jsonb_build_object('defaults',
				coalesce((SELECT jsonb_agg(jsonb_build_object('connection_id', value)) FROM jsonb_each_text(coalesce(p.result->'connections','{}'))), '[]'));
			INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
				SELECT p.org_id,id,'plan',plan FROM loomplane_connection_ids(doc) id;
		END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_plan_connection_uses_trigger() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$ BEGIN
			IF TG_OP='DELETE' THEN DELETE FROM loomplane_connection_uses WHERE kind='plan' AND owner_id=OLD.id;
			ELSE PERFORM loomplane_sync_plan_connection_uses(NEW.id); END IF;
			RETURN NULL;
		END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_binding_connection_uses_trigger() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$ BEGIN
			IF TG_OP='DELETE' THEN DELETE FROM loomplane_connection_uses WHERE kind='binding' AND owner_id=OLD.id;
			ELSE
				DELETE FROM loomplane_connection_uses WHERE kind='binding' AND owner_id=NEW.id;
				INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
					SELECT NEW.org_id,id,'binding',NEW.id FROM loomplane_connection_ids(to_jsonb(NEW)) id;
			END IF;
			RETURN NULL;
		END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_deployment_connection_uses_trigger() RETURNS TRIGGER
		LANGUAGE plpgsql AS $$ BEGIN
			IF TG_OP='DELETE' THEN PERFORM loomplane_sync_plan_connection_uses(OLD.plan_id);
			ELSE PERFORM loomplane_sync_plan_connection_uses(NEW.plan_id); END IF;
			RETURN NULL;
		END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_plan_connection_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_plans
		FOR EACH ROW EXECUTE FUNCTION loomplane_plan_connection_uses_trigger()`,
	`CREATE OR REPLACE TRIGGER loomplane_binding_connection_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_bindings
		FOR EACH ROW EXECUTE FUNCTION loomplane_binding_connection_uses_trigger()`,
	`CREATE OR REPLACE TRIGGER loomplane_deployment_connection_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_deployments
		FOR EACH ROW EXECUTE FUNCTION loomplane_deployment_connection_uses_trigger()`,
	// Existing broken references stay visible to the planner. Only extant
	// connections can be backfilled into the deletion guard.
	`INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
		SELECT b.org_id,refs.id,'binding',b.id FROM loomplane_bindings b
		CROSS JOIN LATERAL loomplane_connection_ids(to_jsonb(b)) refs(id)
		JOIN loomplane_cloud_connections c ON c.org_id=b.org_id AND c.id=refs.id ON CONFLICT DO NOTHING`,
	`INSERT INTO loomplane_connection_uses (org_id,connection_id,kind,owner_id)
		SELECT p.org_id,refs.id,'plan',p.id FROM loomplane_plans p
		CROSS JOIN LATERAL loomplane_connection_ids(coalesce(p.result,'{}') || jsonb_build_object('defaults',
			coalesce((SELECT jsonb_agg(jsonb_build_object('connection_id', value)) FROM jsonb_each_text(coalesce(p.result->'connections','{}'))),'[]'))) refs(id)
		JOIN loomplane_cloud_connections c ON c.org_id=p.org_id AND c.id=refs.id
		WHERE p.status IN ('planning','ready','approved') AND NOT EXISTS (SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=p.id AND d.status='succeeded')
		ON CONFLICT DO NOTHING`,
}

var connectionUsesDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_plan_connection_uses ON loomplane_plans`,
	`DROP TRIGGER IF EXISTS loomplane_binding_connection_uses ON loomplane_bindings`,
	`DROP TRIGGER IF EXISTS loomplane_deployment_connection_uses ON loomplane_deployments`,
	`DROP FUNCTION IF EXISTS loomplane_plan_connection_uses_trigger()`,
	`DROP FUNCTION IF EXISTS loomplane_binding_connection_uses_trigger()`,
	`DROP FUNCTION IF EXISTS loomplane_deployment_connection_uses_trigger()`,
	`DROP FUNCTION IF EXISTS loomplane_sync_plan_connection_uses(TEXT)`,
	`DROP FUNCTION IF EXISTS loomplane_connection_ids(JSONB)`,
	`DROP TABLE IF EXISTS loomplane_connection_uses`,
	`DROP INDEX IF EXISTS loomplane_connections_org_id`,
}
