package provider

import "context"

// ClusterUpdate applies only the named fields. Omitted fields remain unchanged.
type ClusterUpdate struct {
	FieldMask       []string                `json:"field_mask"`
	Maintenance     ClusterMaintenance      `json:"maintenance"`
	HA              bool                    `json:"ha"`
	ProviderOptions *ClusterProviderOptions `json:"provider_options,omitempty"`
}

type ClusterMaintainer interface {
	UpdateCluster(context.Context, AccountAccess, ClusterRef, ClusterUpdate) (ClusterMutation, error)
	UpgradeCluster(context.Context, AccountAccess, ClusterRef, string) (ClusterMutation, error)
}

type NodePoolManager interface {
	CreateNodePool(context.Context, AccountAccess, ClusterRef, NodePoolSpec) (ClusterMutation, error)
	UpdateNodePool(context.Context, AccountAccess, ClusterRef, string, NodePoolSpec) (ClusterMutation, error)
	DeleteNodePool(context.Context, AccountAccess, ClusterRef, string) (ClusterMutation, error)
}

// ClusterLifecyclePreflight verifies exact upgrade paths and provider safety.
// It must reject destructive operations whose native drain can force deletion.
type ClusterLifecyclePreflight interface {
	CheckClusterLifecycle(context.Context, AccountAccess, ClusterRef, string, string) error
}

type ClusterLifecycleBlockedError struct {
	Reason string `json:"reason"`
}

func (e *ClusterLifecycleBlockedError) Error() string {
	return "cluster maintenance blocked: " + e.Reason
}
func (e *ClusterLifecycleBlockedError) Unwrap() error { return ErrInfrastructureUnsupported }
