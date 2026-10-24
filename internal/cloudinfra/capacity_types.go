package cloudinfra

import "time"

// ResourceVector uses CPU milliseconds and bytes. Zero is a measured value.
type ResourceVector struct {
	Servers      int64 `json:"servers"`
	CPUMillis    int64 `json:"cpu_millis"`
	MemoryBytes  int64 `json:"memory_bytes"`
	StorageBytes int64 `json:"storage_bytes"`
}

type CapacityPolicy struct {
	OrgID                    string                `json:"org_id"`
	CloudID                  string                `json:"cloud_id"`
	Revision                 int64                 `json:"revision"`
	Enabled                  bool                  `json:"enabled"`
	RequireExpansionApproval bool                  `json:"require_expansion_approval"`
	Limits                   ResourceVector        `json:"limits"`
	Pools                    []PoolExpansionPolicy `json:"pools"`
	FrontendLimits           []FrontendLimit       `json:"frontend_limits"`
}

type PoolExpansionPolicy struct {
	// BootstrapSize is the server-validated homogeneous minimum allocation.
	BootstrapSize string          `json:"bootstrap_size"`
	PoolID        string          `json:"pool_id"`
	ConnectionID  string          `json:"connection_id"`
	Region        string          `json:"region"`
	Architecture  string          `json:"architecture"`
	Image         string          `json:"image"`
	AllowedSizes  []string        `json:"allowed_sizes"`
	MinServers    int64           `json:"min_servers"`
	MaxServers    int64           `json:"max_servers"`
	Limits        *ResourceVector `json:"limits"`
	SystemReserve ResourceVector  `json:"system_reserve"`
}

// Maximum nil permits unlimited frontends, while zero blocks new frontends.
type FrontendLimit struct {
	TargetID string `json:"target_id"`
	Maximum  *int64 `json:"maximum"`
}

type CapacityPolicyInput struct {
	ExpectedRevision int64          `json:"expected_revision"`
	Policy           CapacityPolicy `json:"policy"`
}

type CapacityDemand struct {
	OrgID          string         `json:"org_id"`
	CloudID        string         `json:"cloud_id"`
	PoolID         string         `json:"pool_id"`
	TargetID       string         `json:"target_id"`
	ProjectID      string         `json:"project_id"`
	EnvironmentID  string         `json:"environment_id"`
	PlanID         string         `json:"plan_id"`
	Service        string         `json:"service"`
	Architecture   string         `json:"architecture"`
	StickyServerID string         `json:"sticky_server_id"`
	Resources      ResourceVector `json:"resources"`
	LocalData      bool           `json:"local_data"`
	PolicyRevision int64          `json:"policy_revision"`
	IntentHash     string         `json:"intent_hash"`
}

type CapacityBlock struct {
	Reason         string `json:"reason"`
	Resource       string `json:"resource"`
	CloudID        string `json:"cloud_id"`
	PoolID         string `json:"pool_id"`
	TargetID       string `json:"target_id"`
	Limit          int64  `json:"limit"`
	Counted        int64  `json:"counted"`
	Requested      int64  `json:"requested"`
	PolicyRevision int64  `json:"policy_revision"`
	Retryable      bool   `json:"retryable"`
}

type CapacityDecision struct {
	Placement   *Placement     `json:"placement,omitempty"`
	ExpansionID string         `json:"expansion_id,omitempty"`
	Block       *CapacityBlock `json:"block,omitempty"`
}

type Expansion struct {
	ID                   string         `json:"id"`
	OrgID                string         `json:"org_id"`
	CloudID              string         `json:"cloud_id"`
	PoolID               string         `json:"pool_id"`
	OperationID          string         `json:"operation_id"`
	Status               string         `json:"status"`
	IntentHash           string         `json:"intent_hash"`
	PolicyRevision       int64          `json:"policy_revision"`
	Resources            ResourceVector `json:"resources"`
	Provider             string         `json:"provider"`
	AccountID            string         `json:"account_id"`
	ConnectionID         string         `json:"connection_id"`
	ConnectionGeneration int64          `json:"connection_generation"`
	Region               string         `json:"region"`
	Size                 string         `json:"size"`
	Architecture         string         `json:"architecture"`
	Image                string         `json:"image"`
	ApprovedBy           string         `json:"approved_by"`
	ApprovedAt           *time.Time     `json:"approved_at,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

type CapacityUsage struct {
	Provisioned          ResourceVector `json:"provisioned"`
	Pending              ResourceVector `json:"pending"`
	Reserved             ResourceVector `json:"reserved"`
	Observed             ResourceVector `json:"observed"`
	SystemReserve        ResourceVector `json:"system_reserve"`
	Allocatable          ResourceVector `json:"allocatable"`
	Headroom             ResourceVector `json:"headroom"`
	ObservedStorageBytes *int64         `json:"observed_storage_bytes"`
	CheckedAt            time.Time      `json:"checked_at"`
	Fresh                bool           `json:"fresh"`
}
