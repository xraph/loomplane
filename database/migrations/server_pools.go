package migrations

// serverCapacitySQL counts each retained allocation separately. Legacy in-place
// deployments share one allocation and keep their maximum demand.
const serverCapacitySQL = `CREATE OR REPLACE FUNCTION loomplane_server_capacity(host_id TEXT, extra_environment TEXT DEFAULT '', extra JSONB DEFAULT '[]') RETURNS TABLE(cpu BIGINT,memory BIGINT) LANGUAGE SQL STABLE AS $$
 SELECT coalesce(sum(c),0)::bigint,coalesce(sum(m),0)::bigint FROM (
 SELECT max(cpu_millis) c,max(memory_bytes) m FROM (
 SELECT environment_id,service,coalesce(body->>'allocation','') allocation,cpu_millis,memory_bytes FROM loomplane_capacity_reservations WHERE server_id=host_id
 UNION ALL SELECT extra_environment,p->>'service',coalesce(p->>'allocation',''),(p->>'cpu_millis')::bigint,(p->>'memory_bytes')::bigint FROM jsonb_array_elements(extra) p WHERE p->>'server_id'=host_id
 ) demands GROUP BY environment_id,service,allocation) allocated $$`

var serverPoolsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_placement_moves(org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,service TEXT NOT NULL,cloud_id TEXT NOT NULL,pool_id TEXT NOT NULL,source_server_id TEXT NOT NULL,destination_server_id TEXT NOT NULL,review TEXT NOT NULL,expires_at TIMESTAMPTZ NOT NULL,source_config JSONB NOT NULL,PRIMARY KEY(org_id,environment_id,service))`,
	`CREATE TABLE IF NOT EXISTS loomplane_server_pools(id TEXT PRIMARY KEY,org_id TEXT NOT NULL,cloud_id TEXT NOT NULL,revision BIGINT NOT NULL CHECK(revision>0),body JSONB NOT NULL,UNIQUE(org_id,cloud_id,id),FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id) ON DELETE RESTRICT)`,
	`CREATE TABLE IF NOT EXISTS loomplane_server_pool_members(org_id TEXT NOT NULL,pool_id TEXT NOT NULL REFERENCES loomplane_server_pools(id) ON DELETE CASCADE,server_id TEXT NOT NULL REFERENCES loomplane_cloud_servers(id),cpu_millis BIGINT NOT NULL CHECK(cpu_millis>0),memory_bytes BIGINT NOT NULL CHECK(memory_bytes>0),draining BOOLEAN NOT NULL DEFAULT false,PRIMARY KEY(pool_id,server_id))`,
	`ALTER TABLE loomplane_cloud_targets ADD COLUMN IF NOT EXISTS runtime TEXT NOT NULL DEFAULT 'managed'`,
	`ALTER TABLE loomplane_cloud_targets ADD COLUMN IF NOT EXISTS pool_id TEXT`,
	`ALTER TABLE loomplane_cloud_targets ALTER COLUMN connection_id DROP NOT NULL`,
	`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_target_pool_scope') THEN ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_target_pool_scope FOREIGN KEY(org_id,cloud_id,pool_id) REFERENCES loomplane_server_pools(org_id,cloud_id,id); END IF; END $$`,
	`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_target_runtime') THEN ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_target_runtime CHECK((runtime='managed' AND pool_id IS NULL AND connection_id IS NOT NULL) OR (runtime='server' AND pool_id IS NOT NULL AND connection_id IS NULL AND provider='server')); END IF; END $$`,
	`CREATE TABLE IF NOT EXISTS loomplane_capacity_reservations(org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,service TEXT NOT NULL,plan_id TEXT NOT NULL REFERENCES loomplane_plans(id) ON DELETE RESTRICT,pool_id TEXT NOT NULL REFERENCES loomplane_server_pools(id),server_id TEXT NOT NULL REFERENCES loomplane_cloud_servers(id),connection_id TEXT NOT NULL,cpu_millis BIGINT NOT NULL CHECK(cpu_millis>0),memory_bytes BIGINT NOT NULL CHECK(memory_bytes>0),binding_id TEXT NOT NULL DEFAULT '',body JSONB NOT NULL,PRIMARY KEY(plan_id,service,server_id))`,
	serverCapacitySQL,
	`CREATE INDEX IF NOT EXISTS loomplane_capacity_server ON loomplane_capacity_reservations(org_id,server_id)`,
	// A daemon is a single capacity domain, even if two SSH connections reach it.
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_server_docker_identity ON loomplane_cloud_servers((body->'health'->>'docker_id')) WHERE status NOT IN ('deleted','disconnected') AND coalesce(body->'health'->>'docker_id','')<>''`,
	`CREATE OR REPLACE FUNCTION loomplane_reserve_pool_plan() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 DECLARE a JSONB; pin JSONB; host loomplane_cloud_servers%ROWTYPE; member loomplane_server_pool_members%ROWTYPE; pool loomplane_server_pools%ROWTYPE; cpu BIGINT; mem BIGINT; used_cpu BIGINT; used_mem BIGINT;
 BEGIN
 IF NEW.status<>'approved' OR (TG_OP='UPDATE' AND OLD.status='approved') THEN RETURN NEW; END IF;
 -- Order host and connection locks consistently across every accepted plan.
 PERFORM 1 FROM loomplane_cloud_connections c WHERE c.org_id=NEW.org_id AND c.id IN (SELECT x->>'connection_id' FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) x) ORDER BY c.id FOR UPDATE;
 PERFORM pg_advisory_xact_lock(hashtextextended('host-admission/'||identity,0)) FROM (SELECT DISTINCT c.details->>'docker_id' identity FROM loomplane_cloud_connections c WHERE c.org_id=NEW.org_id AND c.cloud='server' AND coalesce(c.details->>'docker_id','')<>'' AND c.id IN (SELECT x->>'connection_id' FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) x) ORDER BY identity) identities;
 PERFORM 1 FROM loomplane_cloud_servers s WHERE s.org_id=NEW.org_id AND s.id IN (SELECT x->'service'->'cloud_placement'->>'server_id' FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) x) ORDER BY s.id FOR UPDATE;
 FOR a IN SELECT value FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) LOOP
 IF a->>'op'='delete' OR (a->>'op'='keep' AND NOT coalesce((a->>'adopt_cloud_placement')::boolean,false)) THEN CONTINUE; END IF;
 pin:=a->'service'->'cloud_placement';
 IF pin ? 'allocation' AND (jsonb_typeof(pin->'allocation')<>'string' OR length(pin->>'allocation')>200 OR pin->>'allocation'<>btrim(pin->>'allocation')) THEN RAISE EXCEPTION 'allocation must be a bounded string' USING ERRCODE='23514'; END IF;
 IF coalesce(pin->>'pool_id','')='' THEN
 IF a->>'provider'='server' AND EXISTS(SELECT 1 FROM loomplane_server_pool_members m JOIN loomplane_cloud_servers s ON s.id=m.server_id JOIN loomplane_cloud_connections c ON c.org_id=NEW.org_id AND c.id=a->>'connection_id' WHERE (s.runtime_connection_id=c.id OR (coalesce(c.details->>'docker_id','')<>'' AND c.details->>'docker_id'=s.body->'health'->>'docker_id')) AND s.status NOT IN ('deleted','disconnected')) THEN RAISE EXCEPTION 'pooled capacity requires a pool target' USING ERRCODE='23514'; END IF;
 CONTINUE; END IF;
 IF coalesce(NEW.result->>'cloud_sync_id','')<>'' AND NOT loomplane_accepted_pool_sync(NEW) THEN RAISE EXCEPTION 'pool sync needs its exact immutable accepted participant plan' USING ERRCODE='23514'; END IF;
 IF jsonb_array_length(coalesce(a->'routes','[]'))>0 AND (
 coalesce(pin->>'allocation','')='' OR EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=NEW.org_id AND b.project_id=NEW.project_id AND b.environment_id=NEW.environment_id AND b.subject='service' AND b.name=a->>'name' AND (coalesce(b.config->'cloud_placement'->>'allocation','')=pin->>'allocation' OR (b.config->'cloud_placement' ? 'allocation' AND jsonb_typeof(b.config->'cloud_placement'->'allocation')<>'string') OR (coalesce(b.config->'cloud_placement'->>'pool_id','')<>'' AND NOT EXISTS(SELECT 1 FROM loomplane_capacity_reservations r WHERE r.org_id=b.org_id AND r.project_id=b.project_id AND r.environment_id=b.environment_id AND r.service=b.name AND r.plan_id=b.plan_id AND r.server_id=b.config->'cloud_placement'->>'server_id' AND r.binding_id=b.id AND r.body=b.config->'cloud_placement'))))) THEN RAISE EXCEPTION 'routed allocation must retain the exact source and reserve a distinct candidate' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id AND service=a->>'name' AND binding_id='' AND plan_id<>NEW.id) THEN RAISE EXCEPTION 'recover the unresolved accepted placement before another deployment' USING ERRCODE='23514'; END IF;
 IF coalesce((pin->>'pool_revision')::bigint,0)<1 OR coalesce((pin->>'server_revision')::bigint,0)<1 OR coalesce((pin->>'connection_generation')::bigint,0)<1 OR coalesce((pin->>'cpu_millis')::bigint,0)<1 OR coalesce((pin->>'memory_bytes')::bigint,0)<1 OR coalesce((a->'service'->'scale'->>'min')::int,1)<>1 OR coalesce((a->'service'->'scale'->>'max')::int,1)<>1 THEN RAISE EXCEPTION 'pool placement needs bounded resources and one replica' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=NEW.org_id AND b.environment_id=NEW.environment_id AND b.subject='service' AND b.name=a->>'name' AND coalesce(b.config->'cloud_placement'->>'server_id','')<>pin->>'server_id' AND (coalesce(b.config->'catalog','null')<>'null'::jsonb OR (NOT loomplane_accepted_pool_sync(NEW) AND NOT EXISTS(SELECT 1 FROM loomplane_placement_moves m WHERE m.org_id=b.org_id AND m.project_id=b.project_id AND m.environment_id=b.environment_id AND m.service=b.name AND m.source_server_id=b.config->'cloud_placement'->>'server_id' AND m.destination_server_id=pin->>'server_id' AND m.source_config=b.config AND m.expires_at>now())))) THEN RAISE EXCEPTION 'host relocation needs a current explicit stateless move review' USING ERRCODE='23514'; END IF;
 SELECT * INTO host FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND id=pin->>'server_id';
 SELECT * INTO pool FROM loomplane_server_pools WHERE org_id=NEW.org_id AND cloud_id=pin->>'cloud_id' AND id=pin->>'pool_id' FOR SHARE;
 SELECT * INTO member FROM loomplane_server_pool_members WHERE org_id=NEW.org_id AND pool_id=pool.id AND server_id=host.id;
 IF host.id IS NULL OR pool.id IS NULL OR member.server_id IS NULL OR host.status<>'ready' OR host.revision<>(pin->>'server_revision')::bigint OR pool.revision<>(pin->>'pool_revision')::bigint OR host.runtime_connection_id<>pin->>'connection_id' OR a->>'connection_id'<>pin->>'connection_id' OR a->>'provider'<>'server' THEN RAISE EXCEPTION 'pool placement changed; review again' USING ERRCODE='23514'; END IF;
 IF (coalesce((host.body->>'draining')::boolean,false) OR EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE server_id=host.id AND draining)) AND NOT EXISTS(SELECT 1 FROM loomplane_bindings WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id AND subject='service' AND name=a->>'name' AND config->'cloud_placement'->>'server_id'=host.id) THEN RAISE EXCEPTION 'server is draining' USING ERRCODE='23514'; END IF;
 IF (host.body->'health'->>'checked_at')::timestamptz < now()-interval '15 minutes' OR (host.body->'health'->>'checked_at')::timestamptz > now()+interval '1 minute' OR coalesce((host.body->'health'->>'effective_cpu_millis')::bigint,0)<member.cpu_millis OR coalesce((host.body->'health'->>'effective_memory_bytes')::bigint,0)<member.memory_bytes THEN RAISE EXCEPTION 'effective host capacity needs a fresh check' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM loomplane_cloud_connections c WHERE c.org_id=NEW.org_id AND c.id=host.runtime_connection_id AND c.status='ready' AND c.generation=(pin->>'connection_generation')::bigint) THEN RAISE EXCEPTION 'runtime connection changed' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM loomplane_cloud_project_grants WHERE org_id=NEW.org_id AND cloud_id=pin->>'cloud_id' AND project_id=NEW.project_id FOR SHARE;
 PERFORM 1 FROM loomplane_cloud_targets WHERE org_id=NEW.org_id AND id=pin->>'target_id' FOR SHARE;
 PERFORM 1 FROM loomplane_cloud_assignments WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id FOR SHARE;
 PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=NEW.org_id AND id=pin->>'cloud_id' FOR SHARE;
 IF NOT EXISTS(SELECT 1 FROM loomplane_cloud_targets t JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=NEW.project_id AND NOT g.revoked JOIN loomplane_cloud_assignments ca ON ca.org_id=t.org_id AND ca.project_id=NEW.project_id AND ca.environment_id=NEW.environment_id AND ca.cloud_id=t.cloud_id JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id WHERE t.org_id=NEW.org_id AND t.id=pin->>'target_id' AND t.pool_id=pool.id AND t.revision=(pin->>'target_revision')::bigint AND ca.revision=(pin->>'assignment_revision')::bigint AND c.revision=(pin->>'cloud_revision')::bigint AND g.revision=(pin->>'grant_revision')::bigint AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))) THEN RAISE EXCEPTION 'pool target authority changed' USING ERRCODE='23514'; END IF;
 cpu:=coalesce((a->'service'->'size'->>'cpu')::numeric*1000,1000);mem:=coalesce((a->'service'->'size'->>'memory')::bigint*1048576,536870912);
 IF cpu<=0 OR mem<=0 OR cpu<>(pin->>'cpu_millis')::bigint OR mem<>(pin->>'memory_bytes')::bigint THEN RAISE EXCEPTION 'capacity differs from runtime limits' USING ERRCODE='23514'; END IF;
 SELECT c.cpu,c.memory INTO used_cpu,used_mem FROM loomplane_server_capacity(host.id,NEW.environment_id,jsonb_build_array(jsonb_build_object('service',a->>'name','server_id',host.id,'allocation',coalesce(pin->>'allocation',''),'cpu_millis',cpu,'memory_bytes',mem))) c;
 IF used_cpu>member.cpu_millis OR used_mem>member.memory_bytes THEN RAISE EXCEPTION 'insufficient reserved host capacity' USING ERRCODE='23514'; END IF;
 INSERT INTO loomplane_capacity_reservations(org_id,project_id,environment_id,service,plan_id,pool_id,server_id,connection_id,cpu_millis,memory_bytes,body) VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,a->>'name',NEW.id,pool.id,host.id,host.runtime_connection_id,cpu,mem,pin) ON CONFLICT DO NOTHING;
 END LOOP;
 RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_pool_plan_reserve AFTER INSERT OR UPDATE ON loomplane_plans FOR EACH ROW EXECUTE FUNCTION loomplane_reserve_pool_plan()`,
	`CREATE OR REPLACE FUNCTION loomplane_pool_binding_capacity() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 DECLARE pin JSONB;
 BEGIN
 IF TG_OP='DELETE' THEN DELETE FROM loomplane_capacity_reservations WHERE org_id=OLD.org_id AND binding_id=OLD.id; RETURN OLD; END IF;
 pin:=NEW.config->'cloud_placement';
 DELETE FROM loomplane_capacity_reservations r WHERE r.org_id=NEW.org_id AND r.binding_id=NEW.id AND NOT coalesce(r.server_id=pin->>'server_id' AND r.plan_id=NEW.plan_id,false) AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.replaced) x WHERE x->>'plan_id'=r.plan_id AND x->'config'->'cloud_placement'->>'server_id'=r.server_id);
 UPDATE loomplane_capacity_reservations SET binding_id=NEW.id WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND environment_id=NEW.environment_id AND service=NEW.name AND (plan_id=NEW.plan_id OR body=pin) AND server_id=pin->>'server_id';
 RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_pool_binding_capacity AFTER INSERT OR UPDATE OR DELETE ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION loomplane_pool_binding_capacity()`,
	`CREATE OR REPLACE FUNCTION loomplane_pool_server_retirement() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.status IN ('retiring','deleted','disconnected') AND EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE server_id=NEW.id) THEN RAISE EXCEPTION 'server has reserved workloads' USING ERRCODE='23514'; END IF; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_pool_server_retirement BEFORE UPDATE ON loomplane_cloud_servers FOR EACH ROW EXECUTE FUNCTION loomplane_pool_server_retirement()`,
}
var serverPoolsDown = []string{
	`DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_cloud_targets WHERE pool_id IS NOT NULL) THEN RAISE EXCEPTION 'remove pool targets before downgrading'; END IF; END $$`,
	`DROP TABLE IF EXISTS loomplane_placement_moves`,
	`DROP TRIGGER IF EXISTS loomplane_pool_plan_reserve ON loomplane_plans`,
	`DROP TRIGGER IF EXISTS loomplane_pool_binding_capacity ON loomplane_bindings`,
	`DROP TRIGGER IF EXISTS loomplane_pool_server_retirement ON loomplane_cloud_servers`,
	`DROP FUNCTION IF EXISTS loomplane_reserve_pool_plan()`,
	`DROP FUNCTION IF EXISTS loomplane_pool_binding_capacity()`,
	`DROP FUNCTION IF EXISTS loomplane_pool_server_retirement()`,
	`DROP FUNCTION IF EXISTS loomplane_server_capacity(TEXT,TEXT,JSONB)`,
	`DROP TABLE IF EXISTS loomplane_capacity_reservations`,
	`DROP INDEX IF EXISTS loomplane_server_docker_identity`,
	`ALTER TABLE loomplane_cloud_targets DROP CONSTRAINT IF EXISTS loomplane_target_pool_scope`,
	`ALTER TABLE loomplane_cloud_targets DROP CONSTRAINT IF EXISTS loomplane_target_runtime`,
	`ALTER TABLE loomplane_cloud_targets DROP COLUMN IF EXISTS pool_id`,
	`ALTER TABLE loomplane_cloud_targets DROP COLUMN IF EXISTS runtime`,
	`DROP TABLE IF EXISTS loomplane_server_pool_members`,
	`DROP TABLE IF EXISTS loomplane_server_pools`,
}
