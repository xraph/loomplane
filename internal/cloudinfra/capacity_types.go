package cloudinfra

import (
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// ResourceVector uses CPU milliseconds and bytes. Zero is a measured value.
// It carries at most one GPU kind: GPUs 0 means GPUKind "". The GPU fields are
// omitted when empty so stored CPU-only vectors keep their digests.
type ResourceVector struct {
	Servers      int64  `json:"servers"`
	CPUMillis    int64  `json:"cpu_millis"`
	MemoryBytes  int64  `json:"memory_bytes"`
	StorageBytes int64  `json:"storage_bytes"`
	GPUKind      string `json:"gpu_kind,omitempty"`
	GPUs         int64  `json:"gpus,omitempty"`
}

type CapacityPolicy struct {
	authority                *security.Principal
	OrgID                    string                `json:"org_id"`
	CloudID                  string                `json:"cloud_id"`
	Revision                 int64                 `json:"revision"`
	Enabled                  bool                  `json:"enabled"`
	RequireExpansionApproval bool                  `json:"require_expansion_approval"`
	Limits                   ResourceVector        `json:"limits"`
	Pools                    []PoolExpansionPolicy `json:"pools"`
	FrontendLimits           []FrontendLimit       `json:"frontend_limits"`
	GPULimits                []GPULimit            `json:"gpu_limits,omitempty"`
}

// GPULimit caps the GPUs of one kind across every server in a Cloud. A kind
// without a limit can't grow, since GPU servers cost far more than CPU ones.
type GPULimit struct {
	Kind    string `json:"kind"`
	Maximum int64  `json:"maximum"`
}

type PoolExpansionPolicy struct {
	// BootstrapSize is the server-validated homogeneous minimum allocation.
	BootstrapSize string `json:"bootstrap_size"`
	PoolID        string `json:"pool_id"`
	// AcceleratorKind "" is a CPU pool. A GPU pool only grows by sizes of this kind.
	AcceleratorKind string          `json:"accelerator_kind,omitempty"`
	ConnectionID    string          `json:"connection_id"`
	Region          string          `json:"region"`
	Architecture    string          `json:"architecture"`
	Image           string          `json:"image"`
	AllowedSizes    []string        `json:"allowed_sizes"`
	MinServers      int64           `json:"min_servers"`
	MaxServers      int64           `json:"max_servers"`
	Limits          *ResourceVector `json:"limits"`
	SystemReserve   ResourceVector  `json:"system_reserve"`
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
	Revision             int64               `json:"revision"`
	Minimum              bool                `json:"minimum"`
	Template             PoolExpansionPolicy `json:"template"`
	Block                *CapacityBlock      `json:"block,omitempty"`
	ID                   string              `json:"id"`
	OrgID                string              `json:"org_id"`
	CloudID              string              `json:"cloud_id"`
	PoolID               string              `json:"pool_id"`
	OperationID          string              `json:"operation_id"`
	Status               string              `json:"status"`
	IntentHash           string              `json:"intent_hash"`
	PolicyRevision       int64               `json:"policy_revision"`
	Resources            ResourceVector      `json:"resources"`
	Provider             string              `json:"provider"`
	AccountID            string              `json:"account_id"`
	ConnectionID         string              `json:"connection_id"`
	ConnectionGeneration int64               `json:"connection_generation"`
	Region               string              `json:"region"`
	Size                 string              `json:"size"`
	Architecture         string              `json:"architecture"`
	Image                string              `json:"image"`
	ApprovedBy           string              `json:"approved_by"`
	ApprovedAt           *time.Time          `json:"approved_at,omitempty"`
	CreatedAt            time.Time           `json:"created_at"`
	UpdatedAt            time.Time           `json:"updated_at"`
	// EstimatedHourlyUSD and Accelerator come from the selected size, so an
	// approval shows what the new server costs per hour.
	EstimatedHourlyUSD float64               `json:"estimated_hourly_usd,omitempty"`
	Accelerator        *provider.Accelerator `json:"accelerator,omitempty"`
}

// CapacityResources preserves unknown facts as null, independently per dimension.
type CapacityResources struct {
	Servers      *int64 `json:"servers"`
	CPUMillis    *int64 `json:"cpu_millis"`
	MemoryBytes  *int64 `json:"memory_bytes"`
	StorageBytes *int64 `json:"storage_bytes"`
}

type CapacityObservation struct {
	CPUMillis        *int64    `json:"cpu_millis"`
	MemoryBytes      *int64    `json:"memory_bytes"`
	StorageBytes     *int64    `json:"storage_bytes"`
	FreeStorageBytes *int64    `json:"free_storage_bytes"`
	CheckedAt        time.Time `json:"checked_at"`
}

// CapacityAccountSnapshot contains account totals, without unrelated resource
// identities or credentials. The quota and each resource fact may be unknown.
type CapacityAccountSnapshot struct {
	Provider    string                 `json:"provider"`
	AccountID   string                 `json:"account_id"`
	Provisioned CapacityResources      `json:"provisioned"`
	Quota       provider.CapacityQuota `json:"quota"`
	CheckedAt   time.Time              `json:"checked_at"`
}

type CapacityUsage struct {
	Accounts             []CapacityAccountSnapshot `json:"accounts"`
	Records              []CapacityRecord          `json:"records"`
	Provisioned          CapacityResources         `json:"provisioned"`
	Pending              CapacityResources         `json:"pending"`
	Reserved             CapacityResources         `json:"reserved"`
	Observed             CapacityResources         `json:"observed"`
	SystemReserve        CapacityResources         `json:"system_reserve"`
	Allocatable          CapacityResources         `json:"allocatable"`
	Headroom             CapacityResources         `json:"headroom"`
	ObservedStorageBytes *int64                    `json:"observed_storage_bytes"`
	CheckedAt            time.Time                 `json:"checked_at"`
	Fresh                bool                      `json:"fresh"`
	GPUs                 []GPUUsage                `json:"gpus"`
}

// GPUUsage counts one GPU kind. Provisioned and Limit are null when unknown.
type GPUUsage struct {
	Kind        string `json:"kind"`
	Provisioned *int64 `json:"provisioned"`
	Pending     int64  `json:"pending"`
	Reserved    int64  `json:"reserved"`
	Limit       *int64 `json:"limit"`
}
