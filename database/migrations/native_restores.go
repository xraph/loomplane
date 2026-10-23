package migrations

var nativeRestoresUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_native_sources_restore_scope ON loomplane_native_sources(org_id,project_id,environment_id,id)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_native_points_restore_scope ON loomplane_native_points(org_id,source_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_native_restores (
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,
 source_id TEXT NOT NULL,point_id TEXT NOT NULL,request_id TEXT NOT NULL,
 source JSONB NOT NULL CHECK(jsonb_typeof(source)='object'),point JSONB NOT NULL CHECK(jsonb_typeof(point)='object'),
 target JSONB NOT NULL CHECK(jsonb_typeof(target)='object'),checkpoint JSONB NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(checkpoint)='object'),
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','deleting','cleanup_failed','deleted')),
 token TEXT NOT NULL DEFAULT '',lease_until TIMESTAMPTZ,
 external_id TEXT NOT NULL DEFAULT '',physical_id TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',
 created_by TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL,expires_at TIMESTAMPTZ NOT NULL CHECK(expires_at>created_at),
 verified_at TIMESTAMPTZ,duration_ms BIGINT NOT NULL DEFAULT 0 CHECK(duration_ms>=0),
 UNIQUE(org_id,project_id,request_id),
 FOREIGN KEY(org_id,project_id,environment_id,source_id) REFERENCES loomplane_native_sources(org_id,project_id,environment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,source_id,point_id) REFERENCES loomplane_native_points(org_id,source_id,id) ON DELETE RESTRICT
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_native_restores_scope ON loomplane_native_restores(org_id,project_id,environment_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS loomplane_native_restores_pending ON loomplane_native_restores(status,lease_until,expires_at)`,
	`CREATE OR REPLACE FUNCTION loomplane_native_restore_connection_uses() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN DELETE FROM loomplane_connection_uses WHERE kind='native_restore' AND owner_id=OLD.id;
 ELSE
 DELETE FROM loomplane_connection_uses WHERE kind='native_restore' AND owner_id=NEW.id;
 IF NEW.status<>'deleted' THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
 SELECT NEW.org_id,id,'native_restore',NEW.id FROM (SELECT NEW.source->>'connection_id' AS id UNION SELECT NEW.target->>'connection_id') refs;
 END IF;
 END IF;
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_native_restore_connection_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_native_restores FOR EACH ROW EXECUTE FUNCTION loomplane_native_restore_connection_uses()`,
}

var nativeRestoresDown = []string{`DO $native_restores_down$ BEGIN
 IF to_regclass('loomplane_native_restores') IS NOT NULL THEN
 LOCK TABLE loomplane_native_restores IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_native_restores) THEN RAISE EXCEPTION 'native restore history is still retained' USING ERRCODE='55000'; END IF;
 DROP TRIGGER IF EXISTS loomplane_native_restore_connection_uses ON loomplane_native_restores;
 DROP TABLE loomplane_native_restores;
 END IF;
 DROP FUNCTION IF EXISTS loomplane_native_restore_connection_uses();
 DROP INDEX IF EXISTS loomplane_native_points_restore_scope;
 DROP INDEX IF EXISTS loomplane_native_sources_restore_scope;
 END $native_restores_down$`}
