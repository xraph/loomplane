package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A project's default Cloud replaces the per-environment Cloud assignment.
// Clouds belong to the organization and grants already ignore environments;
// the assignment was the one thing tying a Cloud to an environment. Now one
// grant per project is its default, every environment of the project uses
// it, and environments differ only through loomplane.yaml.
//
// Placements stop pinning an assignment revision. Switching the default
// bumps both grants' revisions, which every placement already pins, so a
// plan resolved against the old default is stale without a new pin.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for _, step := range projectDefaultCloudUp {
				if _, err := db.Exec(ctx, step.sql); err != nil {
					return fmt.Errorf("%s: %w", step.what, err)
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, step := range projectDefaultCloudDown {
				if _, err := db.Exec(ctx, step.sql); err != nil {
					return fmt.Errorf("%s: %w", step.what, err)
				}
			}
			return nil
		},
	)
}

type migrationStep struct{ what, sql string }

var projectDefaultCloudUp = []migrationStep{
	{"add the default flag to Cloud grants", `ALTER TABLE loomplane_cloud_project_grants ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false`},
	// The Cloud most of a project's environments use becomes its default,
	// ties going to the most recent assignment. Marking it changes nothing
	// any plan resolved, so its revision stays where every plan pinned it.
	{"pause grant revision checks for the backfill", `ALTER TABLE loomplane_cloud_project_grants DISABLE TRIGGER loomplane_cloud_grant_revision`},
	{"make each project's most used Cloud its default", `DO $$ BEGIN
	-- A rerun finds the assignments already gone and has nothing to carry over.
	IF to_regclass('loomplane_cloud_assignments') IS NULL THEN RETURN; END IF;
	WITH used AS (
		SELECT a.org_id,a.project_id,a.cloud_id,count(*) AS environments,max(a.revision) AS latest
		FROM loomplane_cloud_assignments a
		JOIN loomplane_cloud_project_grants g ON g.org_id=a.org_id AND g.project_id=a.project_id AND g.cloud_id=a.cloud_id AND NOT g.revoked
		WHERE a.cloud_id IS NOT NULL
		GROUP BY a.org_id,a.project_id,a.cloud_id
	), picked AS (
		SELECT DISTINCT ON (org_id,project_id) org_id,project_id,cloud_id FROM used
		ORDER BY org_id,project_id,environments DESC,latest DESC,cloud_id
	)
	UPDATE loomplane_cloud_project_grants g SET is_default=true
	FROM picked p WHERE g.org_id=p.org_id AND g.project_id=p.project_id AND g.cloud_id=p.cloud_id
		AND NOT EXISTS(SELECT 1 FROM loomplane_cloud_project_grants d WHERE d.org_id=g.org_id AND d.project_id=g.project_id AND d.is_default AND NOT d.revoked);
END $$`},
	{"resume grant revision checks", `ALTER TABLE loomplane_cloud_project_grants ENABLE TRIGGER loomplane_cloud_grant_revision`},
	{"allow one live default Cloud per project", `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_project_default ON loomplane_cloud_project_grants(org_id,project_id) WHERE is_default AND NOT revoked`},
	// Revoking a grant ends its default, however the revocation is written.
	// Asking for a revoked default outright is refused, and a default can't
	// change under a shared change still moving services on that Cloud.
	{"guard the default flag", `CREATE OR REPLACE FUNCTION loomplane_cloud_default_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='UPDATE' AND NEW.revoked AND NOT OLD.revoked THEN
		NEW.is_default := false;
	END IF;
	IF NEW.is_default AND NEW.revoked THEN
		RAISE EXCEPTION 'a revoked grant cannot be the default Cloud' USING ERRCODE='23514';
	END IF;
	IF TG_OP='UPDATE' AND NEW.is_default IS NOT DISTINCT FROM OLD.is_default THEN RETURN NEW; END IF;
	IF NOT NEW.is_default THEN RETURN NEW; END IF;
	IF EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND (target_id<>'' OR body->>'default'='true') AND status NOT IN ('succeeded','reverted')) THEN
		RAISE EXCEPTION 'Cloud target has unfinished migration work' USING ERRCODE='55P03';
	END IF;
	RETURN NEW;
END $$`},
	{"attach the default guard", `CREATE OR REPLACE TRIGGER loomplane_cloud_default BEFORE INSERT OR UPDATE ON loomplane_cloud_project_grants FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_default_guard()`},
	{"announce default Cloud changes", `CREATE OR REPLACE FUNCTION loomplane_project_cloud_event_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='UPDATE' AND NEW.is_default IS NOT DISTINCT FROM OLD.is_default AND NEW.revoked IS NOT DISTINCT FROM OLD.revoked THEN RETURN NULL; END IF;
	IF TG_OP='INSERT' AND NOT NEW.is_default THEN RETURN NULL; END IF;
	PERFORM loomplane_append_event(NEW.org_id,NEW.project_id,'project_cloud.updated',NEW.project_id,
		jsonb_build_object('org_id',NEW.org_id,'project_id',NEW.project_id,
			'cloud_id',(SELECT cloud_id FROM loomplane_cloud_project_grants WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND is_default AND NOT revoked)));
	RETURN NULL;
END $$`},
	{"attach the default Cloud event", `CREATE OR REPLACE TRIGGER loomplane_project_cloud_event AFTER INSERT OR UPDATE ON loomplane_cloud_project_grants FOR EACH ROW EXECUTE FUNCTION loomplane_project_cloud_event_trigger()`},
	// A target stays while a serving placement pins it, or while its Cloud
	// is a project's default, whose manifests may name it at the next plan.
	{"keep targets that are in use", `CREATE OR REPLACE FUNCTION loomplane_cloud_target_delete_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=OLD.org_id AND id=OLD.cloud_id FOR UPDATE;
	IF EXISTS(SELECT 1 FROM loomplane_cloud_project_grants WHERE org_id=OLD.org_id AND cloud_id=OLD.cloud_id AND is_default AND NOT revoked)
		OR EXISTS(SELECT 1 FROM loomplane_bindings WHERE org_id=OLD.org_id AND subject='service' AND config->'cloud_placement'->>'target_id'=OLD.id) THEN
		RAISE EXCEPTION 'target belongs to a Cloud in use' USING ERRCODE='23503';
	END IF;
	RETURN OLD;
END $$`},
	// The assignment's foreign key once kept a Cloud in use from being
	// deleted. Grants cascade with their Cloud, so a project's default would
	// vanish with it; refuse before the cascade runs.
	{"keep Clouds that are in use", `CREATE OR REPLACE FUNCTION loomplane_cloud_profile_delete_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_cloud_project_grants WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND is_default AND NOT revoked)
		OR EXISTS(SELECT 1 FROM loomplane_bindings WHERE org_id=OLD.org_id AND subject='service' AND config->'cloud_placement'->>'cloud_id'=OLD.id) THEN
		RAISE EXCEPTION 'Cloud is in use' USING ERRCODE='23503';
	END IF;
	RETURN OLD;
END $$`},
	{"attach the Cloud delete guard", `CREATE OR REPLACE TRIGGER loomplane_cloud_profile_delete BEFORE DELETE ON loomplane_cloud_profiles FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_profile_delete_guard()`},
	{"check capacity intents without an assignment", `CREATE OR REPLACE FUNCTION public.loomplane_capacity_intent_current(p loomplane_plans, i jsonb)
 RETURNS boolean
 LANGUAGE sql
 STABLE
AS $function$
	SELECT coalesce(i->'demand'->>'org_id'=p.org_id AND i->'demand'->>'project_id'=p.project_id AND i->'demand'->>'environment_id'=p.environment_id AND i->'demand'->>'plan_id'=p.id
		AND EXISTS(SELECT 1 FROM loomplane_cloud_targets t
			JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=p.project_id AND NOT g.revoked
			JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
			JOIN loomplane_cloud_capacity_policies cp ON cp.org_id=t.org_id AND cp.cloud_id=t.cloud_id
			JOIN loomplane_cloud_connections pc ON pc.org_id=t.org_id AND pc.id=i->'template'->>'connection_id' AND pc.generation=(i->'provider_pin'->>'connection_generation')::bigint
				AND (CASE pc.cloud
					WHEN 'digitalocean' THEN coalesce(nullif(pc.details->>'team_uuid',''),pc.details->>'account_uuid')
					WHEN 'vercel' THEN coalesce(nullif(pc.details->>'team_id',''),pc.details->>'user_id')
					ELSE pc.account END)=i->'provider_pin'->>'account_id'
				AND pc.status='ready'
			WHERE t.org_id=p.org_id AND t.id=i->'demand'->>'target_id' AND t.cloud_id=i->'demand'->>'cloud_id' AND t.pool_id=i->'demand'->>'pool_id'
				AND t.provider='server' AND t.revision=(i->>'target_revision')::bigint AND c.revision=(i->>'cloud_revision')::bigint AND g.revision=(i->>'grant_revision')::bigint
				AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)) AND cp.enabled
				AND EXISTS(SELECT 1 FROM jsonb_array_elements(cp.body->'pools') template
					WHERE template->>'pool_id'=t.pool_id AND template-ARRAY['min_servers','max_servers','limits'] = (i->'template')-ARRAY['min_servers','max_servers','limits'])) ,false) $function$`},
	{"check workload scope without an assignment", `CREATE OR REPLACE FUNCTION public.loomplane_cluster_workload_scope_current(p loomplane_plans, d jsonb)
 RETURNS boolean
 LANGUAGE sql
 STABLE
AS $function$
 SELECT coalesce(d->'input'->>'org_id'=p.org_id AND d->'input'->>'project_id'=p.project_id AND d->'input'->>'environment_id'=p.environment_id AND d->'input'->>'plan_id'=p.id AND EXISTS(
 SELECT 1 FROM loomplane_cloud_targets t
 JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
 JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=p.project_id AND NOT g.revoked
 JOIN loomplane_cloud_connections cn ON cn.org_id=t.org_id AND cn.id=t.connection_id AND cn.status='ready' AND cn.cloud='kubernetes'
 JOIN loomplane_runtime_attachments rt ON rt.org_id=t.org_id AND rt.id=d->'input'->>'attachment_id' AND rt.cluster_id=t.managed_resource_id AND rt.project_id=p.project_id AND rt.runtime_connection_id=t.connection_id AND rt.deleted_at IS NULL AND rt.body->>'trust_revision'=d->'input'->>'trust_revision' AND rt.body->>'namespace_uid'=cn.details->>'namespace_uid' AND rt.body->>'trust_fingerprint'=cn.details->>'cluster_ca'
 JOIN loomplane_managed_resources mr ON mr.org_id=t.org_id AND mr.id=t.managed_resource_id AND mr.deleted_at IS NULL AND mr.status NOT IN ('missing','identity_changed')
 WHERE t.org_id=p.org_id AND t.id=d->'input'->>'target_id' AND t.cloud_id=d->'input'->>'cloud_id' AND t.project_id=p.project_id AND t.managed_resource_id=d->'input'->>'resource_id' AND t.connection_id=d->'input'->>'connection_id'
 AND t.revision=(d->'placement'->>'target_revision')::bigint AND c.revision=(d->'placement'->>'cloud_revision')::bigint AND g.revision=(d->'placement'->>'grant_revision')::bigint AND cn.generation=(d->>'connection_generation')::bigint
 AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))),false) $function$`},
	{"admit capacity plans without an assignment", `CREATE OR REPLACE FUNCTION public.loomplane_admit_capacity_plan(candidate loomplane_plans, approved loomplane_plans)
 RETURNS void
 LANGUAGE plpgsql
AS $function$
DECLARE
	a JSONB;
	pin JSONB;
	host loomplane_cloud_servers%ROWTYPE;
	member loomplane_server_pool_members%ROWTYPE;
	pool loomplane_server_pools%ROWTYPE;
	cpu BIGINT;
	mem BIGINT;
	used_cpu BIGINT;
	used_mem BIGINT;
BEGIN
	

	-- Order host and connection locks consistently across every accepted plan.
	PERFORM 1 FROM loomplane_cloud_connections c
	WHERE c.org_id=candidate.org_id AND c.id IN (SELECT x->>'connection_id' FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) x)
	ORDER BY c.id
	FOR UPDATE;
	PERFORM pg_advisory_xact_lock(hashtextextended('host-admission/'||identity,0))
	FROM (
		SELECT DISTINCT c.details->>'docker_id' identity
		FROM loomplane_cloud_connections c
		WHERE c.org_id=candidate.org_id AND c.cloud='server' AND coalesce(c.details->>'docker_id','')<>'' AND c.id IN (SELECT x->>'connection_id' FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) x)
		ORDER BY identity
	) identities;
	PERFORM 1 FROM loomplane_cloud_servers s
	WHERE s.org_id=candidate.org_id AND s.id IN (SELECT x->'service'->'cloud_placement'->>'server_id' FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) x)
	ORDER BY s.id
	FOR UPDATE;

	FOR a IN SELECT value FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) LOOP
		IF a->>'op'='delete' OR (a->>'op'='keep' AND NOT coalesce((a->>'adopt_cloud_placement')::boolean,false)) THEN CONTINUE; END IF;
		IF coalesce((a->'compensation'->>'retained')::boolean,false) THEN
			IF NOT loomplane_retained_compensation(approved,a) THEN RAISE EXCEPTION 'retained compensation identity changed' USING ERRCODE='23514'; END IF;
			CONTINUE;
		END IF;
		IF coalesce(a->'service'->'capacity_intent','null')<>'null'::jsonb THEN
			IF NOT EXISTS(SELECT 1 FROM loomplane_capacity_intents i WHERE i.org_id=approved.org_id AND i.plan_id=approved.id AND i.service=a->>'name' AND i.plan_revision=approved.revision AND i.body=a->'service'->'capacity_intent') THEN
				RAISE EXCEPTION 'missing approved deferred intent' USING ERRCODE='23514';
			END IF;
			CONTINUE;
		END IF;
		pin:=a->'service'->'cloud_placement';
		IF pin ? 'allocation' AND (jsonb_typeof(pin->'allocation')<>'string' OR length(pin->>'allocation')>200 OR pin->>'allocation'<>btrim(pin->>'allocation')) THEN
			RAISE EXCEPTION 'allocation must be a bounded string' USING ERRCODE='23514';
		END IF;
		IF coalesce(pin->>'pool_id','')='' THEN
			IF a->>'provider'='server' AND EXISTS(
				SELECT 1 FROM loomplane_server_pool_members m
				JOIN loomplane_cloud_servers s ON s.id=m.server_id
				JOIN loomplane_cloud_connections c ON c.org_id=candidate.org_id AND c.id=a->>'connection_id'
				WHERE (s.runtime_connection_id=c.id OR (coalesce(c.details->>'docker_id','')<>'' AND c.details->>'docker_id'=s.body->'health'->>'docker_id')) AND s.status NOT IN ('deleted','disconnected')
			) THEN
				RAISE EXCEPTION 'pooled capacity requires a pool target' USING ERRCODE='23514';
			END IF;
			CONTINUE;
		END IF;
		IF coalesce(candidate.result->>'cloud_sync_id','')<>'' AND NOT loomplane_accepted_pool_sync(approved) THEN
			RAISE EXCEPTION 'pool sync needs its exact immutable accepted participant plan' USING ERRCODE='23514';
		END IF;
		IF jsonb_array_length(coalesce(a->'routes','[]'))>0 AND (
			coalesce(pin->>'allocation','')='' OR EXISTS(
				SELECT 1 FROM loomplane_bindings b
				WHERE b.org_id=candidate.org_id AND b.project_id=candidate.project_id AND b.environment_id=candidate.environment_id AND b.subject='service' AND b.name=a->>'name'
				AND (coalesce(b.config->'cloud_placement'->>'allocation','')=pin->>'allocation'
					OR (b.config->'cloud_placement' ? 'allocation' AND jsonb_typeof(b.config->'cloud_placement'->'allocation')<>'string')
					OR (coalesce(b.config->'cloud_placement'->>'pool_id','')<>'' AND NOT EXISTS(SELECT 1 FROM loomplane_capacity_reservations r WHERE r.org_id=b.org_id AND r.project_id=b.project_id AND r.environment_id=b.environment_id AND r.service=b.name AND r.plan_id=b.plan_id AND r.server_id=b.config->'cloud_placement'->>'server_id' AND r.binding_id=b.id AND r.body=b.config->'cloud_placement'))))) THEN
			RAISE EXCEPTION 'routed allocation must retain the exact source and reserve a distinct candidate' USING ERRCODE='23514';
		END IF;
		IF EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=candidate.org_id AND environment_id=candidate.environment_id AND service=a->>'name' AND binding_id='' AND plan_id<>candidate.id) THEN
			RAISE EXCEPTION 'recover the unresolved accepted placement before another deployment' USING ERRCODE='23514';
		END IF;
		IF coalesce((pin->>'pool_revision')::bigint,0)<1 OR coalesce((pin->>'server_revision')::bigint,0)<1 OR coalesce((pin->>'connection_generation')::bigint,0)<1
			OR coalesce((pin->>'cpu_millis')::bigint,0)<1 OR coalesce((pin->>'memory_bytes')::bigint,0)<1
			OR coalesce((a->'service'->'scale'->>'min')::int,1)<>1 OR coalesce((a->'service'->'scale'->>'max')::int,1)<>1 THEN
			RAISE EXCEPTION 'pool placement needs bounded resources and one replica' USING ERRCODE='23514';
		END IF;
		IF EXISTS(
			SELECT 1 FROM loomplane_bindings b
			WHERE b.org_id=candidate.org_id AND b.environment_id=candidate.environment_id AND b.subject='service' AND b.name=a->>'name' AND coalesce(b.config->'cloud_placement'->>'server_id','')<>pin->>'server_id'
			AND (coalesce(b.config->'catalog','null')<>'null'::jsonb OR (NOT loomplane_accepted_pool_sync(approved) AND NOT EXISTS(SELECT 1 FROM loomplane_placement_moves m WHERE m.org_id=b.org_id AND m.project_id=b.project_id AND m.environment_id=b.environment_id AND m.service=b.name AND m.source_server_id=b.config->'cloud_placement'->>'server_id' AND m.destination_server_id=pin->>'server_id' AND m.source_config=b.config AND m.expires_at>now())))
		) THEN
			RAISE EXCEPTION 'host relocation needs a current explicit stateless move review' USING ERRCODE='23514';
		END IF;

		SELECT * INTO host FROM loomplane_cloud_servers WHERE org_id=candidate.org_id AND id=pin->>'server_id';
		SELECT * INTO pool FROM loomplane_server_pools WHERE org_id=candidate.org_id AND cloud_id=pin->>'cloud_id' AND id=pin->>'pool_id' FOR SHARE;
		SELECT * INTO member FROM loomplane_server_pool_members WHERE org_id=candidate.org_id AND pool_id=pool.id AND server_id=host.id;
		IF host.id IS NULL OR pool.id IS NULL OR member.server_id IS NULL OR host.status<>'ready' OR host.revision<>(pin->>'server_revision')::bigint OR pool.revision<>(pin->>'pool_revision')::bigint
			OR host.runtime_connection_id<>pin->>'connection_id' OR a->>'connection_id'<>pin->>'connection_id' OR a->>'provider'<>'server' THEN
			RAISE EXCEPTION 'pool placement changed; review again' USING ERRCODE='23514';
		END IF;
		IF (coalesce((host.body->>'draining')::boolean,false) OR EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE server_id=host.id AND draining))
			AND NOT EXISTS(SELECT 1 FROM loomplane_bindings WHERE org_id=candidate.org_id AND environment_id=candidate.environment_id AND subject='service' AND name=a->>'name' AND config->'cloud_placement'->>'server_id'=host.id) THEN
			RAISE EXCEPTION 'server is draining' USING ERRCODE='23514';
		END IF;
		IF (host.body->'health'->>'checked_at')::timestamptz < now()-interval '15 minutes' OR (host.body->'health'->>'checked_at')::timestamptz > now()+interval '1 minute'
			OR coalesce((host.body->'health'->>'effective_cpu_millis')::bigint,0)<member.cpu_millis OR coalesce((host.body->'health'->>'effective_memory_bytes')::bigint,0)<member.memory_bytes THEN
			RAISE EXCEPTION 'effective host capacity needs a fresh check' USING ERRCODE='23514';
		END IF;
		IF NOT EXISTS(SELECT 1 FROM loomplane_cloud_connections c WHERE c.org_id=candidate.org_id AND c.id=host.runtime_connection_id AND c.status='ready' AND c.generation=(pin->>'connection_generation')::bigint) THEN
			RAISE EXCEPTION 'runtime connection changed' USING ERRCODE='23514';
		END IF;

		PERFORM 1 FROM loomplane_cloud_project_grants WHERE org_id=candidate.org_id AND cloud_id=pin->>'cloud_id' AND project_id=candidate.project_id FOR SHARE;
		PERFORM 1 FROM loomplane_cloud_targets WHERE org_id=candidate.org_id AND id=pin->>'target_id' FOR SHARE;
		PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=candidate.org_id AND id=pin->>'cloud_id' FOR SHARE;
		IF NOT EXISTS(
			SELECT 1 FROM loomplane_cloud_targets t
			JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=candidate.project_id AND NOT g.revoked
			JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
			WHERE t.org_id=candidate.org_id AND t.id=pin->>'target_id' AND t.pool_id=pool.id AND t.revision=(pin->>'target_revision')::bigint
			AND c.revision=(pin->>'cloud_revision')::bigint AND g.revision=(pin->>'grant_revision')::bigint AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))
		) THEN
			RAISE EXCEPTION 'pool target authority changed' USING ERRCODE='23514';
		END IF;

		cpu:=coalesce((a->'service'->'size'->>'cpu')::numeric*1000,1000);
		mem:=coalesce((a->'service'->'size'->>'memory')::bigint*1048576,536870912);
		IF cpu<=0 OR mem<=0 OR cpu<>(pin->>'cpu_millis')::bigint OR mem<>(pin->>'memory_bytes')::bigint THEN
			RAISE EXCEPTION 'capacity differs from runtime limits' USING ERRCODE='23514';
		END IF;
		SELECT c.cpu,c.memory INTO used_cpu,used_mem
		FROM loomplane_server_capacity(host.id,candidate.environment_id,jsonb_build_array(jsonb_build_object('service',a->>'name','server_id',host.id,'allocation',coalesce(pin->>'allocation',''),'cpu_millis',cpu,'memory_bytes',mem))) c;
		IF used_cpu>member.cpu_millis OR used_mem>member.memory_bytes THEN
			RAISE EXCEPTION 'insufficient reserved host capacity' USING ERRCODE='23514';
		END IF;
		INSERT INTO loomplane_capacity_reservations(org_id,project_id,environment_id,service,plan_id,pool_id,server_id,connection_id,cpu_millis,memory_bytes,body)
		VALUES(candidate.org_id,candidate.project_id,candidate.environment_id,a->>'name',candidate.id,pool.id,host.id,host.runtime_connection_id,cpu,mem,pin)
		ON CONFLICT DO NOTHING;
	END LOOP;

	RETURN;
END $function$`},
	{"drop the assignment table", `DROP TABLE IF EXISTS loomplane_cloud_assignments`},
	{"drop the assignment event function", `DROP FUNCTION IF EXISTS loomplane_environment_cloud_event_trigger()`},
	{"drop the assignment grant guard", `DROP FUNCTION IF EXISTS loomplane_cloud_assignment_grant_guard()`},
}

