package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

var ErrClusterCapacityUnavailable = errors.New("cluster capacity observation is unavailable")
var ErrClusterAggregateUnsupported = errors.New("cluster capacity data format is unsupported; reconcile stored data before retrying")

// GetClusterCapacity reads persisted inventory without issuing provider credentials.
func (s *Service) GetClusterCapacity(ctx context.Context, p security.Principal, cloud, resource string) (*ClusterCapacitySnapshot, error) {
	r, err := s.GetCluster(ctx, p, cloud, resource)
	if err != nil {
		return nil, err
	}
	reader, ok := s.Clusters.(interface {
		ReadClusterCapacity(context.Context, string, string, string) (*ClusterCapacitySnapshot, error)
	})
	if !ok {
		return nil, ErrClusterCapacityUnavailable
	}
	v, err := reader.ReadClusterCapacity(ctx, p.OrgID, cloud, resource)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrClusterCapacityUnavailable
	}
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrClusterCapacityUnavailable
	}
	if v.ResourceFingerprint != retirementResourceFingerprint(r) || clusterCapacityIdentityInvalid(r) {
		v.Complete = false
		v.Conditions = append(v.Conditions, provider.ClusterCondition{Type: "capacity", Status: "unknown", Reason: "resource_trust_changed"})
	}
	return v, nil
}
func (s *PgStore) ReadClusterCapacity(ctx context.Context, org, cloud, resource string) (*ClusterCapacitySnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if lockErr := lockCapacityCloud(ctx, tx, org, cloud); lockErr != nil {
		return nil, lockErr
	}
	var raw []byte
	var version int
	if err = tx.QueryRow(ctx, `SELECT schema_version,body FROM loomplane_cluster_capacity_inventory WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3`, org, cloud, resource).Scan(&version, &raw); err != nil {
		return nil, storeError(err)
	}
	if version != 1 {
		return nil, ErrClusterAggregateUnsupported
	}
	if len(raw) == 0 {
		return nil, ErrClusterCapacityUnavailable
	}
	var out ClusterCapacitySnapshot
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, ErrClusterAggregateUnsupported
	}
	if out.ResourceID != resource || out.ObservationRevision < 1 || out.CheckedAt.IsZero() {
		return nil, ErrClusterAggregateUnsupported
	}
	return &out, nil
}
