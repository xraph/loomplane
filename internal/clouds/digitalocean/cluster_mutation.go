package digitalocean

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.ClusterCreator = (*Client)(nil)
var _ provider.ClusterObserver = (*Client)(nil)

func validateDOKSSpec(spec provider.ClusterSpec) error {
	if provider.ClusterRequestDigest(spec) == "" || spec.OperationID == "" || len(spec.OperationID) > 64 || strings.ContainsAny(spec.OperationID, " /\\?#%\t\r\n") || spec.Name == "" || spec.Region == "" || spec.Version == "" || spec.NetworkID == "" || len(spec.NodePools) == 0 || spec.ProviderOptions != nil {
		return provider.ErrInfrastructureInvalid
	}
	names := map[string]bool{}
	for _, p := range spec.NodePools {
		if p.Name == "" || names[p.Name] || p.Size == "" || p.Count < 1 || p.Autoscale || p.Min != 0 || p.Max != 0 || p.ProviderOptions != nil {
			return provider.ErrInfrastructureInvalid
		}
		names[p.Name] = true
	}
	return nil
}
func (c *Client) CreateCluster(ctx context.Context, access provider.AccountAccess, spec provider.ClusterSpec) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "create", OperationID: spec.OperationID, RequestDigest: provider.ClusterRequestDigest(spec), Ref: provider.ClusterRef{AccountID: access.AccountID, Region: spec.Region}}
	if err := validateDOKSSpec(spec); err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if spec.ReconcileOnly {
		observation, err := c.ObserveClusterMutation(ctx, access, receipt)
		receipt.Ref = observation.Ref
		return receipt, err
	}
	if err := c.clusterIdentity(ctx, access); err != nil {
		return receipt, err
	}
	pools := make([]map[string]any, 0, len(spec.NodePools))
	for _, p := range spec.NodePools {
		pools = append(pools, map[string]any{"name": p.Name, "size": p.Size, "count": p.Count, "auto_scale": false, "labels": p.Labels, "taints": p.Taints})
	}
	request := map[string]any{"name": spec.Name, "region": spec.Region, "version": spec.Version, "vpc_uuid": spec.NetworkID, "ha": spec.HA, "auto_upgrade": false, "node_pools": pools, "tags": []string{"loomplane-operation:" + spec.OperationID, "loomplane-request:" + receipt.RequestDigest}}
	if spec.Maintenance.Day != "" || spec.Maintenance.StartTime != "" {
		request["maintenance_policy"] = map[string]string{"day": spec.Maintenance.Day, "start_time": spec.Maintenance.StartTime}
	}
	var response struct {
		Cluster doksCluster `json:"kubernetes_cluster"`
	}
	if err := c.do(ctx, string(access.Credential), http.MethodPost, "/v2/kubernetes/clusters", nil, request, &response); err != nil {
		return receipt, infrastructureError(err, "kubernetes:create")
	}
	observation, err := response.Cluster.observation(access.AccountID)
	if err != nil {
		return receipt, err
	}
	if observation.Ref.Region != spec.Region {
		return receipt, provider.ErrInfrastructureIdentity
	}
	receipt.Ref = observation.Ref
	return receipt, nil
}

// ObserveClusterMutation requires both immutable request tags, even with a receipt.
// A missing match cannot establish that the provider rejected an attempted create.
func (c *Client) ObserveClusterMutation(ctx context.Context, access provider.AccountAccess, m provider.ClusterMutation) (provider.ClusterObservation, error) {
	var out provider.ClusterObservation
	if m.OperationID == "" || m.RequestDigest == "" || m.Ref.AccountID != access.AccountID || m.Ref.Region == "" {
		return out, provider.ErrInfrastructureIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if err := c.clusterIdentity(ctx, access); err != nil {
		return out, err
	}
	match := func(cluster doksCluster) bool {
		return slices.Contains(cluster.Tags, "loomplane-operation:"+m.OperationID) && slices.Contains(cluster.Tags, "loomplane-request:"+m.RequestDigest) && cluster.Region == m.Ref.Region
	}
	if m.Ref.ID != "" {
		if err := clusterRef(access, m.Ref); err != nil {
			return out, err
		}
		var response struct {
			Cluster doksCluster `json:"kubernetes_cluster"`
		}
		if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/clusters/"+url.PathEscape(m.Ref.ID), nil, nil, &response); err != nil {
			return out, infrastructureError(err, "kubernetes:read")
		}
		if !match(response.Cluster) || response.Cluster.ID != m.Ref.ID || (m.Ref.Incarnation != "" && response.Cluster.CreatedAt != m.Ref.Incarnation) {
			return out, provider.ErrInfrastructureIdentity
		}
		return response.Cluster.observation(access.AccountID)
	}
	state := doksPageState{}
	seen := map[string]bool{}
	matches := 0
	for page := 1; page <= infrastructureMaxPages; page++ {
		var response struct {
			Clusters []doksCluster `json:"kubernetes_clusters"`
			doksPageEvidence
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/clusters", q, nil, &response); err != nil {
			return out, infrastructureError(err, "kubernetes:read")
		}
		if response.Clusters == nil || len(response.Clusters) > pageSize {
			return out, provider.ErrInfrastructurePagination
		}
		for _, cluster := range response.Clusters {
			if cluster.ID == "" || seen[cluster.ID] {
				return out, provider.ErrInfrastructurePagination
			}
			seen[cluster.ID] = true
			if !match(cluster) {
				continue
			}
			matches++
			if matches > 1 {
				return provider.ClusterObservation{}, provider.ErrInfrastructureIdentity
			}
			var err error
			out, err = cluster.observation(access.AccountID)
			if err != nil {
				return out, err
			}
		}
		next, err := state.next(response.doksPageEvidence, page, len(response.Clusters))
		if err != nil {
			return out, err
		}
		if next == "" {
			if matches == 1 {
				return out, nil
			}
			return out, provider.ErrInfrastructureAmbiguous
		}
	}
	return out, provider.ErrInfrastructurePagination
}

func (*Client) ValidateClusterSpec(spec provider.ClusterSpec) error { return validateDOKSSpec(spec) }
