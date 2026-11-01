package planning

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

// ReconcileClusterDemand accepts a durable demand identity. Every retry reloads
// the approved action, including the scheduler profile and maximum.
func (s *Service) ReconcileClusterDemand(ctx context.Context, org, cloud, demandID string) (cloudinfra.CapacityDecision, error) {
	var decision cloudinfra.CapacityDecision
	st, ok := s.store.(*PgStore)
	if !ok {
		return decision, ErrUnavailable
	}
	if org == "" || cloud == "" || demandID == "" {
		return decision, ErrInvalid
	}
	var planID, service string
	var cancelled bool
	if err := st.db.QueryRow(ctx, `SELECT i.plan_id,i.service,EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=i.org_id AND d.plan_id=i.plan_id AND d.status='cancelled') FROM loomplane_cluster_workload_intents i WHERE loomplane_cluster_controls_current($1,i.body->'input'->>'resource_id') AND i.org_id=$1 AND i.body->'input'->>'cloud_id'=$2 AND i.body->'input'->>'id'=$3`, org, cloud, demandID).Scan(&planID, &service, &cancelled); err != nil {
		return decision, err
	}
	if cancelled {
		decision.Block = &cloudinfra.CapacityBlock{Reason: "cluster_demand_cancelled", CloudID: cloud}
		return decision, nil
	}
	plan, err := st.GetPlan(ctx, org, planID)
	if err != nil {
		return decision, err
	}
	if plan.Status != StatusApproved {
		return decision, ErrNotReady
	}
	for _, a := range plan.Actions {
		if a.Name != service || a.Service == nil || a.Service.ClusterDemand == nil {
			continue
		}
		d := a.Service.ClusterDemand
		if d.Input.ID != demandID || d.Input.CloudID != cloud || d.Input.OrgID != org {
			return decision, ErrConflict
		}
		if err := s.resolveClusterWorkload(ctx, st, plan, a); err != nil {
			var b *cloudinfra.CapacityAdmissionError
			var wait *cloudinfra.ClusterDemandWaitError
			if errors.As(err, &wait) {
				decision.ClusterCohortID = wait.Cohort
				decision.ClusterDemandState = "awaiting_review"
			}
			if errors.As(err, &b) {
				decision.Block = &b.Block
				return decision, nil
			}
			return decision, err
		}
		decision.ClusterPlacement = &cloudinfra.ClusterDemandPlacement{ResourceID: d.Input.ResourceID, TargetID: d.Input.TargetID, DemandID: d.Input.ID, Mode: d.Mode}
		if d.Profile != nil {
			decision.ClusterPlacement.PoolID = d.Profile.PoolID
		}
		decision.ClusterDemandState = "admitted"
		if d.Input.Native != nil {
			var group *string
			err := st.db.QueryRow(ctx, `SELECT (SELECT c.id FROM loomplane_cluster_demand_cohorts c JOIN loomplane_cluster_demand_members m ON m.cohort_id=c.id JOIN loomplane_cluster_capacity_inventory i ON i.org_id=c.org_id AND i.cloud_id=c.cloud_id AND i.resource_id=c.resource_id AND i.saved_revision=m.observed_revision AND i.saved_revision=i.issued_revision AND i.complete AND i.checked_at BETWEEN clock_timestamp()-interval '2 minutes' AND clock_timestamp() WHERE m.org_id=$1 AND m.demand_id=$2 AND m.reason='scheduler_shortage' AND c.closed_at IS NULL AND c.pool_id=$3 AND c.profile=$4 AND c.policy_revision=$5)`, org, d.Input.ID, d.Input.Native.PoolID, d.Input.Native.ProfileDigest, d.Input.Native.PolicyRevision).Scan(&group)
			if err != nil {
				return decision, err
			}
			if group != nil {
				decision.ClusterCohortID = *group
				decision.ClusterDemandState = "waiting_native_capacity"
			}
		}
		return decision, nil
	}
	return decision, ErrConflict
}
