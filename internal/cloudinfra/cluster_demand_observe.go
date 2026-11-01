package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

// Update the cohort from this exact inventory. An unschedulable external pod
// consumes requests but cannot ask for a Loomplane expansion.
func recordClusterDemandObservation(ctx context.Context, tx driver.Tx, r *ManagedResource, s ClusterCapacitySnapshot) error {
	current := s.Fresh(time.Now()) && s.WorkloadsComplete && r.Observation != nil && freshClusterEnvelopeEvidence(r.Observation.ObservedAt, time.Now())
	if current {
		for _, w := range s.Workloads {
			if !w.SchedulerShortage || w.NamespaceUID == "" || w.ReplicaSetUID == "" || w.ReleaseID == "" {
				continue
			}
			var raw []byte
			err := tx.QueryRow(ctx, `SELECT i.body->'input' FROM loomplane_cluster_workload_intents i JOIN loomplane_plans p ON p.id=i.plan_id AND p.org_id=i.org_id AND p.status='approved' JOIN loomplane_cluster_workload_holds h ON h.org_id=i.org_id AND h.id=i.body->'input'->>'id' AND NOT h.released JOIN loomplane_managed_workload_authority a ON a.org_id=i.org_id AND a.release_id=$4 AND a.body->>'Plan'=i.plan_id AND a.body->>'Service'=i.service AND a.revoked_at IS NULL
 JOIN loomplane_workload_attempts wa ON wa.org_id=i.org_id AND wa.environment_id=p.environment_id AND wa.project_id=p.project_id AND wa.service=i.service AND wa.plan_id=p.id AND wa.deployment_id=a.body->>'Deployment' AND wa.generation=a.epoch AND wa.effects_started AND NOT wa.legacy_unfenced
 JOIN loomplane_workload_controllers c ON c.org_id=a.org_id AND c.connection_id=a.body->>'Connection' AND c.instance_id=a.body->>'Instance' AND c.runtime_ref=a.body->>'RuntimeRef'
 WHERE i.org_id=$1 AND h.cloud_id=$2 AND h.resource_id=$3 AND i.body->>'mode'='guaranteed' AND i.body->'input'->'native' IS NOT NULL
 AND h.body->'attempt'->>'Generation'=a.epoch::text AND h.body->'attempt'->>'Deployment'=wa.deployment_id AND a.body->>'ClusterCA'=$9 AND a.body->>'NamespaceUID'=$5 AND c.body->>'cluster_ca'=$9 AND i.body->'input'->>'connection_id'=a.body->>'Connection' AND EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances j WHERE j.org_id=a.org_id AND j.release_id=a.release_id AND j.epoch=a.epoch AND j.scope=a.body AND j.cleanup_grant_id IS NULL AND j.request->>'Resource'='deployments' AND j.outcome->>'State'='accepted' AND j.outcome->>'UID'=$6 AND j.outcome->>'Generation'=c.body->>'deployment_generation') AND c.body->>'release_id'=a.release_id AND c.body->>'runtime_revision'=a.body->>'RuntimeRevision' AND c.body->>'namespace_uid'=$5 AND c.body->>'deployment_uid'=$6 AND c.body->>'deployment_generation' IN ($7::bigint::text,$8::bigint::text) AND loomplane_cluster_workload_scope_current(p,i.body)
 AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=i.org_id AND d.plan_id=i.plan_id AND d.status='cancelled')`, r.OrgID, r.CloudID, r.ID, w.ReleaseID, w.NamespaceUID, w.DeploymentUID, w.DeploymentGeneration, w.ReplicaProofGeneration, r.Observation.CAHash).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			var in ClusterWorkloadInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return err
			}
			n := in.Native
			if n == nil || !matchesReviewedPod(w.Requests, in.Maximum) {
				continue
			}
			policy := r.ScalingPolicies[n.PoolID]
			if policy.Mode != "native" || policy.PauseReason != "" || policy.Revoked || policy.Revision != n.PolicyRevision || policy.ID != n.PolicyID || policy.MaxNodes != n.MaximumNodes || scalingBreach(policy, *r.Observation) != "" {
				continue
			}
			var profile bool
			for _, p := range s.SchedulingProfiles {
				if p.PoolID == n.PoolID && p.Digest == n.ProfileDigest && (w.schedulerArchitecture == "" || w.schedulerArchitecture == p.Architecture) {
					profile = true
				}
			}
			if !profile {
				continue
			}
			if _, err := joinClusterDemandCohort(ctx, tx, in, n.PoolID, n.ProfileDigest, "scheduler_shortage", s.ObservationRevision); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_cluster_demand_members m USING loomplane_cluster_demand_cohorts c WHERE m.cohort_id=c.id AND c.org_id=$1 AND c.cloud_id=$2 AND c.resource_id=$3 AND m.reason='scheduler_shortage' AND m.observed_revision<>$4`, r.OrgID, r.CloudID, r.ID, s.ObservationRevision); err != nil {
		return err
	}
	return recordLowDemand(ctx, tx, r, s, current)
}

func recordLowDemand(ctx context.Context, tx driver.Tx, r *ManagedResource, s ClusterCapacitySnapshot, current bool) error {
	for pool, policy := range r.ScalingPolicies {
		low := false
		ownsPool := policy.Mode == "native" && !policy.Revoked || policy.Mode == "manual" && policy.Revoked
		if current && ownsPool && policy.PauseReason == "" && scalingConverged(&policy, *r.Observation) && scalingBreach(policy, *r.Observation) == "" {
			holds, err := clusterWorkloadHolds(ctx, tx, r.OrgID, r.ID)
			if err != nil {
				return err
			}
			used, err := chargedClusterWorkloads(ctx, tx, s, holds)
			if err != nil {
				return err
			}
			low = poolCouldShrink(s, used, policy)
			for _, other := range r.ScalingPolicies {
				if !other.Revoked && other.ContinuityPoolID == pool {
					low = false
				}
			}
			var physical int
			for _, n := range s.Nodes {
				if n.PoolID == pool {
					physical++
				}
			}
			for _, known := range r.Observation.Pools {
				if known.ID == pool && known.Count != physical {
					low = false
				}
			}
		}
		var waiting bool
		if low {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cluster_demand_cohorts WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3 AND pool_id=$4 AND closed_at IS NULL)`, r.OrgID, r.CloudID, r.ID, pool).Scan(&waiting); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO loomplane_cluster_pool_advice AS prev(org_id,cloud_id,resource_id,pool_id,policy_revision,observed_at,low_since) VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $7 THEN $6::timestamptz END)
 ON CONFLICT(org_id,cloud_id,resource_id,pool_id) DO UPDATE SET policy_revision=excluded.policy_revision,observed_at=excluded.observed_at,
 low_since=CASE WHEN $7 THEN CASE WHEN prev.policy_revision=excluded.policy_revision AND prev.observed_at>=excluded.observed_at-interval '2 minutes' THEN coalesce(prev.low_since,excluded.observed_at) ELSE excluded.observed_at END END,
 review_decrease=$7 AND prev.policy_revision=excluded.policy_revision AND prev.observed_at>=excluded.observed_at-interval '2 minutes' AND coalesce(prev.low_since<=excluded.observed_at-interval '30 minutes',false)
 WHERE prev.observed_at<=excluded.observed_at`, r.OrgID, r.CloudID, r.ID, pool, policy.Revision, s.CheckedAt, low && !waiting)
		if err != nil {
			return err
		}
	}
	return nil
}

func poolCouldShrink(s ClusterCapacitySnapshot, used ClusterWorkloadVector, policy ClusterScalingPolicy) bool {
	if len(s.Volumes) > 0 {
		return false
	}
	var count int64
	var vacant bool
	for _, n := range s.Nodes {
		if n.PoolID != policy.PoolID {
			continue
		}
		if !n.Ready || n.Unschedulable || len(n.Taints) > 0 || n.NodeUID == "" || n.Requests.Pods == nil {
			return false
		}
		count++
		source := n.Provenance["requests/pods"]
		vacant = vacant || (*n.Requests.Pods == 0 && source.Source == "kubernetes" && source.Identity == n.NodeUID && !source.CheckedAt.IsZero() && source.CheckedAt.Equal(s.StorageProvenance.CheckedAt))
	}
	if !vacant || count <= int64(max(1, policy.MinNodes)) {
		return false
	}
	// A nonempty candidate needs a separate drain review for pods, disruption
	// budgets and local data. Keep every persistent volume and continuity pool.
	var profile *ClusterSchedulingProfile
	for i := range s.SchedulingProfiles {
		if s.SchedulingProfiles[i].PoolID == policy.PoolID {
			if profile != nil {
				return false
			}
			profile = &s.SchedulingProfiles[i]
		}
	}
	if profile == nil {
		return false
	}
	spare := workloadDimensions(profile.Available)
	for _, w := range s.Workloads {
		if w.Pending {
			return false
		}
	}
	for i, q := range workloadDimensions(used) {
		if spare[i] < 0 || q/(count-1) > spare[i] || q/(count-1) == spare[i] && q%(count-1) > 0 {
			return false
		}
	}
	return true
}

// CurrentClusterPoolAdvice only reports a sustained window from a still-current
// inventory and policy. It does not authorize a drain or a smaller pool.
func (s *PgStore) CurrentClusterPoolAdvice(ctx context.Context, org, cloud, resource, pool string) (bool, error) {
	var eligible bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cluster_pool_advice a
 JOIN loomplane_managed_resources r ON r.org_id=a.org_id AND r.cloud_id=a.cloud_id AND r.id=a.resource_id AND r.deleted_at IS NULL
 JOIN loomplane_cluster_capacity_inventory i ON i.org_id=a.org_id AND i.cloud_id=a.cloud_id AND i.resource_id=a.resource_id AND i.complete AND i.saved_revision=i.issued_revision AND i.checked_at=a.observed_at
 WHERE a.org_id=$1 AND a.cloud_id=$2 AND a.resource_id=$3 AND a.pool_id=$4 AND a.review_decrease AND coalesce(a.retry_observation_at<=clock_timestamp(),true) AND a.observed_at BETWEEN clock_timestamp()-interval '2 minutes' AND clock_timestamp() AND r.body->'scaling_policies'->$4->>'revision'=a.policy_revision::text AND ((r.body->'scaling_policies'->$4->>'mode'='native' AND r.body->'scaling_policies'->$4->>'revoked'='false') OR (r.body->'scaling_policies'->$4->>'mode'='manual' AND r.body->'scaling_policies'->$4->>'revoked'='true'))
 AND r.body->'observation'->>'state'='running' AND (r.body->'observation'->>'observed_at')::timestamptz BETWEEN clock_timestamp()-interval '2 minutes' AND clock_timestamp() AND coalesce(r.body->'scaling_policies'->$4->>'pause_reason','')='' AND NOT EXISTS(SELECT 1 FROM loomplane_cluster_demand_cohorts c WHERE c.org_id=a.org_id AND c.cloud_id=a.cloud_id AND c.resource_id=a.resource_id AND c.pool_id=a.pool_id AND c.closed_at IS NULL))`, org, cloud, resource, pool).Scan(&eligible)
	return eligible, err
}

// Divide the signed maximum by its replica bound. Comparing by division
// prevents a large, mutated pod request from overflowing a multiplication.
func matchesReviewedPod(actual, maximum ClusterWorkloadVector) bool {
	if maximum.Pods <= 0 || actual.Pods != 1 || actual.CPUMillis <= 0 || actual.MemoryBytes <= 0 {
		return false
	}
	a, m := workloadDimensions(actual), workloadDimensions(maximum)
	for i, v := range m {
		if v%maximum.Pods != 0 || v/maximum.Pods != a[i] {
			return false
		}
	}
	return true
}
