package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"
)

type clusterGrowthCooldownError struct{ until time.Time }

func (e *clusterGrowthCooldownError) Error() string {
	return "cluster scale-up cooldown until " + e.until.Format(time.RFC3339Nano)
}
func (e *clusterGrowthCooldownError) Unwrap() error { return ErrPending }

func poolGrowthCooldown(o *ClusterOperation, r *ManagedResource) (int64, error) {
	pool := o.Input.Maintenance.PoolID
	old := r.ScalingPolicies[pool]
	sec := old.CooldownSeconds
	if sec == 0 {
		sec = 600
	}
	if o.Kind == "node-pool-update" {
		if old.ID == "" || o.CapacityTarget.Servers == 0 {
			return 0, nil
		}
	} else {
		next := o.Input.Scaling
		if next == nil || next.PoolID != pool {
			return 0, ErrInvalid
		}
		if next.Mode != "native" {
			return 0, nil
		}
		if old.Mode == "native" && !old.Revoked && next.MaxNodes <= old.MaxNodes && next.MinNodes <= old.MinNodes {
			return 0, nil
		}
		sec = next.CooldownSeconds
	}
	if sec < 1 || sec > 86400 {
		return 0, ErrInvalid
	}
	return sec, nil
}

// The worker records growth with its first attempt. An uncertain provider
// response keeps the cooldown. Observation and receipt recovery never extend it.
func recordClusterGrowthTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	if o.Kind != "node-pool-update" && o.Kind != "node-pool-scaling" {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT body FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, o.OrgID, o.CloudID, o.Input.ResourceID).Scan(&raw); err != nil {
		return err
	}
	var r ManagedResource
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	sec, err := poolGrowthCooldown(o, &r)
	if err != nil || sec == 0 {
		return err
	}
	var until *time.Time
	if checkErr := tx.QueryRow(ctx, `SELECT loomplane_claim_pool_growth($1,$2,$3,$4,$5,$6)`, o.OrgID, o.CloudID, r.ID, o.Input.Maintenance.PoolID, o.ID, sec).Scan(&until); checkErr != nil {
		return checkErr
	}
	if until != nil {
		return &clusterGrowthCooldownError{until: *until}
	}
	// A reviewed growth may address several compatible waiters without giving
	// any waiter authority to issue the operation. Native pod observations never
	// reach this first-attempt transaction.
	_, err = tx.Exec(ctx, `UPDATE loomplane_cluster_demand_cohorts c
 SET operation_id=$5,issued_at=clock_timestamp()
 WHERE c.org_id=$1 AND c.cloud_id=$2 AND c.resource_id=$3 AND c.pool_id=$4 AND c.closed_at IS NULL AND c.policy_revision=$6 AND c.budget_revision=$7 AND $7>0
 AND EXISTS(SELECT 1 FROM loomplane_cloud_capacity_policies b WHERE b.org_id=c.org_id AND b.cloud_id=c.cloud_id AND b.revision=c.budget_revision)
 AND EXISTS(SELECT 1 FROM loomplane_cluster_capacity_inventory i CROSS JOIN LATERAL jsonb_array_elements(i.body->'scheduling_profiles') p
 WHERE i.org_id=c.org_id AND i.cloud_id=c.cloud_id AND i.resource_id=c.resource_id AND i.issued_revision=i.saved_revision AND i.complete AND i.checked_at BETWEEN clock_timestamp()-interval '2 minutes' AND clock_timestamp() AND p->>'pool_id'=c.pool_id AND p->>'digest'=c.profile)
 AND EXISTS(SELECT 1 FROM loomplane_cluster_demand_members m JOIN loomplane_cluster_workload_intents i ON i.org_id=m.org_id AND i.plan_id=m.plan_id AND i.service=m.service
 JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.revision=i.plan_revision AND p.status='approved'
 WHERE m.cohort_id=c.id AND m.reason='review_capacity' AND i.body->'input'->>'id'=m.demand_id AND loomplane_cluster_workload_scope_current(p,i.body)
 AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=p.org_id AND d.plan_id=p.id AND d.status='cancelled'))
 AND (c.operation_id IS NULL OR EXISTS(SELECT 1 FROM loomplane_cluster_operations previous WHERE previous.org_id=c.org_id AND previous.id=c.operation_id AND previous.status IN ('succeeded','cancelled','failed')))`, o.OrgID, o.CloudID, r.ID, o.Input.Maintenance.PoolID, o.ID, r.ScalingPolicies[o.Input.Maintenance.PoolID].Revision, o.Input.PolicyRevision)
	return err
}
