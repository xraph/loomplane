package migrations

import "strings"

var cloudCapacityIntentsUp = func() []string {
	out := []string{
		`CREATE TABLE loomplane_capacity_intents (
 org_id TEXT NOT NULL, plan_id TEXT NOT NULL REFERENCES loomplane_plans(id) ON DELETE RESTRICT, service TEXT NOT NULL,
 plan_revision TEXT NOT NULL, body JSONB NOT NULL, actor JSONB NOT NULL,
 PRIMARY KEY(org_id,plan_id,service))`,
		`CREATE TABLE loomplane_capacity_placements (
 org_id TEXT NOT NULL, plan_id TEXT NOT NULL, service TEXT NOT NULL, intent_hash TEXT NOT NULL, body JSONB NOT NULL,
 PRIMARY KEY(org_id,plan_id,service), FOREIGN KEY(org_id,plan_id,service) REFERENCES loomplane_capacity_intents(org_id,plan_id,service))`,
		`CREATE FUNCTION loomplane_capacity_intent_current(p loomplane_plans, i JSONB) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT coalesce(i->'demand'->>'org_id'=p.org_id AND i->'demand'->>'project_id'=p.project_id AND i->'demand'->>'environment_id'=p.environment_id AND i->'demand'->>'plan_id'=p.id
 AND EXISTS(SELECT 1 FROM loomplane_cloud_targets t JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=p.project_id AND NOT g.revoked
 JOIN loomplane_cloud_assignments a ON a.org_id=t.org_id AND a.project_id=p.project_id AND a.environment_id=p.environment_id AND a.cloud_id=t.cloud_id
 JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
 JOIN loomplane_cloud_capacity_policies cp ON cp.org_id=t.org_id AND cp.cloud_id=t.cloud_id
 JOIN loomplane_cloud_connections pc ON pc.org_id=t.org_id AND pc.id=i->'template'->>'connection_id' AND pc.generation=(i->'provider_pin'->>'connection_generation')::bigint AND (CASE pc.cloud WHEN 'digitalocean' THEN coalesce(nullif(pc.details->>'team_uuid',''),pc.details->>'account_uuid') WHEN 'vercel' THEN coalesce(nullif(pc.details->>'team_id',''),pc.details->>'user_id') ELSE pc.account END)=i->'provider_pin'->>'account_id' AND pc.status='ready'
 WHERE t.org_id=p.org_id AND t.id=i->'demand'->>'target_id' AND t.cloud_id=i->'demand'->>'cloud_id' AND t.pool_id=i->'demand'->>'pool_id'
 AND t.provider='server' AND t.revision=(i->>'target_revision')::bigint AND a.revision=(i->>'assignment_revision')::bigint AND c.revision=(i->>'cloud_revision')::bigint AND g.revision=(i->>'grant_revision')::bigint
 AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)) AND cp.enabled
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(cp.body->'pools') template WHERE template->>'pool_id'=t.pool_id AND template-ARRAY['min_servers','max_servers','limits'] = (i->'template')-ARRAY['min_servers','max_servers','limits'])) ,false) $$`,
		`CREATE FUNCTION loomplane_capacity_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 DECLARE a JSONB; i JSONB; actor JSONB;
 BEGIN
 IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM loomplane_capacity_intents WHERE org_id=OLD.org_id AND plan_id=OLD.id)
 AND (NEW.org_id,NEW.project_id,NEW.environment_id,NEW.revision,NEW.result,NEW.decided_by) IS DISTINCT FROM (OLD.org_id,OLD.project_id,OLD.environment_id,OLD.revision,OLD.result,OLD.decided_by)
 THEN RAISE EXCEPTION 'approved capacity intent is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.status<>'approved' OR (TG_OP='UPDATE' AND OLD.status='approved') THEN RETURN NEW; END IF;
 FOR a IN SELECT value FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) LOOP
 i:=a->'service'->'capacity_intent';
 IF i IS NULL OR i='null'::jsonb THEN CONTINUE; END IF;
 IF coalesce(NEW.result->>'cloud_sync_id','')<>'' THEN
 IF NOT loomplane_accepted_pool_sync(NEW) THEN RAISE EXCEPTION 'deferred sync needs its immutable accepted snapshot' USING ERRCODE='23514'; END IF;
 SELECT snapshot->'actor' INTO actor FROM loomplane_cloud_syncs WHERE org_id=NEW.org_id AND id=NEW.result->>'cloud_sync_id';
 ELSE actor:=nullif(current_setting('loomplane.capacity_actor',true),'')::jsonb; END IF;
 IF actor IS NULL OR actor->>'OrgID' IS DISTINCT FROM NEW.org_id OR actor->>'UserID' IS DISTINCT FROM NEW.decided_by OR coalesce(actor->>'UserID','')='' OR coalesce((actor->>'ReadOnly')::boolean,true) THEN RAISE EXCEPTION 'deferred approval needs its authenticated actor' USING ERRCODE='23514'; END IF;
 IF NOT NEW.executable OR NOT coalesce((NEW.result->>'executable')::boolean,false) OR NOT loomplane_capacity_intent_current(NEW,i) OR a->>'provider' IS DISTINCT FROM 'server' OR a->>'subject' IS DISTINCT FROM 'service' OR coalesce(a->>'op','') NOT IN ('create','replace','update')
 OR coalesce(a->>'connection_id','')<>'' OR coalesce(a->'service'->>'target','')<>'' OR coalesce(a->'service'->'cloud_placement','null')<>'null'::jsonb
 OR i->'demand'->>'service' IS DISTINCT FROM a->>'name' OR coalesce(i->>'commit_sha','')='' OR i->>'commit_sha' IS DISTINCT FROM a->'service'->>'commit_sha'
 OR coalesce(i->>'template_hash','')='' OR coalesce(i->'demand'->>'intent_hash','')='' OR coalesce(i->'demand'->>'architecture','')='' OR i->'demand'->>'architecture' IS DISTINCT FROM i->'template'->>'architecture'
 OR coalesce((i->'demand'->>'local_data')::boolean,true) OR coalesce(i->'demand'->>'sticky_server_id','')<>''
 OR coalesce((i->'demand'->'resources'->>'servers')::bigint,-1)<>0 OR coalesce((i->'demand'->'resources'->>'storage_bytes')::bigint,-1)<>0
 OR coalesce((i->'demand'->'resources'->>'cpu_millis')::bigint,0)<>coalesce((a->'service'->'size'->>'cpu')::numeric*1000,1000)
 OR coalesce((i->'demand'->'resources'->>'memory_bytes')::bigint,0)<>coalesce((a->'service'->'size'->>'memory')::bigint*1048576,536870912)
 OR coalesce((i->'demand'->'resources'->>'cpu_millis')::bigint,0)<=0 OR coalesce((i->'demand'->'resources'->>'memory_bytes')::bigint,0)<=0
 OR coalesce((a->'service'->'scale'->>'min')::int,1)<>1 OR coalesce((a->'service'->'scale'->>'max')::int,1)<>1
 OR coalesce(a->'service'->'catalog','null')<>'null'::jsonb OR jsonb_array_length(coalesce(a->'routes','[]'))>0
 THEN RAISE EXCEPTION 'invalid deferred capacity intent' USING ERRCODE='23514'; END IF;
 INSERT INTO loomplane_capacity_intents(org_id,plan_id,service,plan_revision,body,actor) VALUES(NEW.org_id,NEW.id,a->>'name',NEW.revision,i,actor);
 END LOOP; RETURN NEW; END $$`,
		// Persist after the plan row exists, before ordinary pool admission runs.
		`CREATE TRIGGER loomplane_capacity_intent_approval AFTER INSERT OR UPDATE ON loomplane_plans FOR EACH ROW EXECUTE FUNCTION loomplane_capacity_intent_guard()`,
		`CREATE FUNCTION loomplane_capacity_saved_immutable() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'capacity approval and placement records are immutable' USING ERRCODE='23514'; END $$`,
		`CREATE TRIGGER loomplane_capacity_intent_immutable BEFORE UPDATE OR DELETE ON loomplane_capacity_intents FOR EACH ROW EXECUTE FUNCTION loomplane_capacity_saved_immutable()`,
		`CREATE TRIGGER loomplane_capacity_placement_immutable BEFORE UPDATE OR DELETE ON loomplane_capacity_placements FOR EACH ROW EXECUTE FUNCTION loomplane_capacity_saved_immutable()`,
	}
	// Both ordinary approvals and deferred conversion run the same host checks.
	for _, sql := range cloudCompensationUp {
		if !strings.HasPrefix(sql, "CREATE OR REPLACE FUNCTION loomplane_reserve_pool_plan() ") {
			continue
		}
		sql = strings.Replace(sql, "loomplane_reserve_pool_plan() RETURNS TRIGGER", "loomplane_admit_capacity_plan(candidate loomplane_plans, approved loomplane_plans) RETURNS VOID", 1)
		sql = strings.Replace(sql, "IF NEW.status<>'approved' OR (TG_OP='UPDATE' AND OLD.status='approved') THEN RETURN NEW; END IF;", "", 1)
		sql = strings.ReplaceAll(sql, "NEW.", "candidate.")
		sql = strings.ReplaceAll(sql, "loomplane_accepted_pool_sync(NEW)", "loomplane_accepted_pool_sync(approved)")
		sql = strings.ReplaceAll(sql, "loomplane_retained_compensation(NEW,a)", "loomplane_retained_compensation(approved,a)")
		sql = strings.Replace(sql, "pin:=a->'service'->'cloud_placement';", `IF coalesce(a->'service'->'capacity_intent','null')<>'null'::jsonb THEN
 IF NOT EXISTS(SELECT 1 FROM loomplane_capacity_intents i WHERE i.org_id=approved.org_id AND i.plan_id=approved.id AND i.service=a->>'name' AND i.plan_revision=approved.revision AND i.body=a->'service'->'capacity_intent') THEN RAISE EXCEPTION 'missing approved deferred intent' USING ERRCODE='23514'; END IF;
 CONTINUE; END IF;
 pin:=a->'service'->'cloud_placement';`, 1)
		sql = strings.ReplaceAll(sql, "RETURN NEW;", "RETURN;")
		out = append(out, sql)
	}
	out = append(out, `CREATE OR REPLACE FUNCTION loomplane_reserve_pool_plan() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.status='approved' AND (TG_OP='INSERT' OR OLD.status<>'approved') THEN PERFORM loomplane_admit_capacity_plan(NEW,NEW); END IF; RETURN NEW; END $$`)
	return out
}()
var cloudCapacityIntentsDown = func() []string {
	out := []string{`DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_capacity_intents) THEN RAISE EXCEPTION 'capacity intent approvals must be retained'; END IF; END $$`}
	for _, sql := range cloudCompensationUp {
		if strings.HasPrefix(sql, "CREATE OR REPLACE FUNCTION loomplane_reserve_pool_plan() ") {
			out = append(out, sql)
		}
	}
	return append(out,
		`DROP TRIGGER loomplane_capacity_intent_approval ON loomplane_plans`,
		`DROP TABLE loomplane_capacity_placements`,
		`DROP TABLE loomplane_capacity_intents`,
		`DROP FUNCTION loomplane_capacity_intent_guard()`,
		`DROP FUNCTION loomplane_capacity_saved_immutable()`,
		`DROP FUNCTION loomplane_admit_capacity_plan(loomplane_plans,loomplane_plans)`,
		`DROP FUNCTION loomplane_capacity_intent_current(loomplane_plans,JSONB)`)
}()
