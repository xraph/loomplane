package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ DECLARE original text; replacement text; BEGIN
 original:=pg_get_functiondef('loomplane_check_pending_cleanup_owner()'::regprocedure);
 replacement:=replace(original,'w.request->>''Resource'' NOT IN (''replicasets'',''pods'')','(w.request ? ''CleanupSource'' OR w.request->>''Resource'' NOT IN (''replicasets'',''pods''))');
 IF replacement=original THEN RAISE EXCEPTION 'missing managed cleanup fence'; END IF;
 EXECUTE replace(original,'loomplane_check_pending_cleanup_owner','loomplane_check_pending_cleanup_owner_v1');
 EXECUTE replacement;
END $$;
CREATE FUNCTION loomplane_guard_orphan_cleanup_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.request ? 'CleanupSource' THEN RAISE EXCEPTION 'recovered cleanup issuance must be retained' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 IF TG_OP='UPDATE' AND (OLD.cleanup_children IS NOT NULL OR OLD.request ? 'CleanupSource' OR NEW.request ? 'CleanupSource') THEN
  IF (NEW.id,NEW.org_id,NEW.release_id,NEW.epoch,NEW.scope,NEW.request,NEW.cleanup_grant_id,NEW.cleanup_grant_revision,NEW.issued_at) IS DISTINCT FROM (OLD.id,OLD.org_id,OLD.release_id,OLD.epoch,OLD.scope,OLD.request,OLD.cleanup_grant_id,OLD.cleanup_grant_revision,OLD.issued_at)
  OR (OLD.outcome IS NOT NULL AND (NEW.outcome,NEW.resolved_at) IS DISTINCT FROM (OLD.outcome,OLD.resolved_at)) THEN RAISE EXCEPTION 'cleanup ownership source and accepted outcomes are immutable' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.request ? 'CleanupSource' AND NOT EXISTS(
  SELECT 1 FROM loomplane_managed_workload_issuances p CROSS JOIN LATERAL jsonb_array_elements(p.cleanup_children) c
  WHERE p.id=NEW.request->>'CleanupSource' AND p.org_id=NEW.org_id AND p.outcome->>'State'='accepted' AND p.outcome->>'UID'=p.request->>'UID'
  AND p.scope->>'Connection'=NEW.scope->>'Connection' AND p.scope->>'Instance'=NEW.scope->>'Instance'
  AND p.scope->>'NamespaceUID'=NEW.scope->>'NamespaceUID' AND p.scope->>'ClusterCA'=NEW.scope->>'ClusterCA'
  AND NEW.cleanup_grant_id IS NOT NULL AND c->>'release'=NEW.release_id AND c->>'resource'=NEW.request->>'Resource' AND c->>'name'=NEW.request->>'Name' AND c->>'uid'=NEW.request->>'UID'
  AND NEW.request->>'Namespace'=NEW.scope->>'Namespace' AND NEW.request->>'Version'='v1' AND NEW.request->>'Group'=CASE c->>'resource' WHEN 'pods' THEN '' WHEN 'replicasets' THEN 'apps' END
  AND NEW.request->>'Method'='DELETE' AND NEW.request->>'PropagationPolicy'='Orphan' AND NEW.request->>'ResourceVersion'<>''
 ) THEN RAISE EXCEPTION 'captured cleanup child required' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_orphan_cleanup_source BEFORE INSERT OR UPDATE OR DELETE ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_orphan_cleanup_source();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE request ? 'CleanupSource') THEN RAISE EXCEPTION 'recovered cleanup evidence must be retained'; END IF;
 EXECUTE replace(pg_get_functiondef('loomplane_check_pending_cleanup_owner_v1()'::regprocedure),'loomplane_check_pending_cleanup_owner_v1','loomplane_check_pending_cleanup_owner');
END $$;
DROP FUNCTION loomplane_check_pending_cleanup_owner_v1();
DROP TRIGGER loomplane_guard_orphan_cleanup_source ON loomplane_managed_workload_issuances;
DROP FUNCTION loomplane_guard_orphan_cleanup_source();`)
		return err
	})
}
