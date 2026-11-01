package client

import (
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

type NodePoolSpec struct {
	ID              string                           `json:"id,omitempty"`
	Name            string                           `json:"name"`
	Size            string                           `json:"size"`
	Count           int                              `json:"count"`
	Min             int                              `json:"min"`
	Max             int                              `json:"max"`
	Autoscale       bool                             `json:"autoscale"`
	Labels          map[string]string                `json:"labels,omitempty"`
	Taints          []provider.NodePoolTaint         `json:"taints,omitempty"`
	ProviderOptions *provider.ClusterProviderOptions `json:"provider_options,omitempty"`
}
type ClusterSpec struct {
	Name            string                           `json:"name"`
	Region          string                           `json:"region"`
	Version         string                           `json:"version"`
	NetworkID       string                           `json:"network_id"`
	HA              bool                             `json:"ha"`
	NodePools       []NodePoolSpec                   `json:"node_pools"`
	Maintenance     provider.ClusterMaintenance      `json:"maintenance"`
	ProviderOptions *provider.ClusterProviderOptions `json:"provider_options,omitempty"`
}
type ClusterObservation struct {
	Maintenance provider.ClusterMaintenance `json:"maintenance,omitempty"`
	HA          bool                        `json:"ha,omitempty"`
	Name        string                      `json:"name"`
	Ref         provider.ClusterRef         `json:"ref"`
	Endpoint    string                      `json:"endpoint"`
	CAHash      string                      `json:"ca_hash"`
	Version     string                      `json:"version"`
	State       string                      `json:"state"`
	Pools       []NodePoolSpec              `json:"pools"`
	Conditions  []provider.ClusterCondition `json:"conditions"`
	ObservedAt  time.Time                   `json:"observed_at"`
}

type ClusterPage struct {
	Items      []ClusterObservation `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}
