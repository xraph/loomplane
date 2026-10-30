package digitalocean

import (
	"context"
	"net/http"
	"net/url"

	"github.com/xraph/loomplane/pkg/provider"
)

func (c *Client) UpdateNodePoolScaling(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, poolID string, bounds provider.NodePoolScaling) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "node-pool-scaling", Ref: ref, PoolID: poolID}
	if poolID == "" || bounds.Min < 0 || bounds.Max < bounds.Min || bounds.Native && bounds.Max < 1 || !bounds.Native && (bounds.Min != 0 || bounds.Max != 0) || a.BeforeEffect == nil {
		return receipt, provider.ErrInfrastructureInvalid
	}
	observation, err := c.GetCluster(ctx, a, ref)
	if err != nil {
		return receipt, err
	}
	if observation.State != "running" {
		return receipt, &provider.ClusterLifecycleBlockedError{Reason: "provider_not_ready"}
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
	request := map[string]any{"name": current.Name, "auto_scale": bounds.Native}
	if bounds.Native {
		request["min_nodes"] = bounds.Min
		request["max_nodes"] = bounds.Max
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return receipt, err
	}
	var response struct {
		Pool struct {
			ID string `json:"id"`
		} `json:"node_pool"`
	}
	if err := c.do(ctx, string(a.Credential), http.MethodPut, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID)+"/node_pools/"+url.PathEscape(poolID), nil, request, &response); err != nil {
		return receipt, infrastructureError(err, "kubernetes:update")
	}
	if response.Pool.ID != poolID {
		return receipt, provider.ErrInfrastructureIdentity
	}
	return receipt, nil
}
