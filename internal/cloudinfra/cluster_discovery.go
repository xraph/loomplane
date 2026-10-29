package cloudinfra

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// clusterAccount resolves credentials only after checking the caller's current grant.
func (s *Service) clusterAccount(ctx context.Context, p security.Principal, connectionID string, capability provider.InfrastructureCapability) (provider.ClusterReader, provider.AccountAccess, error) {
	var access provider.AccountAccess
	if p.OrgID == "" || p.UserID == "" {
		return nil, access, security.ErrUnauthenticated
	}
	if err := s.Authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, access, err
	}
	conn, raw, err := s.Connections.Credential(ctx, p.OrgID, connectionID)
	if err != nil {
		return nil, access, err
	}
	if conn == nil || conn.OrgID != p.OrgID || conn.ID != connectionID || conn.Status != connections.StatusReady || conn.Generation < 1 {
		return nil, access, provider.ErrInfrastructureIdentity
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return nil, access, err
	}
	adapter, err := s.Adapters.Cluster(conn.Cloud, capability)
	if err != nil {
		return nil, access, err
	}
	access = provider.AccountAccess{Provider: conn.Cloud, AccountID: credential.AccountID, ConnectionID: conn.ID, Generation: conn.Generation, Credential: raw}
	return adapter, access, nil
}

func (s *Service) DiscoverClusters(ctx context.Context, p security.Principal, connectionID, cursor string) (provider.ClusterPage, error) {
	adapter, access, err := s.clusterAccount(ctx, p, connectionID, provider.ClusterDiscovery)
	if err != nil {
		return provider.ClusterPage{}, err
	}
	page, err := adapter.ListClusters(ctx, access, cursor)
	if err != nil {
		return provider.ClusterPage{}, err
	}
	for _, item := range page.Items {
		if item.Ref.AccountID != access.AccountID {
			return provider.ClusterPage{}, provider.ErrInfrastructureIdentity
		}
	}
	if page.NextCursor != "" && page.NextCursor == cursor {
		return provider.ClusterPage{}, provider.ErrInfrastructurePagination
	}
	return page, nil
}

// ClusterOptions always refreshes live options after current authorization.
func (s *Service) ClusterOptions(ctx context.Context, p security.Principal, cloudID, connectionID, region string) (provider.ClusterOptions, error) {
	if _, err := s.authorize(ctx, p, cloudID); err != nil {
		return provider.ClusterOptions{}, err
	}
	adapter, access, err := s.clusterAccount(ctx, p, connectionID, provider.ClusterOptionsRead)
	if err != nil {
		return provider.ClusterOptions{}, err
	}
	options, err := adapter.Options(ctx, access, region)
	if err != nil {
		return provider.ClusterOptions{}, err
	}
	age := s.now().Sub(options.CheckedAt)
	if options.Digest == "" || options.CheckedAt.IsZero() || age < 0 || age >= 5*time.Minute {
		return provider.ClusterOptions{}, provider.ErrInfrastructureInvalid
	}
	return options, nil
}
