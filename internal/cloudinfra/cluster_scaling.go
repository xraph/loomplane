package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// ClusterScalingPolicy grants one reviewed pool controller and a scoped observer.
// Cooldown and maximum remain available to durable demand admission.
type ClusterScalingPolicy struct {
	ID               string        `json:"id"`
	OrgID            string        `json:"org_id"`
	SchemaVersion    int           `json:"schema_version"`
	Revision         int64         `json:"revision"`
	ExpectedRevision int64         `json:"expected_revision"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	OperationID      string        `json:"operation_id"`
	ResourceID       string        `json:"resource_id"`
	PoolID           string        `json:"pool_id"`
	Mode             string        `json:"mode"`
	MinNodes         int           `json:"min_nodes"`
	MaxNodes         int           `json:"max_nodes"`
	CooldownSeconds  int64         `json:"cooldown_seconds"`
	ContinuityPoolID string        `json:"continuity_pool_id,omitempty"`
	ContinuityFloor  int           `json:"continuity_floor"`
	PauseReason      string        `json:"pause_reason,omitempty"`
	Revoked          bool          `json:"revoked"`
	ReviewInput      *ClusterInput `json:"-"`
}

func (s *Service) clusterScalingEnabled() error {
	if err := s.clusterEnabled(); err != nil {
		return err
	}
	if !s.ClusterCapacity.Enabled {
		return security.ErrForbidden
	}
	return nil
}

// PreviewClusterScaling accepts the normal cluster review pins through ReviewInput.
// Scope, identity and revision are assigned by the service before persistence.
func (s *Service) PreviewClusterScaling(ctx context.Context, p security.Principal, cloudID, resourceID string, policy ClusterScalingPolicy) (*ClusterOperation, error) {
	if err := s.clusterScalingEnabled(); err != nil {
		return nil, err
	}
	if policy.ReviewInput == nil || policy.ID != "" || policy.OrgID != "" || policy.Revision != 0 || policy.OperationID != "" || policy.PauseReason != "" || policy.Revoked {
		return nil, ErrInvalid
	}
	in := *policy.ReviewInput
	policy.ReviewInput = nil
	in.Kind = "node-pool-scaling"
	in.ResourceID = resourceID
	policy.ResourceID = resourceID
	policy.ID = "scaling-" + uuid.NewString()
	policy.OrgID = p.OrgID
	policy.SchemaVersion = 1
	policy.Revision = policy.ExpectedRevision + 1
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, resourceID)
	if err != nil {
		return nil, err
	}
	if r.CloudID != cloudID || r.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if old, ok := r.ScalingPolicies[policy.PoolID]; ok {
		if old.Revision != policy.ExpectedRevision {
			return nil, ErrConflict
		}
		policy.ID = old.ID
		policy.CreatedAt = old.CreatedAt
	} else if policy.ExpectedRevision != 0 {
		return nil, ErrConflict
	}
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = s.now().UTC()
	}
	policy.UpdatedAt = policy.CreatedAt
	if policy.CooldownSeconds == 0 {
		policy.CooldownSeconds = 600
	}
	in.Scaling = &policy
	return s.PreviewCluster(ctx, p, cloudID, in)
}

func validateScalingPolicy(policy *ClusterScalingPolicy, observation provider.ClusterObservation, current provider.NodePoolSpec) error {
	if policy == nil || policy.PoolID != current.ID || policy.CooldownSeconds < 1 || policy.CooldownSeconds > 86400 || policy.MinNodes < 0 || policy.MaxNodes < policy.MinNodes {
		return ErrInvalid
	}
	switch policy.Mode {
	case "manual":
		if policy.MinNodes != 0 || policy.MaxNodes != 0 || policy.ContinuityPoolID != "" || policy.ContinuityFloor != 0 {
			return ErrInvalid
		}
	case "native":
		if policy.MaxNodes < 1 {
			return ErrInvalid
		}
		if policy.MinNodes == 0 {
			// This is Loomplane's continuity floor, not a provider minimum or health proof.
			valid := false
			for _, pool := range observation.Pools {
				if pool.ID == policy.ContinuityPoolID && pool.ID != current.ID && !pool.Autoscale && pool.Count >= 1 && policy.ContinuityFloor == 1 {
					valid = true
				}
			}
			if !valid {
				return &ClusterMaintenanceAdmissionError{Reason: "continuity_floor_unavailable"}
			}
		} else if policy.ContinuityPoolID != "" || policy.ContinuityFloor != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func scalingConverged(policy *ClusterScalingPolicy, observation provider.ClusterObservation) bool {
	if policy == nil || observation.State != "running" {
		return false
	}
	for _, pool := range observation.Pools {
		if pool.ID != policy.PoolID {
			continue
		}
		if policy.Mode == "manual" {
			return !pool.Autoscale
		}
		return pool.Autoscale && pool.Min == policy.MinNodes && pool.Max == policy.MaxNodes && pool.Count >= pool.Min && pool.Count <= pool.Max
	}
	return false
}

func scalingBreach(policy ClusterScalingPolicy, observation provider.ClusterObservation) string {
	if policy.Revoked || policy.Mode != "native" {
		return ""
	}
	for _, pool := range observation.Pools {
		if pool.ID != policy.PoolID {
			continue
		}
		if !pool.Autoscale || pool.Min != policy.MinNodes || pool.Max != policy.MaxNodes || pool.Count > policy.MaxNodes {
			return "native_bounds_changed"
		}
		if err := validateScalingPolicy(&policy, observation, pool); err != nil {
			return "continuity_floor_lost"
		}
		return ""
	}
	return "native_pool_missing"
}

func (s *Service) prepareScaling(ctx context.Context, p security.Principal, in *ClusterInput, reader provider.ClusterReader, access provider.AccountAccess, r *ManagedResource, observation provider.ClusterObservation) (capacity ResourceVector, nodeCost int64, unknownCosts []string, failure error) {
	var zero ResourceVector
	if err := s.clusterScalingEnabled(); err != nil {
		return zero, 0, nil, err
	}
	if !freshClusterEnvelopeEvidence(observation.ObservedAt, s.now()) {
		return zero, 0, nil, ErrConflict
	}
	policy := in.Scaling
	if policy == nil || policy.SchemaVersion != 1 || policy.OrgID != p.OrgID || policy.ResourceID != r.ID || policy.ID == "" || policy.Revision != policy.ExpectedRevision+1 || policy.OperationID != "" {
		return zero, 0, nil, ErrInvalid
	}
	old := r.ScalingPolicies[policy.PoolID]
	if old.Revision != policy.ExpectedRevision {
		return zero, 0, nil, ErrConflict
	}
	if _, ok := reader.(provider.NodePoolScalingManager); !ok {
		return zero, 0, nil, provider.ErrInfrastructureUnsupported
	}
	var pool *provider.NodePoolSpec
	for i := range observation.Pools {
		if observation.Pools[i].ID == policy.PoolID {
			pool = &observation.Pools[i]
		}
	}
	if pool == nil || in.Maintenance.PoolID != policy.PoolID || in.Maintenance.Pool != nil || in.Maintenance.Update != nil || in.Maintenance.Version != "" {
		return zero, 0, nil, ErrInvalid
	}
	if err := validateScalingPolicy(policy, observation, *pool); err != nil {
		return zero, 0, nil, err
	}
	if policy.Mode == "manual" {
		return zero, 0, nil, nil
	}
	total := policy.MaxNodes
	for _, other := range observation.Pools {
		if other.ID == pool.ID {
			continue
		}
		count := other.Count
		if other.Autoscale {
			count = other.Max
		}
		if count < 0 || count > s.ManagedClusters.MaxNodes-total {
			return zero, 0, nil, ErrInvalid
		}
		total += count
	}
	if total > s.ManagedClusters.MaxNodes {
		return zero, 0, nil, ErrInvalid
	}
	maximum := *pool
	maximum.Count = policy.MaxNodes
	maximum.Autoscale, maximum.Min, maximum.Max = false, 0, 0
	capacity, cost, unknown, err := s.maintenanceCapacity(ctx, p, in, reader, access, []provider.NodePoolSpec{maximum})
	if err != nil {
		return zero, 0, nil, err
	}
	money, err := clusterPoolEnvelopeMoney(in.costEvidence, pool.Name)
	if err != nil {
		return zero, 0, nil, err
	}
	store, ok := s.Clusters.(interface {
		ClusterScalingEnvelope(context.Context, string, string, string) (*ClusterCapacityEnvelope, error)
	})
	if !ok {
		return zero, 0, nil, ErrInvalid
	}
	envelope, err := store.ClusterScalingEnvelope(ctx, p.OrgID, r.ID, pool.ID)
	if err != nil {
		return zero, 0, nil, err
	}
	if in.EnvelopeReview != nil {
		if len(in.EnvelopeReview.Pools) != 1 || in.EnvelopeReview.Pools[0].Maximum != capacity || in.EnvelopeReview.Pools[0].Money.PriceDigest != money.PriceDigest {
			return zero, 0, nil, ErrConflict
		}
		return capacity, cost, unknown, nil
	}
	if envelope.Pending != zero || envelope.State != "active" {
		return zero, 0, nil, ErrPending
	}
	unit, err := multiplyCapacity(capacity, 1)
	if err != nil {
		return zero, 0, nil, err
	}
	unit.Servers /= int64(policy.MaxNodes)
	unit.CPUMillis /= int64(policy.MaxNodes)
	unit.MemoryBytes /= int64(policy.MaxNodes)
	unit.StorageBytes /= int64(policy.MaxNodes)
	review := ClusterPoolEnvelopeReview{EnvelopeID: envelope.ID, Key: envelope.Review.Key, PoolID: pool.ID, RequestDigest: envelope.Review.RequestDigest, ExpectedRevision: envelope.Revision, Baseline: envelope.Consumed, Maximum: capacity, Unit: unit, Money: money}
	prepared := &ClusterEnvelopeReview{Automatic: true, Pools: []ClusterPoolEnvelopeReview{review}}
	if in.EnvelopeReview != nil && maintenanceHash(in.EnvelopeReview) != maintenanceHash(prepared) {
		return zero, 0, nil, ErrConflict
	}
	in.EnvelopeReview = prepared
	return capacity, cost, unknown, nil
}

// ObserveClusterScaling uses only the principal saved by this policy's review.
// A breach pauses future demand admission while observations remain available.
func (s *Service) ObserveClusterScaling(ctx context.Context, org, cloud, resource, pool string) (*ClusterCapacitySnapshot, error) {
	r, err := s.Clusters.GetManagedResource(ctx, org, resource)
	if err != nil {
		return nil, err
	}
	policy, ok := r.ScalingPolicies[pool]
	if !ok || policy.Revoked || policy.OperationID == "" || r.CloudID != cloud || r.DeletedAt != nil {
		return nil, ErrConflict
	}
	operation, err := s.Clusters.GetClusterOperation(ctx, org, cloud, policy.OperationID)
	if err != nil {
		return nil, err
	}
	if operation.Kind != "node-pool-scaling" || operation.Status != "succeeded" || operation.Input.Scaling == nil || operation.Input.Scaling.ID != policy.ID || operation.Input.Scaling.Revision != policy.Revision {
		return nil, ErrConflict
	}
	store, ok := s.Clusters.(interface {
		ValidateClusterScalingObserver(context.Context, string, string, string, string, int64) error
		SaveClusterScalingObservation(context.Context, *ManagedResource, provider.ClusterObservation) error
	})
	if !ok {
		return nil, ErrInvalid
	}
	fence := clusterMaintenanceFence(func(call context.Context) error {
		if checkErr := store.ValidateClusterScalingObserver(call, org, cloud, resource, pool, policy.Revision); checkErr != nil {
			return checkErr
		}
		if checkErr := s.Authz.Require(call, operation.Principal, security.ClusterManage, ""); checkErr != nil {
			return checkErr
		}
		_, _, checkErr := s.clusterAuthority(call, operation.Principal, cloud, operation.Input)
		return checkErr
	})
	ctx = context.WithValue(ctx, clusterMaintenanceFenceKey{}, fence)
	ctx = context.WithValue(ctx, clusterScalingObservationKey{}, ClusterScalingObservation{PolicyID: policy.ID, PoolID: pool, Revision: policy.Revision, OperationID: policy.OperationID})
	if checkErr := fence(ctx); checkErr != nil {
		return nil, checkErr
	}
	reader, access, err := s.clusterAuthority(ctx, operation.Principal, cloud, operation.Input)
	if err != nil {
		return nil, err
	}
	observation, err := reader.GetCluster(ctx, access, operation.Input.Ref)
	if err != nil {
		return nil, err
	}
	if !freshClusterEnvelopeEvidence(observation.ObservedAt, s.now()) || observation.Ref != operation.Input.Ref {
		return nil, ErrConflict
	}
	if checkErr := fence(ctx); checkErr != nil {
		return nil, checkErr
	}
	if err := store.SaveClusterScalingObservation(ctx, r, observation); err != nil {
		return nil, err
	}
	return s.ObserveClusterCapacity(ctx, operation.Principal, cloud, resource)
}

// CurrentClusterScalingPolicy is the admission seam for durable demand cohorts.
// Call it again before effects; a returned review is not a reusable credential.
func (s *Service) CurrentClusterScalingPolicy(ctx context.Context, org, cloud, resource, pool string) (*ClusterScalingPolicy, error) {
	if err := s.clusterScalingEnabled(); err != nil {
		return nil, err
	}
	r, err := s.Clusters.GetManagedResource(ctx, org, resource)
	if err != nil {
		return nil, err
	}
	policy, ok := r.ScalingPolicies[pool]
	if !ok || policy.Revoked || policy.Mode != "native" || policy.PauseReason != "" || r.CloudID != cloud || r.DeletedAt != nil || r.Observation == nil || !freshClusterEnvelopeEvidence(r.Observation.ObservedAt, s.now()) {
		return nil, ErrConflict
	}
	if reason := scalingBreach(policy, *r.Observation); reason != "" {
		return nil, &ClusterMaintenanceAdmissionError{Reason: reason}
	}
	operation, err := s.Clusters.GetClusterOperation(ctx, org, cloud, policy.OperationID)
	if err != nil {
		return nil, err
	}
	if operation.Status != "succeeded" || operation.Kind != "node-pool-scaling" || operation.Input.Scaling == nil || operation.Input.Scaling.ID != policy.ID || operation.Input.Scaling.Revision != policy.Revision {
		return nil, ErrConflict
	}
	if _, _, checkErr := s.clusterAuthority(ctx, operation.Principal, cloud, operation.Input); checkErr != nil {
		return nil, checkErr
	}
	budgets, ok := s.Store.(CapacityPolicyStore)
	if !ok {
		return nil, ErrInvalid
	}
	budget, err := budgets.GetCapacityPolicy(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	if budget.Revision != operation.Input.PolicyRevision {
		return nil, ErrConflict
	}
	store, ok := s.Clusters.(interface {
		ValidateClusterScalingObserver(context.Context, string, string, string, string, int64) error
	})
	if !ok {
		return nil, ErrInvalid
	}
	if err := store.ValidateClusterScalingObserver(ctx, org, cloud, resource, pool, policy.Revision); err != nil {
		return nil, err
	}
	return &policy, nil
}

// ClusterScalingObservation identifies the standing review behind credential issuance.
type ClusterScalingObservation struct {
	PolicyID    string `json:"policy_id"`
	PoolID      string `json:"pool_id"`
	Revision    int64  `json:"revision"`
	OperationID string `json:"operation_id"`
}
type clusterScalingObservationKey struct{}

// ClusterScalingObserverScope contains no inherited creator authority.
type ClusterScalingObserverScope struct{ OrgID, CloudID, ResourceID, PoolID string }

func (s *Service) recoverClusterScaling(ctx context.Context) error {
	if !s.ManagedClusters.Enabled || s.Clusters == nil {
		return nil
	}
	store, ok := s.Clusters.(interface {
		ListClusterScalingObservers(context.Context) ([]ClusterScalingObserverScope, error)
	})
	if !ok {
		return nil
	}
	scopes, err := store.ListClusterScalingObservers(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, scope := range scopes {
		_, err := s.ObserveClusterScaling(ctx, scope.OrgID, scope.CloudID, scope.ResourceID, scope.PoolID)
		result = errors.Join(result, err)
	}
	return result
}
