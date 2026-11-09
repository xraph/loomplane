package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `
CREATE UNIQUE INDEX loomplane_group_target_scope ON loomplane_cloud_targets(org_id,id);
CREATE UNIQUE INDEX loomplane_group_plan_scope ON loomplane_plans(org_id,id);
CREATE TABLE loomplane_tls_renewal_authorities (
 org_id text NOT NULL,id text NOT NULL,project_id text NOT NULL,attachment_id text NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),active boolean NOT NULL,
 schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),body jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,id),FOREIGN KEY(org_id,project_id) REFERENCES loomplane_projects(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id) ON DELETE RESTRICT,
 CHECK(coalesce(body->>'id'=id AND body->>'org_id'=org_id AND (body->>'generation')::bigint=generation AND (body->>'active')::boolean=active,false))
);
CREATE TABLE loomplane_group_timeout_verifications (
 org_id text NOT NULL,id text NOT NULL,project_id text NOT NULL,attachment_id text NOT NULL,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision=1),schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),
 namespace_uid text NOT NULL,config_revision text NOT NULL,policy jsonb NOT NULL,evidence_ref text NOT NULL,verified_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,id),FOREIGN KEY(org_id,project_id) REFERENCES loomplane_projects(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id) ON DELETE RESTRICT,
 CHECK(namespace_uid<>'' AND config_revision<>'' AND evidence_ref<>'' AND verified_by<>'')
);
CREATE TABLE loomplane_member_prerequisites (
 org_id text NOT NULL,id text NOT NULL,project_id text NOT NULL,attachment_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),authority_id text NOT NULL,
 schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),body jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,id),UNIQUE(org_id,project_id,attachment_id,id),
 FOREIGN KEY(org_id,project_id) REFERENCES loomplane_projects(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,authority_id) REFERENCES loomplane_tls_renewal_authorities(org_id,id) ON DELETE RESTRICT,
 CHECK(coalesce(body->>'id'=id AND body->>'org_id'=org_id AND body->>'project_id'=project_id AND body->>'attachment_id'=attachment_id AND body->>'renewal_authority_id'=authority_id AND (body->>'revision')::bigint=revision,false))
);
CREATE TABLE loomplane_member_prerequisite_observations (
 org_id text NOT NULL,prerequisite_id text NOT NULL,prerequisite_revision bigint NOT NULL CHECK(prerequisite_revision>0),
 revision bigint NOT NULL CHECK(revision>0),schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),body jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,prerequisite_id),FOREIGN KEY(org_id,prerequisite_id) REFERENCES loomplane_member_prerequisites(org_id,id) ON DELETE CASCADE
);
CREATE TABLE loomplane_group_destination_mappings (
 org_id text NOT NULL,project_id text NOT NULL,environment_id text NOT NULL,destination_id text NOT NULL,
 target_id text NOT NULL,attachment_id text NOT NULL,revision bigint NOT NULL CHECK(revision>0),
 schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,project_id,environment_id,destination_id),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,target_id) REFERENCES loomplane_cloud_targets(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id) ON DELETE RESTRICT
);
CREATE TABLE loomplane_placement_groups (
 org_id text NOT NULL,id text NOT NULL,project_id text NOT NULL,environment_id text NOT NULL,name text NOT NULL,
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,id),UNIQUE(org_id,project_id,environment_id,name),UNIQUE(org_id,project_id,environment_id,id),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE RESTRICT
);
CREATE TABLE loomplane_placement_group_revisions (
 org_id text NOT NULL,group_id text NOT NULL,generation bigint NOT NULL CHECK(generation>0),plan_id text NOT NULL,
 revision text NOT NULL,body jsonb NOT NULL,schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,group_id,generation),UNIQUE(org_id,group_id,plan_id),
 FOREIGN KEY(org_id,group_id) REFERENCES loomplane_placement_groups(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,plan_id) REFERENCES loomplane_plans(org_id,id) ON DELETE RESTRICT,
 CHECK(coalesce(body->>'org_id'=org_id AND body->>'group_id'=group_id AND (body->>'generation')::bigint=generation AND body->>'revision'=revision,false))
);
CREATE TABLE loomplane_placement_group_members (
 org_id text NOT NULL,group_id text NOT NULL,generation bigint NOT NULL,member_id text NOT NULL,
 destination_id text NOT NULL,target_id text NOT NULL,attachment_id text NOT NULL,prerequisite_id text NOT NULL,
 physical_identity text NOT NULL,binding_id text NOT NULL UNIQUE,instance_id text NOT NULL UNIQUE,runtime_ref text NOT NULL UNIQUE,release_id text NOT NULL UNIQUE,
 revision text NOT NULL,body jsonb NOT NULL,schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,group_id,generation,member_id),UNIQUE(org_id,group_id,generation,physical_identity),
 FOREIGN KEY(org_id,group_id,generation) REFERENCES loomplane_placement_group_revisions(org_id,group_id,generation) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,target_id) REFERENCES loomplane_cloud_targets(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,prerequisite_id) REFERENCES loomplane_member_prerequisites(org_id,id) ON DELETE RESTRICT,
 CHECK(coalesce(body->>'destination_id'=destination_id AND body->>'target_id'=target_id AND body->'prerequisite'->>'attachment_id'=attachment_id AND body->'prerequisite'->>'id'=prerequisite_id AND physical_identity=(body->'physical_identity'->>'account')||'/'||(body->'physical_identity'->>'namespace_uid') AND body->'identity'->>'group_id'=group_id AND (body->'identity'->>'generation')::bigint=generation AND body->'identity'->>'member_id'=member_id AND body->'identity'->>'binding_id'=binding_id AND body->'identity'->>'instance_id'=instance_id AND body->'identity'->>'runtime_ref'=runtime_ref AND body->'identity'->>'release_id'=release_id,false))
);
CREATE FUNCTION loomplane_group_member_current(o text,pj text,en text,j jsonb,config text,checked timestamptz) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE valid boolean;
BEGIN
 SELECT true INTO valid FROM loomplane_group_destination_mappings m
 JOIN loomplane_federation_destinations d ON d.org_id=m.org_id AND d.project_id=m.project_id AND d.environment_id=m.environment_id AND d.id=m.destination_id
 JOIN loomplane_cloud_targets t ON t.org_id=m.org_id AND t.id=m.target_id
 JOIN loomplane_runtime_attachments a ON a.org_id=m.org_id AND a.id=m.attachment_id
 JOIN loomplane_cloud_connections c ON c.org_id=a.org_id AND c.id=a.runtime_connection_id
 JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=pj
 JOIN loomplane_cloud_locations l ON l.org_id=t.org_id AND l.cloud_id=t.cloud_id AND l.id=t.location_id
 JOIN loomplane_member_prerequisites p ON p.org_id=m.org_id AND p.project_id=pj AND p.attachment_id=a.id AND p.id=j->'prerequisite'->>'id'
 JOIN loomplane_tls_renewal_authorities r ON r.org_id=p.org_id AND r.id=p.authority_id AND r.project_id=pj AND r.attachment_id=a.id
 WHERE m.org_id=o AND m.project_id=pj AND m.environment_id=en AND m.destination_id=j->>'destination_id'
 AND m.revision=(j->>'mapping_revision')::bigint
 AND d.generation=(j->>'destination_generation')::bigint AND NOT d.quarantined AND d.identity=j->'physical_identity'
 AND d.capabilities->>'revision'=j->>'capability_revision' AND d.capabilities->>'identity_revision'=d.identity_revision
 AND (d.capabilities->>'checked_at')::timestamptz<=checked AND (d.capabilities->>'expires_at')::timestamptz>checked
 AND d.capabilities->'results'->'lifecycle.observe'->>'state'='supported'
 AND d.capabilities->'results'->'lifecycle.immutable'->>'state'='supported'
 AND d.capabilities->'results'->'protocol.http'->>'state'='supported'
 AND t.id=j->>'target_id' AND t.cloud_id=j->>'cloud_id' AND t.revision=(j->>'target_revision')::bigint AND t.project_id=pj
 AND t.provider='kubernetes' AND t.connection_id=a.runtime_connection_id AND t.managed_resource_id=a.cluster_id AND t.cloud_id=a.cloud_id
 AND a.project_id=pj AND a.deleted_at IS NULL AND coalesce(a.body->>'retirement_operation_id','')=''
 AND a.revision=(j->>'attachment_revision')::bigint AND a.namespace_uid=j->'prerequisite'->>'namespace_uid'
 AND c.id=d.connection_id AND c.id=j->>'connection_id' AND c.generation=(j->>'connection_generation')::bigint
 AND c.generation=(a.body->>'trust_revision')::bigint AND c.status='ready' AND c.checked_at<=checked AND c.checked_at>checked-interval '5 minutes'
 AND c.details->>'namespace_uid'=a.namespace_uid AND c.details->>'namespace'=a.namespace AND c.details->>'cluster_ca'=a.trust_fingerprint
 AND d.identity->>'provider'='kubernetes' AND d.identity->>'resource_id'=a.namespace_uid AND d.identity->>'namespace_uid'=a.namespace_uid AND d.identity->>'account'=a.trust_fingerprint
 AND NOT g.revoked AND g.revision=(j->>'grant_revision')::bigint AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))
 AND encode(sha256(convert_to(l.provider||'/'||l.account_id||'/'||l.region,'UTF8')),'hex')=j->>'location_identity' AND l.id=j->>'location_id' AND (l.body->>'revision')::bigint=(j->>'location_revision')::bigint
 AND (l.body->>'evidence_revision')::bigint=(j->>'location_evidence_revision')::bigint AND l.body->>'revision'=l.body->>'evidence_revision'
 AND l.body->>'region'=t.region AND coalesce(l.body->>'verified_by','')<>'' AND coalesce(l.body->>'evidence_ref','')<>'' AND (l.body->>'checked_at')::timestamptz<=checked
 AND p.revision=(j->'prerequisite'->>'revision')::bigint AND p.body @> (j->'prerequisite')
 AND r.active AND r.generation=(j->'prerequisite'->>'renewal_authority_generation')::bigint
 AND r.id=j->'prerequisite'->>'renewal_authority_id' AND r.body->>'method' IN ('external-dns01','external-equivalent')
 AND (j->'prerequisite'->'drain_policy'->>'mode'='attested' OR EXISTS(
  SELECT 1 FROM loomplane_group_timeout_verifications v WHERE v.org_id=o AND v.project_id=pj AND v.attachment_id=a.id
   AND v.id=j->'prerequisite'->>'timeout_verification_id' AND v.namespace_uid=a.namespace_uid AND v.config_revision=config AND v.policy=j->'prerequisite'->'drain_policy'))
 FOR SHARE OF m,d,t,a,c,g,l,p,r;
 RETURN coalesce(valid,false);
END $$;
CREATE FUNCTION loomplane_group_review_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE review jsonb; project text; environment text;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF TG_TABLE_NAME='loomplane_placement_group_revisions' THEN
  SELECT p.project_id,p.environment_id INTO project,environment FROM loomplane_plans p JOIN loomplane_placement_groups g ON g.org_id=p.org_id AND g.project_id=p.project_id AND g.environment_id=p.environment_id
   WHERE p.org_id=NEW.org_id AND p.id=NEW.plan_id AND g.id=NEW.group_id AND p.status='planning' FOR SHARE OF p,g;
  IF project IS NULL OR NOT coalesce(NEW.body->>'project_id'=project AND NEW.body->>'environment_id'=environment AND jsonb_array_length(NEW.body->'members') BETWEEN 2 AND 20 AND (NEW.body->>'min_ready')::integer=jsonb_array_length(NEW.body->'members'),false) THEN
   RAISE EXCEPTION 'placement group review scope changed' USING ERRCODE='23514';
  END IF;
 ELSE
  SELECT r.body INTO review FROM loomplane_placement_group_revisions r WHERE r.org_id=NEW.org_id AND r.group_id=NEW.group_id AND r.generation=NEW.generation;
  IF NOT EXISTS(SELECT 1 FROM jsonb_array_elements(review->'members') item WHERE item=NEW.body) OR NOT loomplane_group_member_current(NEW.org_id,review->>'project_id',review->>'environment_id',NEW.body,review->>'config_revision',now()) THEN
   RAISE EXCEPTION 'placement group member authority changed' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER group_review_guard BEFORE INSERT ON loomplane_placement_group_revisions FOR EACH ROW EXECUTE FUNCTION loomplane_group_review_guard();
CREATE TRIGGER group_member_guard BEFORE INSERT ON loomplane_placement_group_members FOR EACH ROW EXECUTE FUNCTION loomplane_group_review_guard();
CREATE FUNCTION loomplane_group_activation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='approved' AND NEW.result ? 'placement_groups' THEN
  RAISE EXCEPTION 'placement group runtime is unavailable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER group_activation_guard BEFORE INSERT OR UPDATE ON loomplane_plans FOR EACH ROW EXECUTE FUNCTION loomplane_group_activation_guard();
CREATE FUNCTION loomplane_group_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'placement group review identity is immutable' USING ERRCODE='23514'; END $$;
CREATE TRIGGER group_revision_immutable BEFORE UPDATE OR DELETE ON loomplane_placement_group_revisions FOR EACH ROW EXECUTE FUNCTION loomplane_group_immutable();
CREATE TRIGGER group_member_immutable BEFORE UPDATE OR DELETE ON loomplane_placement_group_members FOR EACH ROW EXECUTE FUNCTION loomplane_group_immutable();
CREATE TRIGGER group_timeout_immutable BEFORE UPDATE OR DELETE ON loomplane_group_timeout_verifications FOR EACH ROW EXECUTE FUNCTION loomplane_group_immutable();
CREATE FUNCTION loomplane_group_metadata_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_revision bigint; new_revision bigint;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF TG_OP='UPDATE' THEN
  old_revision:=coalesce((to_jsonb(OLD)->>'revision')::bigint,(to_jsonb(OLD)->>'generation')::bigint);
  new_revision:=coalesce((to_jsonb(NEW)->>'revision')::bigint,(to_jsonb(NEW)->>'generation')::bigint);
  IF NEW.org_id<>OLD.org_id OR NEW.created_at<>OLD.created_at OR new_revision<>old_revision+1 OR
    (to_jsonb(NEW)->>'id') IS DISTINCT FROM (to_jsonb(OLD)->>'id') OR
    (to_jsonb(NEW)->>'prerequisite_id') IS DISTINCT FROM (to_jsonb(OLD)->>'prerequisite_id') OR
    (to_jsonb(NEW)->>'project_id') IS DISTINCT FROM (to_jsonb(OLD)->>'project_id') OR
    (to_jsonb(NEW)->>'attachment_id') IS DISTINCT FROM (to_jsonb(OLD)->>'attachment_id') OR
    (to_jsonb(NEW)->>'environment_id') IS DISTINCT FROM (to_jsonb(OLD)->>'environment_id') OR
    (to_jsonb(NEW)->>'destination_id') IS DISTINCT FROM (to_jsonb(OLD)->>'destination_id') OR
    (to_jsonb(NEW)->>'name') IS DISTINCT FROM (to_jsonb(OLD)->>'name') THEN
   RAISE EXCEPTION 'placement group metadata identity or revision changed' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER group_authority_guard BEFORE INSERT OR UPDATE ON loomplane_tls_renewal_authorities FOR EACH ROW EXECUTE FUNCTION loomplane_group_metadata_guard();
CREATE TRIGGER group_prerequisite_guard BEFORE INSERT OR UPDATE ON loomplane_member_prerequisites FOR EACH ROW EXECUTE FUNCTION loomplane_group_metadata_guard();
CREATE TRIGGER group_observation_guard BEFORE INSERT OR UPDATE ON loomplane_member_prerequisite_observations FOR EACH ROW EXECUTE FUNCTION loomplane_group_metadata_guard();
CREATE TRIGGER group_mapping_guard BEFORE INSERT OR UPDATE ON loomplane_group_destination_mappings FOR EACH ROW EXECUTE FUNCTION loomplane_group_metadata_guard();
CREATE TRIGGER group_header_guard BEFORE INSERT OR UPDATE ON loomplane_placement_groups FOR EACH ROW EXECUTE FUNCTION loomplane_group_metadata_guard();
`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TRIGGER group_activation_guard ON loomplane_plans; DROP FUNCTION loomplane_group_activation_guard(); DROP TABLE loomplane_placement_group_members,loomplane_placement_group_revisions,loomplane_placement_groups,loomplane_group_destination_mappings,loomplane_member_prerequisite_observations,loomplane_member_prerequisites,loomplane_group_timeout_verifications,loomplane_tls_renewal_authorities; DROP FUNCTION loomplane_group_immutable(); DROP FUNCTION loomplane_group_metadata_guard(); DROP FUNCTION loomplane_group_review_guard(); DROP FUNCTION loomplane_group_member_current(text,text,text,jsonb,text,timestamptz); DROP INDEX loomplane_group_target_scope; DROP INDEX loomplane_group_plan_scope;`)
		return err
	})
}
