package migrations

var nativeRetirementsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_native_retirements(
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,source_id TEXT NOT NULL,point_id TEXT NOT NULL,witness_id TEXT NOT NULL CHECK(point_id<>witness_id),
 request_id TEXT NOT NULL,created_by TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),completed_at TIMESTAMPTZ,
 status TEXT NOT NULL CHECK(status IN ('queued','running','failed','succeeded')),error TEXT NOT NULL DEFAULT '',
 policy JSONB NOT NULL CHECK(jsonb_typeof(policy)='object'),source JSONB NOT NULL CHECK(jsonb_typeof(source)='object'),point JSONB NOT NULL CHECK(jsonb_typeof(point)='object'),witness JSONB NOT NULL CHECK(jsonb_typeof(witness)='object'),
 attempted BOOLEAN NOT NULL DEFAULT false,token TEXT NOT NULL DEFAULT '',lease_until TIMESTAMPTZ,
 policy_id TEXT NOT NULL DEFAULT '',policy_revision BIGINT NOT NULL DEFAULT 0,
 UNIQUE(org_id,source_id,request_id),UNIQUE(org_id,source_id,point_id),
 FOREIGN KEY(org_id,source_id) REFERENCES loomplane_native_sources(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,source_id,point_id) REFERENCES loomplane_native_points(org_id,source_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,source_id,witness_id) REFERENCES loomplane_native_points(org_id,source_id,id) ON DELETE RESTRICT
 )`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_native_retirements_active ON loomplane_native_retirements(org_id,source_id) WHERE status<>'succeeded'`,
	`CREATE INDEX IF NOT EXISTS loomplane_native_retirements_work ON loomplane_native_retirements(status,created_at,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_native_retirement_events(id BIGSERIAL PRIMARY KEY,org_id TEXT NOT NULL,retirement_id TEXT NOT NULL REFERENCES loomplane_native_retirements(id) ON DELETE RESTRICT,kind TEXT NOT NULL,status TEXT NOT NULL,actor TEXT NOT NULL,error TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp())`,
	`CREATE INDEX IF NOT EXISTS loomplane_native_retirement_events_scope ON loomplane_native_retirement_events(org_id,retirement_id,id DESC)`,
	`CREATE OR REPLACE FUNCTION loomplane_native_retirement_connection_uses() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN DELETE FROM loomplane_connection_uses WHERE kind='native_retirement' AND owner_id=OLD.id;
 ELSE
 DELETE FROM loomplane_connection_uses WHERE kind='native_retirement' AND owner_id=NEW.id;
 IF NEW.status<>'succeeded' THEN INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.source->>'connection_id','native_retirement',NEW.id); END IF;
 END IF; RETURN NULL; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_native_retirement_connection_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_native_retirements FOR EACH ROW EXECUTE FUNCTION loomplane_native_retirement_connection_uses()`,
}
var nativeRetirementsDown = []string{`DO $native_retirements_down$ BEGIN
 IF to_regclass('loomplane_native_retirements') IS NOT NULL THEN
 LOCK TABLE loomplane_native_retirements IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_native_retirements) THEN RAISE EXCEPTION 'native deletion history is retained' USING ERRCODE='55000'; END IF;
 END IF;
 DROP TABLE IF EXISTS loomplane_native_retirement_events;
 DROP TABLE IF EXISTS loomplane_native_retirements;
 DROP FUNCTION IF EXISTS loomplane_native_retirement_connection_uses();
 END $native_retirements_down$`}
