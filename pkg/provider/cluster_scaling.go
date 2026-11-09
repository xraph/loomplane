package provider

import "context"

// NodePoolScaling changes count ownership and bounds without writing desired count.
type NodePoolScaling struct {
	Native bool `json:"native"`
	Min    int  `json:"min"`
	Max    int  `json:"max"`
}

// NodePoolScalingManager keeps native bounds separate from manual count mutations.
type NodePoolScalingManager interface {
	UpdateNodePoolScaling(context.Context, AccountAccess, ClusterRef, string, NodePoolScaling) (ClusterMutation, error)
}
