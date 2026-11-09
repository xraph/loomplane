package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func nativeTemplateBlock() error {
	return &CapacityAdmissionError{Block: CapacityBlock{Reason: "native_template_unqualified", Retryable: true}}
}

// ClusterWorkloadReview resolves managed identity and fresh normalized facts.
// The caller still needs an approved-demand guard before reserving anything.
func (s *PgStore) ClusterWorkloadReview(ctx context.Context, in ClusterWorkloadInput) (ClusterWorkloadInput, ClusterCapacitySnapshot, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT a.id,a.cluster_id,a.runtime_connection_id,(a.body->>'trust_revision')::bigint,i.body FROM loomplane_cloud_targets t JOIN loomplane_runtime_attachments a ON a.org_id=t.org_id AND a.cloud_id=t.cloud_id AND a.cluster_id=t.managed_resource_id AND a.project_id=t.project_id AND a.runtime_connection_id=t.connection_id AND a.deleted_at IS NULL JOIN loomplane_cluster_capacity_inventory i ON i.org_id=a.org_id AND i.resource_id=a.cluster_id AND i.saved_revision=i.issued_revision WHERE loomplane_cluster_controls_current($1,a.cluster_id) AND t.org_id=$1 AND t.cloud_id=$2 AND t.id=$3 AND t.project_id=$4`, in.OrgID, in.CloudID, in.TargetID, in.ProjectID).Scan(&in.AttachmentID, &in.ResourceID, &in.ConnectionID, &in.TrustRevision, &raw)
	var snapshot ClusterCapacitySnapshot
	if err != nil {
		return in, snapshot, storeError(err)
	}
	if checkErr := json.Unmarshal(raw, &snapshot); checkErr != nil {
		return in, snapshot, checkErr
	}
	if !snapshot.Fresh(time.Now()) || !snapshot.WorkloadsComplete {
		return in, snapshot, nativeTemplateBlock()
	}
	return in, snapshot, nil
}

func (s *PgStore) nativeWorkloadSupply(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, snapshot ClusterCapacitySnapshot, supply ClusterWorkloadVector) (ClusterWorkloadVector, error) {
	n := in.Native
	r, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, in.OrgID, in.CloudID, in.ResourceID))
	if err != nil {
		return supply, err
	}
	policy, ok := r.ScalingPolicies[n.PoolID]
	if !ok || policy.ID != n.PolicyID || policy.Revision != n.PolicyRevision || policy.Mode != "native" || policy.Revoked || policy.PauseReason != "" || policy.MaxNodes != n.MaximumNodes || r.Observation == nil || !freshClusterEnvelopeEvidence(r.Observation.ObservedAt, time.Now()) || scalingBreach(policy, *r.Observation) != "" {
		return supply, nativeTemplateBlock()
	}
	if checkErr := scalingObserverTx(ctx, tx, in.OrgID, in.CloudID, in.ResourceID, n.PoolID, n.PolicyRevision); checkErr != nil {
		return supply, checkErr
	}
	var profile *ClusterSchedulingProfile
	for i := range snapshot.SchedulingProfiles {
		p := &snapshot.SchedulingProfiles[i]
		if p.PoolID == n.PoolID && p.Digest == n.ProfileDigest {
			profile = p
		}
	}
	if profile == nil {
		return supply, nativeTemplateBlock()
	}
	matched := false
	for _, pool := range r.Observation.Pools {
		if pool.ID == n.PoolID && profile.TemplateDigest == clusterPoolTemplateDigest(*r.Observation, pool) {
			matched = true
		}
	}
	if !matched {
		return supply, nativeTemplateBlock()
	}
	envelopes, err := clusterEnvelopesTx(ctx, tx, in.OrgID)
	if err != nil {
		return supply, err
	}
	covered := false
	for _, e := range envelopes {
		if e.CloudID == in.CloudID && e.ResourceID == in.ResourceID && e.PoolID == n.PoolID && e.Provider == r.Provider && e.AccountID == r.AccountID && e.State == "active" && e.Ref == r.Observation.Ref && e.Maximum.Servers >= int64(n.MaximumNodes) && e.DesiredMaximum.Servers >= int64(n.MaximumNodes) && freshClusterEnvelopeEvidence(e.CheckedAt, time.Now()) {
			covered = true
		}
	}
	if !covered {
		return supply, nativeTemplateBlock()
	}
	count := 0
	for _, node := range snapshot.Nodes {
		if node.PoolID == n.PoolID {
			count++
		}
	}
	extra := int64(n.MaximumNodes - count)
	if extra < 0 {
		return supply, nativeTemplateBlock()
	}
	dimensions := workloadDimensions(profile.Available)
	for i, v := range dimensions {
		if v < 0 || extra > 0 && v > math.MaxInt64/extra {
			return supply, ErrInvalid
		}
		dimensions[i] *= extra
	}
	return clusterWorkloadUsage(supply, []clusterWorkloadCoverage{{Maximum: ClusterWorkloadVector{CPUMillis: dimensions[0], MemoryBytes: dimensions[1], EphemeralStorageBytes: dimensions[2], Pods: dimensions[3]}}})
}

// ValidateClusterWorkloadProfile checks reviewed scheduler facts under admission locks.
func (s *PgStore) ValidateClusterWorkloadProfile(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, profile ClusterSchedulingProfile) error {
	if checkErr := cloudprofiles.ValidateLocationReviewTx(ctx, tx, in.OrgID, in.ProjectID, in.LocationReview, time.Now()); checkErr != nil {
		return checkErr
	}
	snapshot, err := clusterWorkloadScope(ctx, tx, in)
	if err != nil {
		return err
	}
	matched := false
	for _, p := range snapshot.SchedulingProfiles {
		if reflect.DeepEqual(p, profile) {
			matched = true
		}
	}
	if !matched {
		return nativeTemplateBlock()
	}
	for _, node := range snapshot.Nodes {
		if !node.WorkloadCompatible(profile.Architecture) {
			return &CapacityAdmissionError{Block: CapacityBlock{Reason: "workload_constraints_unsupported"}}
		}
	}
	r, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2 FOR UPDATE`, in.OrgID, in.ResourceID))
	if err != nil {
		return err
	}
	if r.Observation == nil || !freshClusterEnvelopeEvidence(r.Observation.ObservedAt, time.Now()) {
		return nativeTemplateBlock()
	}
	for _, pool := range r.Observation.Pools {
		if pool.ID == profile.PoolID && profile.TemplateDigest == clusterPoolTemplateDigest(*r.Observation, pool) {
			return nil
		}
	}
	return nativeTemplateBlock()
}

