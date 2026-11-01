package cloudinfra

import (
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

func publicPool(v *provider.NodePoolSpec) *client.NodePoolSpec {
	if v == nil {
		return nil
	}
	return &client.NodePoolSpec{ID: v.ID, Name: v.Name, Size: v.Size, Count: v.Count, Min: v.Min, Max: v.Max, Autoscale: v.Autoscale, Labels: v.Labels, Taints: v.Taints, ProviderOptions: v.ProviderOptions}
}
func publicSpec(v provider.ClusterSpec) client.ClusterSpec {
	out := client.ClusterSpec{Name: v.Name, Region: v.Region, Version: v.Version, NetworkID: v.NetworkID, HA: v.HA, Maintenance: v.Maintenance, ProviderOptions: v.ProviderOptions}
	out.NodePools = make([]client.NodePoolSpec, 0, len(v.NodePools))
	for _, pool := range v.NodePools {
		out.NodePools = append(out.NodePools, *publicPool(&pool))
	}
	return out
}
func privatePool(v *client.NodePoolSpec) *provider.NodePoolSpec {
	if v == nil {
		return nil
	}
	return &provider.NodePoolSpec{ID: v.ID, Name: v.Name, Size: v.Size, Count: v.Count, Min: v.Min, Max: v.Max, Autoscale: v.Autoscale, Labels: v.Labels, Taints: v.Taints, ProviderOptions: v.ProviderOptions}
}
func privateSpec(v client.ClusterSpec) provider.ClusterSpec {
	out := provider.ClusterSpec{Name: v.Name, Region: v.Region, Version: v.Version, NetworkID: v.NetworkID, HA: v.HA, Maintenance: v.Maintenance, ProviderOptions: v.ProviderOptions}
	if v.NodePools != nil {
		out.NodePools = make([]provider.NodePoolSpec, 0, len(v.NodePools))
	}
	for _, pool := range v.NodePools {
		out.NodePools = append(out.NodePools, *privatePool(&pool))
	}
	return out
}
func publicObservation(v *provider.ClusterObservation) *client.ClusterObservation {
	if v == nil {
		return nil
	}
	out := &client.ClusterObservation{Maintenance: v.Maintenance, HA: v.HA, Name: v.Name, Ref: v.Ref, Endpoint: v.Endpoint, CAHash: v.CAHash, Version: v.Version, State: v.State, Conditions: append([]provider.ClusterCondition{}, v.Conditions...), ObservedAt: v.ObservedAt}
	out.Pools = make([]client.NodePoolSpec, 0, len(v.Pools))
	for _, p := range v.Pools {
		out.Pools = append(out.Pools, *publicPool(&p))
	}
	return out
}
