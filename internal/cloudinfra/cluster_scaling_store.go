package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/loomplane/pkg/provider"

	"github.com/xraph/grove/driver"
)

func (s *PgStore) ClusterScalingEnvelope(ctx context.Context, org, resource, pool string) (*ClusterCapacityEnvelope, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_cluster_capacity_envelopes WHERE org_id=$1 AND body->>'resource_id'=$2 AND body->>'pool_id'=$3 AND body->>'state'<>'released'`, org, resource, pool).Scan(&raw)
	if err != nil {
		return nil, storeError(err)
	}
	var envelope ClusterCapacityEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	return &envelope, nil
}

func scalingPinsTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT body FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND deleted_at IS NULL FOR UPDATE`, o.OrgID, o.CloudID, o.Input.ResourceID).Scan(&raw); err != nil {
		return storeError(err)
	}
	var resource ManagedResource
	if err := json.Unmarshal(raw, &resource); err != nil {
		return err
	}
	if o.Kind == "node-pool-scaling" {
		policy := o.Input.Scaling
		if policy == nil || resource.ScalingPolicies[policy.PoolID].Revision != policy.ExpectedRevision {
			return ErrConflict
		}
		for _, other := range resource.ScalingPolicies {
			if !other.Revoked && other.PoolID != policy.PoolID && other.ContinuityPoolID == policy.PoolID && policy.Mode == "native" {
				return &ClusterMaintenanceAdmissionError{Reason: "native_continuity_pool"}
			}
		}
		return nil
	}
	if o.Kind == "node-pool-update" || o.Kind == "node-pool-delete" {
		for _, policy := range resource.ScalingPolicies {
			if !policy.Revoked && policy.Mode == "native" && (policy.PoolID == o.Input.Maintenance.PoolID || policy.ContinuityPoolID == o.Input.Maintenance.PoolID) {
				return &ClusterMaintenanceAdmissionError{Reason: "native_policy_owns_pool"}
			}
		}
	}
	return nil
}

func scalingObserverTx(ctx context.Context, tx driver.Tx, org, cloud, resource, pool string, revision int64) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_resources r WHERE r.org_id=$1 AND r.cloud_id=$2 AND r.id=$3 AND r.deleted_at IS NULL AND (r.body->'scaling_policies'->$4->>'revision')::bigint=$5 AND r.body->'scaling_policies'->$4->>'revoked'='false' AND NOT EXISTS(SELECT 1 FROM loomplane_cluster_operations o WHERE o.org_id=r.org_id AND o.resource_id=r.id AND o.status IN ('accepted','running','waiting_provider','needs_attention') AND o.body->>'kind' IN ('retire-cluster','node-pool-scaling')))`, org, cloud, resource, pool, revision).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

func (s *PgStore) ValidateClusterScalingObserver(ctx context.Context, org, cloud, resource, pool string, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := lockCapacityCloud(ctx, tx, org, cloud); err != nil {
		return err
	}
	if err := scalingObserverTx(ctx, tx, org, cloud, resource, pool, revision); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) SaveClusterScalingObservation(ctx context.Context, r *ManagedResource, observation provider.ClusterObservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := lockCapacityCloud(ctx, tx, r.OrgID, r.CloudID); err != nil {
		return err
	}
	if r.Observation == nil || observation.Ref != r.Observation.Ref || observation.Endpoint != r.Observation.Endpoint || observation.CAHash != "" && observation.CAHash != r.Observation.CAHash || !freshClusterEnvelopeEvidence(observation.ObservedAt, time.Now()) {
		return ErrConflict
	}
	for pool, policy := range r.ScalingPolicies {
		if policy.Revoked {
			continue
		}
		if err := scalingObserverTx(ctx, tx, r.OrgID, r.CloudID, r.ID, pool, policy.Revision); err != nil {
			return err
		}
		if reason := scalingBreach(policy, observation); reason != "" {
			policy.PauseReason = reason
			policy.UpdatedAt = time.Now().UTC()
			r.ScalingPolicies[pool] = policy
		}
	}
	observation.CAHash = r.Observation.CAHash
	r.Observation = &observation
	old := r.Revision
	r.Revision++
	r.ObservedRevision = r.Revision
	r.ObservedAt = observation.ObservedAt
	r.UpdatedAt = time.Now().UTC()
	if _, err := claimManagedResourceTx(ctx, tx, r, old); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) ListClusterScalingObservers(ctx context.Context) ([]ClusterScalingObserverScope, error) {
	rows, err := s.db.Query(ctx, `SELECT r.org_id,r.cloud_id,r.id,p.key FROM loomplane_managed_resources r CROSS JOIN LATERAL (SELECT key FROM jsonb_each(coalesce(r.body->'scaling_policies','{}'::jsonb)) WHERE value->>'revoked'='false' AND value->>'mode'='native' ORDER BY key LIMIT 1) p WHERE r.deleted_at IS NULL ORDER BY r.body->>'observed_at',r.id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scopes []ClusterScalingObserverScope
	for rows.Next() {
		var scope ClusterScalingObserverScope
		if err := rows.Scan(&scope.OrgID, &scope.CloudID, &scope.ResourceID, &scope.PoolID); err != nil {
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}