// ValidateClusterWorkloadEffect rechecks the fresh physical/profile evidence
// inside the application's issuance transaction, under the Cloud gate.
func (s *PgStore) ValidateClusterWorkloadEffect(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, profile ClusterSchedulingProfile) error {
	if err := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); err != nil {
		return err
	}
	if err := s.ValidateClusterWorkloadProfile(ctx, tx, in, profile); err != nil {
		return err
	}
	if in.Native != nil {
		snapshot, err := clusterWorkloadScope(ctx, tx, in)
		if err != nil {
			return err
		}
		_, err = s.nativeWorkloadSupply(ctx, tx, in, snapshot, ClusterWorkloadVector{})
		return err
	}
	return nil
}

// PreviewLocationWorkload reuses ledger accounting without reserving capacity.
// The immutable review must still pass normal admission before execution.
func (s *PgStore) PreviewLocationWorkload(ctx context.Context, in ClusterWorkloadInput, planned ClusterWorkloadVector) (ClusterCapacitySnapshot, bool, error) {
	checked, snapshot, err := s.ClusterWorkloadReview(ctx, in)
	if err != nil {
		return snapshot, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return snapshot, false, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			slog.Error("rollback location capacity preview", "error", rollbackErr)
		}
	}()
	if lockErr := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); lockErr != nil {
		return snapshot, false, lockErr
	}
	holds, err := clusterWorkloadHolds(ctx, tx, in.OrgID, checked.ResourceID)
	if err != nil {
		return snapshot, false, err
	}
	usage, supply, err := clusterWorkloadAdmission(ctx, tx, snapshot, holds)
	if err != nil {
		return snapshot, false, err
	}
	usage, err = clusterWorkloadUsage(usage, []clusterWorkloadCoverage{{Maximum: planned}, {Maximum: in.Maximum}})
	if err != nil {
		return snapshot, false, err
	}
	requested, available := workloadDimensions(usage), workloadDimensions(supply)
	for i := range requested {
		if requested[i] > available[i] {
			return snapshot, false, nil
		}
	}
	return snapshot, true, nil
}
