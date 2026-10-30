package digitalocean

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// CheckClusterLifecycle uses the cluster-specific upgrade path, not the create catalog.
func (c *Client) CheckClusterLifecycle(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, kind, version string) error {
	observation, err := c.GetCluster(ctx, a, ref)
	if err != nil {
		return err
	}
	if observation.State != "running" {
		return &provider.ClusterLifecycleBlockedError{Reason: "provider_not_ready"}
	}
	if kind == "cluster-upgrade" {
		if version == "" || version == observation.Version {
			return &provider.ClusterLifecycleBlockedError{Reason: "unsupported_upgrade_path"}
		}
		var response struct {
			Upgrades []struct {
				Slug string `json:"slug"`
			} `json:"available_upgrade_versions"`
		}
		if err := c.do(ctx, string(a.Credential), http.MethodGet, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID)+"/upgrades", nil, nil, &response); err != nil {
			return infrastructureError(err, "kubernetes:read")
		}
		supported := false
		for _, v := range response.Upgrades {
			supported = supported || v.Slug == version
		}
		if !supported {
			return &provider.ClusterLifecycleBlockedError{Reason: "unsupported_upgrade_path"}
		}
	}
	if kind == "cluster-upgrade" || kind == "node-pool-delete" || kind == "node-pool-decrease" {
		// DOKS has no no-force mode or conditional node-set mutation. Cordon and a
		// snapshot cannot prevent native timeout deletion or newly scheduled pods.
		return &provider.ClusterLifecycleBlockedError{Reason: "provider_force_drain_unavoidable"}
	}
	return nil
}

func (c *Client) UpgradeCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, version string) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "cluster-upgrade", Ref: ref}
	return receipt, c.CheckClusterLifecycle(ctx, a, ref, "cluster-upgrade", version)
}

func (c *Client) UpdateCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, change provider.ClusterUpdate) (provider.ClusterMutation, error) {
	receipt := provider.ClusterMutation{Kind: "cluster-configure", Ref: ref}
	if len(change.FieldMask) == 0 || change.ProviderOptions != nil {
		return receipt, provider.ErrInfrastructureInvalid
	}
	seen := map[string]bool{}
	for _, field := range change.FieldMask {
		if seen[field] || !slices.Contains([]string{"maintenance"}, field) {
			return receipt, provider.ErrInfrastructureUnsupported
		}
		seen[field] = true
	}
	if change.HA || !seen["maintenance"] && change.Maintenance != (provider.ClusterMaintenance{}) {
		return receipt, provider.ErrInfrastructureInvalid
	}
	if seen["maintenance"] {
		if !slices.Contains([]string{"any", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}, change.Maintenance.Day) {
			return receipt, provider.ErrInfrastructureInvalid
		}
		if _, err := time.Parse("15:04", change.Maintenance.StartTime); err != nil {
			return receipt, provider.ErrInfrastructureInvalid
		}
	}
	observation, err := c.GetCluster(ctx, a, ref)
	if err != nil {
		return receipt, err
	}
	request := map[string]any{"name": observation.Name, "maintenance_policy": change.Maintenance}
	if a.BeforeEffect == nil {
		return receipt, provider.ErrInfrastructureInvalid
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return receipt, err
	}
	var response struct {
		Cluster doksCluster `json:"kubernetes_cluster"`
	}
	if err := c.do(ctx, string(a.Credential), http.MethodPut, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID), nil, request, &response); err != nil {
		return receipt, infrastructureError(err, "kubernetes:update")
	}
	if response.Cluster.ID != ref.ID || response.Cluster.CreatedAt != ref.Incarnation {
		return receipt, provider.ErrInfrastructureIdentity
	}
	return receipt, nil
}
