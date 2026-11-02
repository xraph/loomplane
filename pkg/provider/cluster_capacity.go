package provider

import (
	"context"
	"time"
)

// ProviderClusterCapacity contains facts read from the provider account.
//
//nolint:revive // The provider/runtime merge contract names this source explicitly.
type ProviderClusterCapacity struct {
	Provider   string                `json:"provider"`
	Ref        ClusterRef            `json:"ref"`
	CheckedAt  time.Time             `json:"checked_at"`
	Complete   bool                  `json:"complete"`
	Nodes      []ClusterCapacityNode `json:"nodes"`
	Conditions []ClusterCondition    `json:"conditions"`
}

type ClusterCapacityNode struct {
	ExternalVMID          string `json:"external_vm_id"`
	ProviderNodeID        string `json:"provider_node_id"`
	PoolID                string `json:"pool_id"`
	Zone                  string `json:"zone"`
	CPUMillis             *int64 `json:"cpu_millis"`
	MemoryBytes           *int64 `json:"memory_bytes"`
	EphemeralStorageBytes *int64 `json:"ephemeral_storage_bytes"`
}

type ClusterCapacityReader interface {
	ObserveClusterCapacity(context.Context, AccountAccess, ClusterRef) (ProviderClusterCapacity, error)
}
