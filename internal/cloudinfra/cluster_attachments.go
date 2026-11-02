package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// RuntimeAttachmentSummary is the current selection for a separate access or detach review.
type RuntimeAttachmentSummary struct {
	ID                 string                         `json:"id"`
	Revision           int64                          `json:"revision"`
	Namespace          string                         `json:"namespace"`
	ProjectID          string                         `json:"project_id"`
	TargetID           string                         `json:"target_id,omitempty"`
	CapabilityRevision int64                          `json:"capability_revision"`
	Profile            *provider.RuntimeAccessProfile `json:"profile,omitempty"`
	State              string                         `json:"state"`
}
type RuntimeAttachmentPage struct {
	ClusterID string                     `json:"cluster_id"`
	Items     []RuntimeAttachmentSummary `json:"items"`
}
type runtimeAttachmentReader interface {
	ListClusterAttachments(context.Context, string, string, string) ([]RuntimeAttachmentSummary, error)
}

func (s *Service) ListClusterAttachments(ctx context.Context, p security.Principal, cloud, cluster string) (RuntimeAttachmentPage, error) {
	out := RuntimeAttachmentPage{ClusterID: cluster, Items: []RuntimeAttachmentSummary{}}
	r, err := s.GetCluster(ctx, p, cloud, cluster)
	if err != nil {
		return out, err
	}
	if r.DeletedAt != nil {
		return out, ErrNotFound
	}
	reader, ok := s.Clusters.(runtimeAttachmentReader)
	if !ok {
		return out, ErrInvalid
	}
	out.Items, err = reader.ListClusterAttachments(ctx, p.OrgID, cloud, cluster)
	return out, err
}

func (s *PgStore) ListClusterAttachments(ctx context.Context, org, cloud, cluster string) ([]RuntimeAttachmentSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT a.id,a.revision,a.body->>'namespace',a.body->>'project_id',
 coalesce((SELECT t.id FROM loomplane_cloud_targets t WHERE t.org_id=a.org_id AND t.cloud_id=a.cloud_id AND t.project_id=a.body->>'project_id' AND t.connection_id=a.body->>'runtime_connection_id' ORDER BY t.id LIMIT 1),''),
 coalesce(v.revision,0),coalesce(v.body->'input'->'profile',o.body->'input'->'attachment'->'runtime_access','null'::jsonb)
 FROM loomplane_runtime_attachments a
 LEFT JOIN LATERAL (SELECT revision,body FROM loomplane_runtime_access WHERE org_id=a.org_id AND attachment_id=a.id ORDER BY revision DESC LIMIT 1) v ON true
 LEFT JOIN loomplane_cluster_operations o ON o.org_id=a.org_id AND o.id=a.body->>'operation_id'
 WHERE a.org_id=$1 AND a.cloud_id=$2 AND a.cluster_id=$3 AND a.deleted_at IS NULL ORDER BY a.id`, org, cloud, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuntimeAttachmentSummary{}
	for rows.Next() {
		var item RuntimeAttachmentSummary
		var profile []byte
		if err := rows.Scan(&item.ID, &item.Revision, &item.Namespace, &item.ProjectID, &item.TargetID, &item.CapabilityRevision, &profile); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(profile, &item.Profile); err != nil {
			return nil, err
		}
		item.State = "active"
		out = append(out, item)
	}
	return out, rows.Err()
}
