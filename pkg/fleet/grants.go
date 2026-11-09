package fleet

import "time"

// GrantLimits bound the total unreleased reservations made under one grant.
// Money is in millionths of Currency per hour; an unknown price is not zero.
type GrantLimits struct {
	MaxInstances     int    `json:"max_instances"`
	CPUMillis        int64  `json:"cpu_millis"`
	MemoryMiB        int64  `json:"memory_mib"`
	StorageGiB       int64  `json:"storage_gib"`
	HourlyCostMicros int64  `json:"hourly_cost_micros"`
	Currency         string `json:"currency"`
}

// CreateGrantInput is reviewed by a human owner before service requests may use it.
type CreateGrantInput struct {
	CredentialID string      `json:"credential_id"`
	StageID      string      `json:"stage_id"`
	ReleaseIDs   []string    `json:"release_ids"`
	ProfileIDs   []string    `json:"profile_ids"`
	CloudIDs     []string    `json:"cloud_ids"`
	Regions      []string    `json:"regions"`
	Actions      []string    `json:"actions"`
	Limits       GrantLimits `json:"limits"`
	ExpiresAt    time.Time   `json:"expires_at"`
}
type Grant struct {
	ID        string           `json:"id" forge:"id"`
	FleetID   string           `json:"fleet_id"`
	Revision  int64            `json:"revision"`
	Status    string           `json:"status"`
	Input     CreateGrantInput `json:"input"`
	CreatedBy string           `json:"created_by"`
	CreatedAt time.Time        `json:"created_at"`
}
