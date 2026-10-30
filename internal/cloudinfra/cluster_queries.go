package cloudinfra

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
)

type ClusterOperationPage struct {
	Items      []*ClusterOperation
	NextCursor string
}

type clusterOperationReader interface {
	ListClusterOperations(context.Context, string, string, string, int) (ClusterOperationPage, error)
}

func (s *Service) ListClusters(ctx context.Context, p security.Principal, cloud, cursor string, limit int) (ResourcePage, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return ResourcePage{}, err
	}
	if s.Clusters == nil {
		return ResourcePage{Items: []*ManagedResource{}}, nil
	}
	page, err := s.Clusters.ListManagedResources(ctx, p.OrgID, cloud, cursor, limit)
	if err != nil {
		return ResourcePage{}, err
	}
	clusters := make([]*ManagedResource, 0, len(page.Items))
	for _, resource := range page.Items {
		if resource != nil && resource.Kind == "cluster" && resource.OrgID == p.OrgID && resource.CloudID == cloud {
			clusters = append(clusters, resource)
		}
	}
	page.Items = clusters
	return page, nil
}
func (s *Service) GetCluster(ctx context.Context, p security.Principal, cloud, id string) (*ManagedResource, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if s.Clusters == nil {
		return nil, ErrNotFound
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if r.OrgID != p.OrgID || r.CloudID != cloud || r.Kind != "cluster" {
		return nil, ErrNotFound
	}
	return r, nil
}
func (s *Service) GetClusterOperation(ctx context.Context, p security.Principal, cloud, id string) (*ClusterOperation, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if s.Clusters == nil {
		return nil, ErrNotFound
	}
	return s.Clusters.GetClusterOperation(ctx, p.OrgID, cloud, id)
}
func (s *Service) ListClusterOperations(ctx context.Context, p security.Principal, cloud, cursor string, limit int) (ClusterOperationPage, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return ClusterOperationPage{}, err
	}
	store, ok := s.Clusters.(clusterOperationReader)
	if !ok {
		return ClusterOperationPage{Items: []*ClusterOperation{}}, nil
	}
	return store.ListClusterOperations(ctx, p.OrgID, cloud, cursor, limit)
}
func (s *PgStore) ListClusterOperations(ctx context.Context, org, cloud, cursor string, limit int) (ClusterOperationPage, error) {
	limit = resourcePageLimit(limit)
	page := ClusterOperationPage{Items: []*ClusterOperation{}}
	rows, err := s.db.Query(ctx, `SELECT body,principal FROM loomplane_cluster_operations WHERE org_id=$1 AND cloud_id=$2 AND id>$3 ORDER BY id LIMIT $4`, org, cloud, cursor, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		o, checkErr := scanClusterOperation(rows)
		if checkErr != nil {
			return page, checkErr
		}
		page.Items = append(page.Items, o)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	return page, nil
}
