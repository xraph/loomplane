package cloudinfra

import (
	"context"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// clusterObservationOnly permits inspection of a durable attempted effect, never
// a new effect. The original principal remains immutable and cannot authorize
// enrollment or another mutation after revocation.
func clusterObservationOnly(o *ClusterOperation) bool {
	return (o.Kind == "retire-cluster" || maintenanceKind(o.Kind) && o.Input.Maintenance != nil && o.Input.Ref.ID != "" || o.Kind == "create" && o.ID != "" && o.Input.Spec.OperationID == o.ID) && len(o.Steps) == 2 && o.Steps[0].Name == "provider" && o.Steps[0].State != "pending" && o.Steps[0].AttemptedAt != nil && o.Steps[0].RequestDigest == o.Digest
}

// clusterRecoveryAccount reads only the pinned request's account. It cannot
// replace approval authority or grant access to another account. A same-account
// credential renewal can restore observation only within the pinned generation.
func (s *Service) clusterRecoveryAccount(ctx context.Context, o *ClusterOperation) (provider.ClusterReader, provider.AccountAccess, error) {
	var access provider.AccountAccess
	if !clusterObservationOnly(o) {
		return nil, access, ErrInvalid
	}
	conn, raw, err := s.Connections.Credential(ctx, o.OrgID, o.Input.ConnectionID)
	if err != nil {
		return nil, access, err
	}
	if conn == nil || conn.OrgID != o.OrgID || conn.ID != o.Input.ConnectionID || conn.Cloud != o.Input.Provider || conn.Status != connections.StatusReady || conn.Generation < 1 || conn.Generation != o.Input.ConnectionGeneration {
		return nil, access, provider.ErrInfrastructureIdentity
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return nil, access, err
	}
	if credential.AccountID != o.Input.AccountID {
		return nil, access, provider.ErrInfrastructureIdentity
	}
	reader, err := s.Adapters.Cluster(o.Input.Provider, provider.ClusterMutationObservation)
	if err != nil {
		return nil, access, err
	}
	access = provider.AccountAccess{Provider: conn.Cloud, AccountID: credential.AccountID, ConnectionID: conn.ID, Generation: conn.Generation, Credential: raw}
	return reader, access, nil
}
