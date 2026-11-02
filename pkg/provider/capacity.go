package provider

import (
	"context"
	"time"
)

// CapacityInventoryAdapter reports a complete account snapshot or an error.
// Nil facts are unknown, and IDs are immutable within each resource kind.
type CapacityInventoryAdapter interface {
	CapacityInventory(context.Context, InfrastructureCredential) (CapacityInventory, error)
}
type CapacityInventory struct {
	Provider  string           `json:"provider"`
	AccountID string           `json:"account_id"`
	Servers   []CapacityServer `json:"servers"`
	Volumes   []CapacityVolume `json:"volumes"`
	Quota     CapacityQuota    `json:"quota"`
	CheckedAt time.Time        `json:"checked_at"`
}
type CapacityServer struct {
	ID           string `json:"id"`
	Region       string `json:"region"`
	CPUMillis    *int64 `json:"cpu_millis"`
	MemoryBytes  *int64 `json:"memory_bytes"`
	StorageBytes *int64 `json:"storage_bytes"`
}
type CapacityVolume struct {
	ID           string   `json:"id"`
	ServerIDs    []string `json:"server_ids"`
	StorageBytes *int64   `json:"storage_bytes"`
}
type CapacityQuota struct {
	Servers      *int64 `json:"servers"`
	CPUMillis    *int64 `json:"cpu_millis"`
	MemoryBytes  *int64 `json:"memory_bytes"`
	StorageBytes *int64 `json:"storage_bytes"`
}
