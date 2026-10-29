package digitalocean

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

func validPool(spec provider.NodePoolSpec) bool {
	return spec.ID == "" && spec.Name != "" && spec.Size != "" && spec.Count > 0 && !spec.Autoscale && spec.Min == 0 && spec.Max == 0 && spec.ProviderOptions == nil
}
func (c *Client) CreateNodePool(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, spec provider.NodePoolSpec) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "node-pool-create", Ref: ref, OperationID: spec.OperationID, RequestDigest: spec.RequestDigest}
	if !validPool(spec) || spec.OperationID == "" || len(spec.OperationID) > 64 || strings.ContainsAny(spec.OperationID, " /\\?#%\t\r\n") || len(spec.RequestDigest) != 64 {
		return receipt, provider.ErrInfrastructureInvalid
	}
	observation, err := c.GetCluster(ctx, a, ref)
	if err != nil {
		return receipt, err
	}
	if observation.State != "running" {
		return receipt, &provider.ClusterLifecycleBlockedError{Reason: "provider_not_ready"}
	}
	for _, pool := range observation.Pools {
		if pool.OperationID == spec.OperationID || pool.Name == spec.Name {
			return receipt, provider.ErrInfrastructureDrift
		}
	}
	request := map[string]any{"name": spec.Name, "size": spec.Size, "count": spec.Count, "auto_scale": false, "labels": spec.Labels, "taints": spec.Taints, "tags": []string{"loomplane-operation:" + spec.OperationID, "loomplane-request:" + spec.RequestDigest}}
	if a.BeforeEffect == nil {
		return receipt, provider.ErrInfrastructureInvalid
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return receipt, err
	}
	var response struct {
		Pool struct {
			ID string `json:"id"`
		} `json:"node_pool"`
	}
	if err := c.do(ctx, string(a.Credential), http.MethodPost, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID)+"/node_pools", nil, request, &response); err != nil {
		return receipt, infrastructureError(err, "kubernetes:create")
	}
	if response.Pool.ID == "" {
		return receipt, provider.ErrInfrastructureInvalid
	}
	receipt.PoolID = response.Pool.ID
	return receipt, nil
}
func (c *Client) UpdateNodePool(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, poolID string, spec provider.NodePoolSpec) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "node-pool-update", Ref: ref, PoolID: poolID}
	if !validPool(spec) || poolID == "" {
		return receipt, provider.ErrInfrastructureInvalid
	}
	observation, err := c.GetCluster(ctx, a, ref)
	if err != nil {
		return receipt, err
	}
	var current *provider.NodePoolSpec
	for i := range observation.Pools {
		if observation.Pools[i].ID == poolID {
			current = &observation.Pools[i]
		}
	}
	if current == nil {
		return receipt, provider.ErrInfrastructureIdentity
	}
	if current.Autoscale {
		return receipt, &provider.ClusterLifecycleBlockedError{Reason: "native_count_owner"}
	}
	if spec.Size != current.Size || spec.Name != current.Name || !maps.Equal(spec.Labels, current.Labels) || !slices.Equal(spec.Taints, current.Taints) {
		return receipt, provider.ErrInfrastructureUnsupported
	}
	if spec.Count < current.Count {
		return receipt, &provider.ClusterLifecycleBlockedError{Reason: "provider_force_drain_unavoidable"}
	}
	if spec.Count == current.Count {
		return receipt, provider.ErrInfrastructureInvalid
	}
	if observation.State != "running" {
		return receipt, &provider.ClusterLifecycleBlockedError{Reason: "provider_not_ready"}
	}
	if a.BeforeEffect == nil {
		return receipt, provider.ErrInfrastructureInvalid
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return receipt, err
	}
	var response struct {
		Pool struct {
			ID string `json:"id"`
		} `json:"node_pool"`
	}
	request := map[string]any{"name": current.Name, "count": spec.Count, "auto_scale": false}
	if err := c.do(ctx, string(a.Credential), http.MethodPut, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID)+"/node_pools/"+url.PathEscape(poolID), nil, request, &response); err != nil {
		return receipt, infrastructureError(err, "kubernetes:update")
	}
	if response.Pool.ID != poolID {
		return receipt, provider.ErrInfrastructureIdentity
	}
	return receipt, nil
}
func (c *Client) DeleteNodePool(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, poolID string) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "node-pool-delete", Ref: ref, PoolID: poolID}
	if poolID == "" {
		return receipt, provider.ErrInfrastructureInvalid
	}
	return receipt, c.CheckClusterLifecycle(ctx, a, ref, "node-pool-delete", "")
}
