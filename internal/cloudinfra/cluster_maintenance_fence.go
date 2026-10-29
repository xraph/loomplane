package cloudinfra

import (
	"context"
)

type clusterMaintenanceFenceKey struct{}
type clusterMaintenanceFence func(context.Context) error

// withClusterMaintenanceFence carries only the invoking operation's lease guard.
// It cannot recurse into provider preflight or mint management credentials.
func (s *Service) withClusterMaintenanceFence(ctx context.Context, o *ClusterOperation) context.Context {
	fence := clusterMaintenanceFence(func(effectCtx context.Context) error {
		if err := s.clusterEnabled(); err != nil {
			return err
		}
		saved, err := s.Clusters.GetClusterOperation(effectCtx, o.OrgID, o.CloudID, o.ID)
		if err != nil {
			return err
		}
		if saved.ID != o.ID || saved.Digest != o.Digest || saved.Revision != o.Revision || saved.CancelRequested || saved.FencingEpoch != o.FencingEpoch || saved.LeaseOwner != o.LeaseOwner || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(s.now()) {
			return ErrConflict
		}
		return s.saveCluster(effectCtx, o)
	})
	return context.WithValue(ctx, clusterMaintenanceFenceKey{}, fence)
}
func checkClusterMaintenanceFence(ctx context.Context) error {
	if fence, ok := ctx.Value(clusterMaintenanceFenceKey{}).(clusterMaintenanceFence); ok {
		return fence(ctx)
	}
	return nil
}
