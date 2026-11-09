package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `ALTER TABLE loomplane_managed_workload_issuances ADD COLUMN cleanup_children jsonb CHECK(cleanup_children IS NULL OR (cleanup_grant_id IS NOT NULL AND request->>'Method'='DELETE' AND request->>'Group'='apps' AND request->>'Version'='v1' AND request->>'Resource'='deployments' AND request->>'UID'<>'' AND request->>'ResourceVersion'<>'' AND request->>'PropagationPolicy'='Orphan' AND jsonb_typeof(cleanup_children)='array') IS TRUE);
CREATE FUNCTION loomplane_guard_cleanup_children() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.cleanup_children IS NOT NULL THEN RAISE EXCEPTION 'reviewed cleanup children must be retained' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 IF NEW.cleanup_children IS DISTINCT FROM OLD.cleanup_children THEN RAISE EXCEPTION 'reviewed cleanup children are immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_cleanup_children BEFORE DELETE OR UPDATE OF cleanup_children ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_children();
CREATE FUNCTION loomplane_check_pending_cleanup_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE identity jsonb;
BEGIN
 IF TG_TABLE_NAME='loomplane_managed_workload_authority' THEN
  IF NEW.revoked_at IS NOT NULL THEN RETURN NEW; END IF;
  identity:=NEW.body;
 ELSE
  IF NEW.cleanup_grant_id IS NOT NULL THEN RETURN NEW; END IF;
  identity:=NEW.scope;
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w
 WHERE w.org_id=NEW.org_id AND w.outcome IS NULL AND w.cleanup_grant_id IS NOT NULL
 AND w.request->>'Resource' NOT IN ('replicasets','pods')
 AND w.scope->>'Instance'=identity->>'Instance' AND w.scope->>'NamespaceUID'=identity->>'NamespaceUID'
 AND (w.scope->>'Connection'=identity->>'Connection' OR w.scope->>'ClusterCA'=identity->>'ClusterCA')) THEN
  RAISE EXCEPTION 'managed cleanup request still unresolved for this instance' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_pending_cleanup_open BEFORE INSERT OR UPDATE ON loomplane_managed_workload_authority FOR EACH ROW EXECUTE FUNCTION loomplane_check_pending_cleanup_owner();
CREATE TRIGGER loomplane_pending_cleanup_issue BEFORE INSERT ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_check_pending_cleanup_owner();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE cleanup_children IS NOT NULL) THEN RAISE EXCEPTION 'cleanup ownership evidence must be retained'; END IF; END $$;
DROP TRIGGER loomplane_pending_cleanup_open ON loomplane_managed_workload_authority;
DROP TRIGGER loomplane_pending_cleanup_issue ON loomplane_managed_workload_issuances;
DROP FUNCTION loomplane_check_pending_cleanup_owner();
DROP TRIGGER loomplane_guard_cleanup_children ON loomplane_managed_workload_issuances;
DROP FUNCTION loomplane_guard_cleanup_children();
ALTER TABLE loomplane_managed_workload_issuances DROP COLUMN cleanup_children;`)
		return err
	})
}
