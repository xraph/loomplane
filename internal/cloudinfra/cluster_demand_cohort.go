package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"
)

// JoinReviewedClusterDemand records an immutable request that did not fit.
// It must run in the same transaction as its failed reservation.
func JoinReviewedClusterDemand(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, pool, profile string) (string, error) {
	return joinClusterDemandCohort(ctx, tx, in, pool, profile, "review_capacity", 0)
}

// joinClusterDemandCohort runs under the admission or inventory cloud lock.
// The cohort groups a request for review or observed scheduler shortage. It
// cannot issue a count operation or change the approved native bounds.
func joinClusterDemandCohort(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, pool, profile, reason string, revision int64) (string, error) {
	if in.ID == "" || pool == "" || profile == "" || reason != "review_capacity" && reason != "scheduler_shortage" {
		return "", ErrInvalid
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	// Cancellation shares this plan lock so it cannot miss an uncommitted member.
	if _, checkErr := tx.Exec(ctx, `SELECT loomplane_lock_cluster_demand($1,$2)`, in.OrgID, in.PlanID); checkErr != nil {
		return "", checkErr
	}
	var valid bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cluster_workload_intents i JOIN loomplane_plans p ON p.id=i.plan_id AND p.org_id=i.org_id AND p.revision=i.plan_revision AND p.status='approved' WHERE i.org_id=$1 AND i.plan_id=$2 AND i.service=$3 AND i.body->'input'=$4::jsonb AND i.body->>'mode'='guaranteed' AND i.body->'profile'->>'pool_id'=$5 AND i.body->'profile'->>'digest'=$6 AND loomplane_cluster_workload_scope_current(p,i.body) AND NOT EXISTS(SELECT 1 FROM loomplane_deployments x WHERE x.org_id=i.org_id AND x.plan_id=i.plan_id AND x.status='cancelled'))`, in.OrgID, in.PlanID, in.Service, raw, pool, profile).Scan(&valid); checkErr != nil {
		return "", checkErr
	}
	if !valid {
		return "", ErrConflict
	}
	if _, checkErr := tx.Exec(ctx, `SELECT loomplane_cluster_control_supported(schema_version) FROM loomplane_cluster_demand_cohorts WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3 AND pool_id=$4 AND closed_at IS NULL`, in.OrgID, in.CloudID, in.ResourceID, pool); checkErr != nil {
		return "", checkErr
	}
	var policyRevision, budgetRevision int64
	var native bool
	if checkErr := tx.QueryRow(ctx, `SELECT coalesce((r.body->'scaling_policies'->$4->>'revision')::bigint,0),coalesce(r.body->'scaling_policies'->$4->>'mode'='native' AND r.body->'scaling_policies'->$4->>'revoked'='false',false),coalesce((SELECT revision FROM loomplane_cloud_capacity_policies b WHERE b.org_id=r.org_id AND b.cloud_id=r.cloud_id),0) FROM loomplane_managed_resources r WHERE r.org_id=$1 AND r.cloud_id=$2 AND r.id=$3 AND r.deleted_at IS NULL`, in.OrgID, in.CloudID, in.ResourceID, pool).Scan(&policyRevision, &native, &budgetRevision); checkErr != nil {
		return "", checkErr
	}
	if native != (in.Native != nil) || native && policyRevision != in.Native.PolicyRevision {
		return "", ErrConflict
	}

	// The caller already checked this exact profile and policy under the cloud
	// lock. Keep historical operations when replacing an obsolete pending group.
	if _, checkErr := tx.Exec(ctx, `DELETE FROM loomplane_cluster_demand_members m USING loomplane_cluster_demand_cohorts c WHERE m.cohort_id=c.id AND c.org_id=$1 AND c.cloud_id=$2 AND c.resource_id=$3 AND c.pool_id=$4 AND c.closed_at IS NULL AND (c.profile<>$5 OR c.policy_revision<>$6 OR c.budget_revision<>$7 OR c.native<>$8)`, in.OrgID, in.CloudID, in.ResourceID, pool, profile, policyRevision, budgetRevision, native); checkErr != nil {
		return "", checkErr
	}
	generated, generateErr := typeid.Generate("cohort")
	if generateErr != nil {
		return "", generateErr
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_cluster_demand_cohorts(id,org_id,cloud_id,resource_id,pool_id,profile,policy_revision,budget_revision,native)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(org_id,cloud_id,resource_id,pool_id) WHERE closed_at IS NULL DO UPDATE SET pool_id=excluded.pool_id WHERE loomplane_cluster_demand_cohorts.profile=excluded.profile AND loomplane_cluster_demand_cohorts.policy_revision=excluded.policy_revision AND loomplane_cluster_demand_cohorts.budget_revision=excluded.budget_revision AND loomplane_cluster_demand_cohorts.native=excluded.native RETURNING id`, generated.String(), in.OrgID, in.CloudID, in.ResourceID, pool, profile, policyRevision, budgetRevision, native).Scan(&id)
	if err != nil {
		return "", err
	}
	var member string
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_cluster_demand_members(cohort_id,org_id,plan_id,service,demand_id,reason,observed_revision) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(org_id,demand_id) DO UPDATE SET reason=excluded.reason,observed_revision=excluded.observed_revision WHERE loomplane_cluster_demand_members.cohort_id=excluded.cohort_id AND loomplane_cluster_demand_members.plan_id=excluded.plan_id AND loomplane_cluster_demand_members.service=excluded.service RETURNING cohort_id`, id, in.OrgID, in.PlanID, in.Service, in.ID, reason, revision).Scan(&member)
	return member, err
}
