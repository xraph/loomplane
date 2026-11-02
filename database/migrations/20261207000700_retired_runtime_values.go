package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	managedInstances.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_retired_runtime_removals(org_id text NOT NULL,instance_id text NOT NULL,grant_id text NOT NULL,id text NOT NULL UNIQUE CHECK(id<>''),issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),resolved_at timestamptz,PRIMARY KEY(org_id,instance_id),FOREIGN KEY(org_id,instance_id) REFERENCES loomplane_managed_retired_instances(org_id,instance_id),FOREIGN KEY(org_id,grant_id) REFERENCES loomplane_managed_cleanup_grants(org_id,id));
CREATE FUNCTION loomplane_retain_retired_ref_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR (NEW.org_id,NEW.instance_id,NEW.grant_id,NEW.id,NEW.issued_at) IS DISTINCT FROM (OLD.org_id,OLD.instance_id,OLD.grant_id,OLD.id,OLD.issued_at) OR (OLD.resolved_at IS NOT NULL AND NEW.resolved_at IS DISTINCT FROM OLD.resolved_at) OR NEW.resolved_at<NEW.issued_at OR NEW.resolved_at>clock_timestamp() THEN RAISE EXCEPTION 'retired runtime removal evidence must be retained' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER loomplane_retain_retired_ref_removal BEFORE UPDATE OR DELETE ON loomplane_retired_runtime_removals FOR EACH ROW EXECUTE FUNCTION loomplane_retain_retired_ref_removal();
CREATE INDEX loomplane_retired_runtime_ref ON loomplane_managed_retired_instances(org_id,runtime_ref) WHERE runtime_ref<>'';
CREATE FUNCTION loomplane_lock_retired_runtime_ref(p_org text,p_ref text) RETURNS void LANGUAGE sql AS $$ SELECT pg_advisory_xact_lock(hashtextextended('retired-runtime-ref/'||p_org||'/'||p_ref,0)) $$;
CREATE FUNCTION loomplane_guard_retired_runtime_ref() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE tenant text; instance text; ref text;
BEGIN
 IF TG_TABLE_NAME='cp_instances' THEN tenant:=NEW.tenant_id;instance:=NEW.id;ref:=NEW.labels->>'loomplane.runtime.ref';
 ELSE tenant:=NEW.org_id;instance:=NEW.body->>'Instance';ref:=NEW.body->>'RuntimeRef';IF NEW.revoked_at IS NOT NULL THEN RETURN NEW; END IF; END IF;
 IF ref IS NULL OR ref='' THEN RETURN NEW; END IF;
 PERFORM loomplane_lock_retired_runtime_ref(tenant,ref);
 IF EXISTS(SELECT 1 FROM loomplane_managed_retired_instances r WHERE r.org_id=tenant AND r.runtime_ref=ref AND r.instance_id<>instance AND NOT EXISTS(SELECT 1 FROM loomplane_retired_runtime_removals v WHERE v.org_id=r.org_id AND v.instance_id=r.instance_id AND v.resolved_at IS NOT NULL))
 THEN RAISE EXCEPTION 'the original runtime values are still being removed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_retired_value_adoption BEFORE INSERT OR UPDATE OF body ON loomplane_managed_workload_authority FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_runtime_ref();
CREATE TRIGGER loomplane_guard_retired_value_instance BEFORE INSERT OR UPDATE OF labels,tenant_id,id ON cp_instances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_runtime_ref();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_retired_runtime_removals) OR EXISTS(SELECT 1 FROM loomplane_managed_retired_instances WHERE runtime_ref<>'') THEN RAISE EXCEPTION 'retired runtime value evidence must be retained'; END IF; END $$;
DROP TRIGGER loomplane_guard_retired_value_adoption ON loomplane_managed_workload_authority;
DROP TRIGGER loomplane_guard_retired_value_instance ON cp_instances;
DROP FUNCTION loomplane_guard_retired_runtime_ref(),loomplane_lock_retired_runtime_ref(text,text);
DROP INDEX loomplane_retired_runtime_ref;
DROP TABLE loomplane_retired_runtime_removals;
DROP FUNCTION loomplane_retain_retired_ref_removal();`)
		return err
	})
}
