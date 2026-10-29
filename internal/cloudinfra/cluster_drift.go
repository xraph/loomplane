package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// ObserveClusterDrift records observations without changing desired intent or granting ownership.
func (s *Service) ObserveClusterDrift(ctx context.Context, p security.Principal, cloud, resource string) (*ManagedResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := s.clusterEnabled(); err != nil {
		return nil, err
	}
	if err := s.Authz.Require(ctx, p, security.ClusterManage, ""); err != nil {
		return nil, err
	}
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, resource)
	if err != nil {
		return nil, err
	}
	if r.CloudID != cloud || r.DeletedAt != nil || r.Observation == nil {
		return nil, ErrConflict
	}
	reader, access, err := s.clusterAccount(ctx, p, r.ConnectionID, provider.ClusterDiscovery)
	if err != nil {
		return nil, err
	}
	observation, err := reader.GetCluster(ctx, access, r.Observation.Ref)
	if err != nil && !errors.Is(err, provider.ErrInfrastructureNotFound) && !errors.Is(err, provider.ErrInfrastructureIdentity) {
		return nil, err
	}
	old := r.Revision
	switch {
	case errors.Is(err, provider.ErrInfrastructureNotFound):
		r.Status = "missing"
	case errors.Is(err, provider.ErrInfrastructureIdentity):
		r.Status = "identity_changed"
	case observation.Ref != r.Observation.Ref || observation.Endpoint != r.Observation.Endpoint || observation.CAHash != "" && observation.CAHash != r.Observation.CAHash:
		r.Status = "identity_changed"
	default:
		previous, next := *r.Observation, observation
		previous.ObservedAt = time.Time{}
		next.ObservedAt = time.Time{}
		if next.CAHash == "" {
			next.CAHash = previous.CAHash
			observation.CAHash = previous.CAHash
		}
		if maintenanceHash(previous) == maintenanceHash(next) {
			return r, nil
		}
		r.Observation = &observation
		r.Status = "drifted"
	}
	r.Revision++
	r.ObservedRevision++
	r.ObservedAt = s.now().UTC()
	r.UpdatedAt = s.now().UTC()
	return s.Clusters.ClaimManagedResource(ctx, r, old)
}