// Down gives every environment of a project its project's default Cloud,
// which is the closest the per-environment model can say.
var projectDefaultCloudDown = []migrationStep{
	{"recreate the assignment table", `CREATE TABLE IF NOT EXISTS loomplane_cloud_assignments(
	org_id text NOT NULL, project_id text NOT NULL, environment_id text NOT NULL, cloud_id text, revision bigint NOT NULL CHECK (revision>0),
	PRIMARY KEY (org_id,project_id,environment_id),
	FOREIGN KEY (org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE CASCADE,
	CONSTRAINT shared_cloud_assignment_owner FOREIGN KEY (org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id) ON DELETE RESTRICT)`},
	{"recreate the assignment index", `CREATE INDEX IF NOT EXISTS loomplane_cloud_assignments_cloud ON loomplane_cloud_assignments(org_id,project_id,cloud_id)`},
	{"assign each environment its project's default", `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision)
SELECT e.org_id,e.project_id,e.id,g.cloud_id,1 FROM loomplane_environments e
JOIN loomplane_cloud_project_grants g ON g.org_id=e.org_id AND g.project_id=e.project_id AND g.is_default AND NOT g.revoked
ON CONFLICT DO NOTHING`},
	{"restore the assignment grant guard", `CREATE OR REPLACE FUNCTION public.loomplane_cloud_assignment_grant_guard()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
	IF NEW.cloud_id IS NULL THEN RETURN NEW; END IF;
	IF TG_OP='UPDATE' AND NEW.cloud_id IS NOT DISTINCT FROM OLD.cloud_id THEN RETURN NEW; END IF;

	PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
	PERFORM 1 FROM loomplane_cloud_project_grants WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND cloud_id=NEW.cloud_id AND NOT revoked FOR SHARE;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'Cloud is not granted to project' USING ERRCODE='23503';
	END IF;
	IF EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND (target_id<>'' OR body->>'default'='true') AND status NOT IN ('succeeded','reverted')) THEN
		RAISE EXCEPTION 'Cloud target has unfinished migration work' USING ERRCODE='55P03';
	END IF;

	RETURN NEW;
END $function$`},
	{"restore the assignment event function", `CREATE OR REPLACE FUNCTION public.loomplane_environment_cloud_event_trigger()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
	doc JSONB;
BEGIN
	IF TG_OP='DELETE' THEN doc:=to_jsonb(OLD); ELSE doc:=to_jsonb(NEW); END IF;

	PERFORM loomplane_append_event(doc->>'org_id',doc->>'project_id','environment_cloud.updated',doc->>'environment_id',doc);
	RETURN NULL;
END $function$`},
	{"restore the assignment grant trigger", `CREATE OR REPLACE TRIGGER loomplane_cloud_assignment_grant BEFORE INSERT OR UPDATE ON loomplane_cloud_assignments FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_assignment_grant_guard()`},
	{"restore the assignment revision trigger", `CREATE OR REPLACE TRIGGER loomplane_cloud_assignment_revision BEFORE UPDATE ON loomplane_cloud_assignments FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_revision_trigger()`},
	{"restore the assignment event trigger", `CREATE OR REPLACE TRIGGER loomplane_state_event AFTER INSERT OR DELETE OR UPDATE ON loomplane_cloud_assignments FOR EACH ROW EXECUTE FUNCTION loomplane_environment_cloud_event_trigger()`},
	{"restore the target delete guard", `CREATE OR REPLACE FUNCTION public.loomplane_cloud_target_delete_trigger()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
	PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=OLD.org_id AND id=OLD.cloud_id FOR UPDATE;
	IF EXISTS(SELECT 1 FROM loomplane_cloud_assignments WHERE org_id=OLD.org_id AND cloud_id=OLD.cloud_id) THEN
		RAISE EXCEPTION 'target belongs to an assigned cloud' USING ERRCODE='23503';
	END IF;
	RETURN OLD;
END $function$`},
	{"restore capacity intent checks", `CREATE OR REPLACE FUNCTION public.loomplane_capacity_intent_current(p loomplane_plans, i jsonb)
 RETURNS boolean
 LANGUAGE sql
 STABLE
AS $function$
	SELECT coalesce(i->'demand'->>'org_id'=p.org_id AND i->'demand'->>'project_id'=p.project_id AND i->'demand'->>'environment_id'=p.environment_id AND i->'demand'->>'plan_id'=p.id
		AND EXISTS(SELECT 1 FROM loomplane_cloud_targets t
			JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=p.project_id AND NOT g.revoked
			JOIN loomplane_cloud_assignments a ON a.org_id=t.org_id AND a.project_id=p.project_id AND a.environment_id=p.environment_id AND a.cloud_id=t.cloud_id
			JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
			JOIN loomplane_cloud_capacity_policies cp ON cp.org_id=t.org_id AND cp.cloud_id=t.cloud_id
			JOIN loomplane_cloud_connections pc ON pc.org_id=t.org_id AND pc.id=i->'template'->>'connection_id' AND pc.generation=(i->'provider_pin'->>'connection_generation')::bigint
				AND (CASE pc.cloud
					WHEN 'digitalocean' THEN coalesce(nullif(pc.details->>'team_uuid',''),pc.details->>'account_uuid')
					WHEN 'vercel' THEN coalesce(nullif(pc.details->>'team_id',''),pc.details->>'user_id')
					ELSE pc.account END)=i->'provider_pin'->>'account_id'
				AND pc.status='ready'
			WHERE t.org_id=p.org_id AND t.id=i->'demand'->>'target_id' AND t.cloud_id=i->'demand'->>'cloud_id' AND t.pool_id=i->'demand'->>'pool_id'
				AND t.provider='server' AND t.revision=(i->>'target_revision')::bigint AND a.revision=(i->>'assignment_revision')::bigint AND c.revision=(i->>'cloud_revision')::bigint AND g.revision=(i->>'grant_revision')::bigint
				AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)) AND cp.enabled
				AND EXISTS(SELECT 1 FROM jsonb_array_elements(cp.body->'pools') template
					WHERE template->>'pool_id'=t.pool_id AND template-ARRAY['min_servers','max_servers','limits'] = (i->'template')-ARRAY['min_servers','max_servers','limits'])) ,false) $function$`},
	{"restore workload scope checks", `CREATE OR REPLACE FUNCTION public.loomplane_cluster_workload_scope_current(p loomplane_plans, d jsonb)
 RETURNS boolean
 LANGUAGE sql
 STABLE
AS $function$
 SELECT coalesce(d->'input'->>'org_id'=p.org_id AND d->'input'->>'project_id'=p.project_id AND d->'input'->>'environment_id'=p.environment_id AND d->'input'->>'plan_id'=p.id AND EXISTS(
 SELECT 1 FROM loomplane_cloud_targets t
 JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
 JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=p.project_id AND NOT g.revoked
 JOIN loomplane_cloud_assignments a ON a.org_id=t.org_id AND a.project_id=p.project_id AND a.environment_id=p.environment_id AND a.cloud_id=t.cloud_id
 JOIN loomplane_cloud_connections cn ON cn.org_id=t.org_id AND cn.id=t.connection_id AND cn.status='ready' AND cn.cloud='kubernetes'
 JOIN loomplane_runtime_attachments rt ON rt.org_id=t.org_id AND rt.id=d->'input'->>'attachment_id' AND rt.cluster_id=t.managed_resource_id AND rt.project_id=p.project_id AND rt.runtime_connection_id=t.connection_id AND rt.deleted_at IS NULL AND rt.body->>'trust_revision'=d->'input'->>'trust_revision' AND rt.body->>'namespace_uid'=cn.details->>'namespace_uid' AND rt.body->>'trust_fingerprint'=cn.details->>'cluster_ca'
 JOIN loomplane_managed_resources mr ON mr.org_id=t.org_id AND mr.id=t.managed_resource_id AND mr.deleted_at IS NULL AND mr.status NOT IN ('missing','identity_changed')
 WHERE t.org_id=p.org_id AND t.id=d->'input'->>'target_id' AND t.cloud_id=d->'input'->>'cloud_id' AND t.project_id=p.project_id AND t.managed_resource_id=d->'input'->>'resource_id' AND t.connection_id=d->'input'->>'connection_id'
 AND t.revision=(d->'placement'->>'target_revision')::bigint AND c.revision=(d->'placement'->>'cloud_revision')::bigint AND g.revision=(d->'placement'->>'grant_revision')::bigint AND a.revision=(d->'placement'->>'assignment_revision')::bigint AND cn.generation=(d->>'connection_generation')::bigint
 AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))),false) $function$`},
	{"restore capacity plan admission", `CREATE OR REPLACE FUNCTION public.loomplane_admit_capacity_plan(candidate loomplane_plans, approved loomplane_plans)
 RETURNS void
 LANGUAGE plpgsql
AS $function$
DECLARE
	a JSONB;
	pin JSONB;
	host loomplane_cloud_servers%ROWTYPE;
	member loomplane_server_pool_members%ROWTYPE;
	pool loomplane_server_pools%ROWTYPE;
	cpu BIGINT;
	mem BIGINT;
	used_cpu BIGINT;
	used_mem BIGINT;
BEGIN
	

	-- Order host and connection locks consistently across every accepted plan.
	PERFORM 1 FROM loomplane_cloud_connections c
	WHERE c.org_id=candidate.org_id AND c.id IN (SELECT x->>'connection_id' FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) x)
	ORDER BY c.id
	FOR UPDATE;
	PERFORM pg_advisory_xact_lock(hashtextextended('host-admission/'||identity,0))
	FROM (
		SELECT DISTINCT c.details->>'docker_id' identity
		FROM loomplane_cloud_connections c
		WHERE c.org_id=candidate.org_id AND c.cloud='server' AND coalesce(c.details->>'docker_id','')<>'' AND c.id IN (SELECT x->>'connection_id' FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) x)
		ORDER BY identity
	) identities;
	PERFORM 1 FROM loomplane_cloud_servers s
	WHERE s.org_id=candidate.org_id AND s.id IN (SELECT x->'service'->'cloud_placement'->>'server_id' FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) x)
	ORDER BY s.id
	FOR UPDATE;

	FOR a IN SELECT value FROM jsonb_array_elements(coalesce(candidate.result->'actions','[]')) LOOP
		IF a->>'op'='delete' OR (a->>'op'='keep' AND NOT coalesce((a->>'adopt_cloud_placement')::boolean,false)) THEN CONTINUE; END IF;
		IF coalesce((a->'compensation'->>'retained')::boolean,false) THEN
			IF NOT loomplane_retained_compensation(approved,a) THEN RAISE EXCEPTION 'retained compensation identity changed' USING ERRCODE='23514'; END IF;
			CONTINUE;
		END IF;
		IF coalesce(a->'service'->'capacity_intent','null')<>'null'::jsonb THEN
			IF NOT EXISTS(SELECT 1 FROM loomplane_capacity_intents i WHERE i.org_id=approved.org_id AND i.plan_id=approved.id AND i.service=a->>'name' AND i.plan_revision=approved.revision AND i.body=a->'service'->'capacity_intent') THEN
				RAISE EXCEPTION 'missing approved deferred intent' USING ERRCODE='23514';
			END IF;
			CONTINUE;
		END IF;
		pin:=a->'service'->'cloud_placement';
		IF pin ? 'allocation' AND (jsonb_typeof(pin->'allocation')<>'string' OR length(pin->>'allocation')>200 OR pin->>'allocation'<>btrim(pin->>'allocation')) THEN
			RAISE EXCEPTION 'allocation must be a bounded string' USING ERRCODE='23514';
		END IF;
		IF coalesce(pin->>'pool_id','')='' THEN
			IF a->>'provider'='server' AND EXISTS(
				SELECT 1 FROM loomplane_server_pool_members m
				JOIN loomplane_cloud_servers s ON s.id=m.server_id
				JOIN loomplane_cloud_connections c ON c.org_id=candidate.org_id AND c.id=a->>'connection_id'
				WHERE (s.runtime_connection_id=c.id OR (coalesce(c.details->>'docker_id','')<>'' AND c.details->>'docker_id'=s.body->'health'->>'docker_id')) AND s.status NOT IN ('deleted','disconnected')
			) THEN
				RAISE EXCEPTION 'pooled capacity requires a pool target' USING ERRCODE='23514';
			END IF;
			CONTINUE;
		END IF;
		IF coalesce(candidate.result->>'cloud_sync_id','')<>'' AND NOT loomplane_accepted_pool_sync(approved) THEN
			RAISE EXCEPTION 'pool sync needs its exact immutable accepted participant plan' USING ERRCODE='23514';
		END IF;
		IF jsonb_array_length(coalesce(a->'routes','[]'))>0 AND (
			coalesce(pin->>'allocation','')='' OR EXISTS(
				SELECT 1 FROM loomplane_bindings b
				WHERE b.org_id=candidate.org_id AND b.project_id=candidate.project_id AND b.environment_id=candidate.environment_id AND b.subject='service' AND b.name=a->>'name'
				AND (coalesce(b.config->'cloud_placement'->>'allocation','')=pin->>'allocation'
					OR (b.config->'cloud_placement' ? 'allocation' AND jsonb_typeof(b.config->'cloud_placement'->'allocation')<>'string')
					OR (coalesce(b.config->'cloud_placement'->>'pool_id','')<>'' AND NOT EXISTS(SELECT 1 FROM loomplane_capacity_reservations r WHERE r.org_id=b.org_id AND r.project_id=b.project_id AND r.environment_id=b.environment_id AND r.service=b.name AND r.plan_id=b.plan_id AND r.server_id=b.config->'cloud_placement'->>'server_id' AND r.binding_id=b.id AND r.body=b.config->'cloud_placement'))))) THEN
			RAISE EXCEPTION 'routed allocation must retain the exact source and reserve a distinct candidate' USING ERRCODE='23514';
		END IF;
		IF EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=candidate.org_id AND environment_id=candidate.environment_id AND service=a->>'name' AND binding_id='' AND plan_id<>candidate.id) THEN
			RAISE EXCEPTION 'recover the unresolved accepted placement before another deployment' USING ERRCODE='23514';
		END IF;
		IF coalesce((pin->>'pool_revision')::bigint,0)<1 OR coalesce((pin->>'server_revision')::bigint,0)<1 OR coalesce((pin->>'connection_generation')::bigint,0)<1
			OR coalesce((pin->>'cpu_millis')::bigint,0)<1 OR coalesce((pin->>'memory_bytes')::bigint,0)<1
			OR coalesce((a->'service'->'scale'->>'min')::int,1)<>1 OR coalesce((a->'service'->'scale'->>'max')::int,1)<>1 THEN
			RAISE EXCEPTION 'pool placement needs bounded resources and one replica' USING ERRCODE='23514';
		END IF;
		IF EXISTS(
			SELECT 1 FROM loomplane_bindings b
			WHERE b.org_id=candidate.org_id AND b.environment_id=candidate.environment_id AND b.subject='service' AND b.name=a->>'name' AND coalesce(b.config->'cloud_placement'->>'server_id','')<>pin->>'server_id'
			AND (coalesce(b.config->'catalog','null')<>'null'::jsonb OR (NOT loomplane_accepted_pool_sync(approved) AND NOT EXISTS(SELECT 1 FROM loomplane_placement_moves m WHERE m.org_id=b.org_id AND m.project_id=b.project_id AND m.environment_id=b.environment_id AND m.service=b.name AND m.source_server_id=b.config->'cloud_placement'->>'server_id' AND m.destination_server_id=pin->>'server_id' AND m.source_config=b.config AND m.expires_at>now())))
		) THEN
			RAISE EXCEPTION 'host relocation needs a current explicit stateless move review' USING ERRCODE='23514';
		END IF;

		SELECT * INTO host FROM loomplane_cloud_servers WHERE org_id=candidate.org_id AND id=pin->>'server_id';
		SELECT * INTO pool FROM loomplane_server_pools WHERE org_id=candidate.org_id AND cloud_id=pin->>'cloud_id' AND id=pin->>'pool_id' FOR SHARE;
		SELECT * INTO member FROM loomplane_server_pool_members WHERE org_id=candidate.org_id AND pool_id=pool.id AND server_id=host.id;
		IF host.id IS NULL OR pool.id IS NULL OR member.server_id IS NULL OR host.status<>'ready' OR host.revision<>(pin->>'server_revision')::bigint OR pool.revision<>(pin->>'pool_revision')::bigint
			OR host.runtime_connection_id<>pin->>'connection_id' OR a->>'connection_id'<>pin->>'connection_id' OR a->>'provider'<>'server' THEN
			RAISE EXCEPTION 'pool placement changed; review again' USING ERRCODE='23514';
		END IF;
		IF (coalesce((host.body->>'draining')::boolean,false) OR EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE server_id=host.id AND draining))
			AND NOT EXISTS(SELECT 1 FROM loomplane_bindings WHERE org_id=candidate.org_id AND environment_id=candidate.environment_id AND subject='service' AND name=a->>'name' AND config->'cloud_placement'->>'server_id'=host.id) THEN
			RAISE EXCEPTION 'server is draining' USING ERRCODE='23514';
		END IF;
		IF (host.body->'health'->>'checked_at')::timestamptz < now()-interval '15 minutes' OR (host.body->'health'->>'checked_at')::timestamptz > now()+interval '1 minute'
			OR coalesce((host.body->'health'->>'effective_cpu_millis')::bigint,0)<member.cpu_millis OR coalesce((host.body->'health'->>'effective_memory_bytes')::bigint,0)<member.memory_bytes THEN
			RAISE EXCEPTION 'effective host capacity needs a fresh check' USING ERRCODE='23514';
		END IF;
		IF NOT EXISTS(SELECT 1 FROM loomplane_cloud_connections c WHERE c.org_id=candidate.org_id AND c.id=host.runtime_connection_id AND c.status='ready' AND c.generation=(pin->>'connection_generation')::bigint) THEN
			RAISE EXCEPTION 'runtime connection changed' USING ERRCODE='23514';
		END IF;

		PERFORM 1 FROM loomplane_cloud_project_grants WHERE org_id=candidate.org_id AND cloud_id=pin->>'cloud_id' AND project_id=candidate.project_id FOR SHARE;
		PERFORM 1 FROM loomplane_cloud_targets WHERE org_id=candidate.org_id AND id=pin->>'target_id' FOR SHARE;
		PERFORM 1 FROM loomplane_cloud_assignments WHERE org_id=candidate.org_id AND environment_id=candidate.environment_id FOR SHARE;
		PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=candidate.org_id AND id=pin->>'cloud_id' FOR SHARE;
		IF NOT EXISTS(
			SELECT 1 FROM loomplane_cloud_targets t
			JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=candidate.project_id AND NOT g.revoked
			JOIN loomplane_cloud_assignments ca ON ca.org_id=t.org_id AND ca.project_id=candidate.project_id AND ca.environment_id=candidate.environment_id AND ca.cloud_id=t.cloud_id
			JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
			WHERE t.org_id=candidate.org_id AND t.id=pin->>'target_id' AND t.pool_id=pool.id AND t.revision=(pin->>'target_revision')::bigint AND ca.revision=(pin->>'assignment_revision')::bigint
			AND c.revision=(pin->>'cloud_revision')::bigint AND g.revision=(pin->>'grant_revision')::bigint AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))
		) THEN
			RAISE EXCEPTION 'pool target authority changed' USING ERRCODE='23514';
		END IF;

		cpu:=coalesce((a->'service'->'size'->>'cpu')::numeric*1000,1000);
		mem:=coalesce((a->'service'->'size'->>'memory')::bigint*1048576,536870912);
		IF cpu<=0 OR mem<=0 OR cpu<>(pin->>'cpu_millis')::bigint OR mem<>(pin->>'memory_bytes')::bigint THEN
			RAISE EXCEPTION 'capacity differs from runtime limits' USING ERRCODE='23514';
		END IF;
		SELECT c.cpu,c.memory INTO used_cpu,used_mem
		FROM loomplane_server_capacity(host.id,candidate.environment_id,jsonb_build_array(jsonb_build_object('service',a->>'name','server_id',host.id,'allocation',coalesce(pin->>'allocation',''),'cpu_millis',cpu,'memory_bytes',mem))) c;
		IF used_cpu>member.cpu_millis OR used_mem>member.memory_bytes THEN
			RAISE EXCEPTION 'insufficient reserved host capacity' USING ERRCODE='23514';
		END IF;
		INSERT INTO loomplane_capacity_reservations(org_id,project_id,environment_id,service,plan_id,pool_id,server_id,connection_id,cpu_millis,memory_bytes,body)
		VALUES(candidate.org_id,candidate.project_id,candidate.environment_id,a->>'name',candidate.id,pool.id,host.id,host.runtime_connection_id,cpu,mem,pin)
		ON CONFLICT DO NOTHING;
	END LOOP;

	RETURN;
END $function$`},
	{"drop the Cloud delete guard", `DROP TRIGGER IF EXISTS loomplane_cloud_profile_delete ON loomplane_cloud_profiles`},
	{"drop the Cloud delete guard function", `DROP FUNCTION IF EXISTS loomplane_cloud_profile_delete_guard()`},
	{"drop the default Cloud event", `DROP TRIGGER IF EXISTS loomplane_project_cloud_event ON loomplane_cloud_project_grants`},
	{"drop the default Cloud event function", `DROP FUNCTION IF EXISTS loomplane_project_cloud_event_trigger()`},
	{"drop the default guard", `DROP TRIGGER IF EXISTS loomplane_cloud_default ON loomplane_cloud_project_grants`},
	{"drop the default guard function", `DROP FUNCTION IF EXISTS loomplane_cloud_default_guard()`},
	{"drop the default index", `DROP INDEX IF EXISTS loomplane_cloud_project_default`},
	{"drop the default flag", `ALTER TABLE loomplane_cloud_project_grants DROP COLUMN IF EXISTS is_default`},
}
