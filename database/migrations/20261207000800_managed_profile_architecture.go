package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE FUNCTION loomplane_guard_profile_architecture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE demand jsonb; arch text;
BEGIN
 IF NEW.cleanup_grant_id IS NOT NULL THEN RETURN NEW; END IF;
 SELECT i.body INTO demand FROM loomplane_cluster_workload_intents i WHERE i.org_id=NEW.org_id AND i.plan_id=NEW.scope->>'Plan' AND i.service=NEW.scope->>'Service';
 IF demand->>'profile_architecture' IS DISTINCT FROM 'true' THEN RETURN NEW; END IF;
 arch:=demand->'profile'->>'architecture';
 IF arch IS NULL OR arch NOT IN ('amd64','arm64') OR demand->>'mode' IS DISTINCT FROM 'guaranteed' THEN RAISE EXCEPTION 'native_template_unqualified' USING ERRCODE='23514'; END IF;
 IF NEW.request->>'Resource'='deployments' AND NEW.request->>'Method' IN ('POST','PUT') THEN
  IF NEW.request->>'SchedulingArchitecture' IS DISTINCT FROM arch THEN RAISE EXCEPTION 'managed deployment requires its reviewed architecture' USING ERRCODE='23514'; END IF;
 ELSIF EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=NEW.org_id AND w.release_id=NEW.release_id AND w.epoch=NEW.epoch AND w.cleanup_grant_id IS NULL AND w.request->>'Resource'='deployments' AND w.request->>'Method' IN ('POST','PUT') AND w.outcome->>'State'='accepted') THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM loomplane_cluster_capacity_inventory i WHERE i.org_id=NEW.org_id AND i.cloud_id=demand->'input'->>'cloud_id' AND i.resource_id=demand->'input'->>'resource_id' AND i.complete AND i.saved_revision=i.issued_revision AND i.checked_at<=clock_timestamp() AND i.checked_at>clock_timestamp()-interval '2 minutes'
 AND i.body->>'workloads_complete'='true' AND jsonb_array_length(i.body->'nodes')>0 AND EXISTS(SELECT 1 FROM jsonb_array_elements(i.body->'scheduling_profiles') profile WHERE profile=demand->'profile')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(i.body->'nodes') n WHERE n->>'architecture' IS DISTINCT FROM arch OR n->>'ready' IS DISTINCT FROM 'true' OR n->>'unschedulable' IS DISTINCT FROM 'false' OR jsonb_array_length(coalesce(nullif(n->'taints','null'::jsonb),'[]'))>0 OR EXISTS(SELECT 1 FROM jsonb_each_text(coalesce(nullif(n->'allocatable'->'extended','null'::jsonb),'{}')) resource WHERE resource.value::bigint<>0))
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(i.body->'scheduling_profiles') profile WHERE profile->>'architecture' IS DISTINCT FROM arch))
 THEN RAISE EXCEPTION 'native_template_unqualified' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_profile_architecture BEFORE INSERT ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_profile_architecture();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_cluster_workload_intents WHERE body->>'profile_architecture'='true') THEN RAISE EXCEPTION 'reviewed workload architecture must be retained'; END IF; END $$;
DROP TRIGGER loomplane_guard_profile_architecture ON loomplane_managed_workload_issuances; DROP FUNCTION loomplane_guard_profile_architecture();`)
		return err
	})
}
