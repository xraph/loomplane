package digitalocean

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.ClusterRetirer = (*Client)(nil)

func (c *Client) AssociatedResources(ctx context.Context, access provider.AccountAccess, ref provider.ClusterRef) (provider.AssociatedResourceInventory, error) {
	out := provider.AssociatedResourceInventory{Ref: ref, Resources: []provider.AssociatedResource{}}
	if ref.Incarnation == "" {
		return out, provider.ErrInfrastructureIdentity
	}
	if _, err := c.GetCluster(ctx, access, ref); err != nil {
		return out, err
	}
	var response struct {
		Volumes []struct {
			ID string `json:"id"`
		} `json:"volumes"`
		Snapshots []struct {
			ID string `json:"id"`
		} `json:"volume_snapshots"`
		Balancers []struct {
			ID string `json:"id"`
		} `json:"load_balancers"`
	}
	if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID)+"/destroy_with_associated_resources", nil, nil, &response); err != nil {
		return out, infrastructureError(err, "kubernetes:read")
	}
	out.Complete = response.Volumes != nil && response.Snapshots != nil && response.Balancers != nil
	seen := map[string]bool{}
	for kind, items := range map[string][]struct {
		ID string `json:"id"`
	}{"volume": response.Volumes, "volume_snapshot": response.Snapshots, "load_balancer": response.Balancers} {
		for _, item := range items {
			key := kind + ":" + item.ID
			if item.ID == "" || seen[key] {
				out.Complete = false
				continue
			}
			seen[key] = true
			out.Resources = append(out.Resources, provider.AssociatedResource{ID: item.ID, Kind: kind, Ownership: "unknown", SurvivesClusterDeletion: true})
		}
	}
	sort.Slice(out.Resources, func(i, j int) bool {
		return out.Resources[i].Kind+out.Resources[i].ID < out.Resources[j].Kind+out.Resources[j].ID
	})
	out.ObservedAt = time.Now().UTC()
	return out, nil
}

// DeleteCluster stays blocked until provider child ownership and conditional deletion are qualified.
func (c *Client) DeleteCluster(ctx context.Context, access provider.AccountAccess, ref provider.ClusterRef, expectedIncarnation string) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "retire-cluster", Ref: ref}
	if expectedIncarnation == "" || expectedIncarnation != ref.Incarnation {
		return receipt, provider.ErrInfrastructureIdentity
	}
	if _, err := c.GetCluster(ctx, access, ref); err != nil {
		return receipt, err
	}
	return receipt, &provider.ClusterLifecycleBlockedError{Reason: "child_ownership_unproven"}
}
