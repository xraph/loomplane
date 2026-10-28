package client

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// ResourceVector uses CPU milliseconds and bytes. It carries at most one GPU
// kind, and GPUs 0 means no GPU.
type ResourceVector struct {
	Servers      int64  `json:"servers"`
	CPUMillis    int64  `json:"cpu_millis"`
	MemoryBytes  int64  `json:"memory_bytes"`
	StorageBytes int64  `json:"storage_bytes"`
	GPUKind      string `json:"gpu_kind,omitempty"`
	GPUs         int64  `json:"gpus,omitempty"`
}

// CapacityPolicy mirrors a Cloud's capacity policy. Revision 0 is a Cloud
// that was never configured.
type CapacityPolicy struct {
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

// GPULimit caps the GPUs of one kind across a Cloud. A kind without one can't grow.
type GPULimit struct {
	Kind    string `json:"kind"`
	Maximum int64  `json:"maximum"`
}

// PoolExpansionPolicy is how one pool grows. AcceleratorKind "" is a CPU pool.
type PoolExpansionPolicy struct {
	BootstrapSize   string          `json:"bootstrap_size"`
	PoolID          string          `json:"pool_id"`
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

// FrontendLimit caps one managed target's frontends. A nil Maximum is unlimited.
type FrontendLimit struct {
	TargetID string `json:"target_id"`
	Maximum  *int64 `json:"maximum"`
}

// CapacityPolicyInput saves a policy over the revision the editor opened.
type CapacityPolicyInput struct {
	ExpectedRevision int64          `json:"expected_revision"`
	Policy           CapacityPolicy `json:"policy"`
}

// ScalingPoolInput creates a pool with no servers and the policy that grows
// it. Policy is the whole policy to save, without the new pool. Template's
// PoolID is ignored and its AcceleratorKind must equal the pool's. A GPU
// ceiling in Template.Limits needs the four CPU ceilings beside it.
type ScalingPoolInput struct {
	Name             string              `json:"name"`
	AcceleratorKind  string              `json:"accelerator_kind,omitempty"`
	ExpectedRevision int64               `json:"expected_revision"`
	Policy           CapacityPolicy      `json:"policy"`
	Template         PoolExpansionPolicy `json:"template"`
}

// ScalingPool is the created pool and the policy saved with it.
type ScalingPool struct {
	Pool   *Pool           `json:"pool"`
	Policy *CapacityPolicy `json:"policy"`
}

// CapacityBlock says why capacity couldn't be granted.
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

// CapacityWait is why a paused deployment waits. Revision is what resuming
// or cancelling it names; a wait that moved since answers ErrConflict.
type CapacityWait struct {
	Revision  int64           `json:"revision"`
	Blocks    []CapacityBlock `json:"blocks"`
	CreatedAt time.Time       `json:"created_at"`
}

// Expansion is one server a pool is growing by. HourlyPriceKnown false means
// the price is unknown, never free.
type Expansion struct {
	Revision             int64                 `json:"revision"`
	Minimum              bool                  `json:"minimum"`
	Template             PoolExpansionPolicy   `json:"template"`
	Block                *CapacityBlock        `json:"block,omitempty"`
	ID                   string                `json:"id"`
	OrgID                string                `json:"org_id"`
	CloudID              string                `json:"cloud_id"`
	PoolID               string                `json:"pool_id"`
	OperationID          string                `json:"operation_id"`
	Status               string                `json:"status"`
	IntentHash           string                `json:"intent_hash"`
	PolicyRevision       int64                 `json:"policy_revision"`
	Resources            ResourceVector        `json:"resources"`
	Provider             string                `json:"provider"`
	AccountID            string                `json:"account_id"`
	ConnectionID         string                `json:"connection_id"`
	ConnectionGeneration int64                 `json:"connection_generation"`
	Region               string                `json:"region"`
	Size                 string                `json:"size"`
	Architecture         string                `json:"architecture"`
	Image                string                `json:"image"`
	ApprovedBy           string                `json:"approved_by"`
	ApprovedAt           *time.Time            `json:"approved_at,omitempty"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
	EstimatedHourlyUSD   float64               `json:"estimated_hourly_usd,omitempty"`
	HourlyPriceKnown     bool                  `json:"hourly_price_known,omitempty"`
	Accelerator          *provider.Accelerator `json:"accelerator,omitempty"`
}

// ExpansionList is the GET /expansions envelope.
type ExpansionList struct {
	Expansions []Expansion `json:"expansions"`
}

// CapacityResources keeps each unknown fact as nil, never zero.
type CapacityResources struct {
	Servers      *int64 `json:"servers"`
	CPUMillis    *int64 `json:"cpu_millis"`
	MemoryBytes  *int64 `json:"memory_bytes"`
	StorageBytes *int64 `json:"storage_bytes"`
}

// CapacityObservation is what a server's runtime last reported.
type CapacityObservation struct {
	CPUMillis        *int64    `json:"cpu_millis"`
	MemoryBytes      *int64    `json:"memory_bytes"`
	StorageBytes     *int64    `json:"storage_bytes"`
	FreeStorageBytes *int64    `json:"free_storage_bytes"`
	CheckedAt        time.Time `json:"checked_at"`
}

// CapacityAccountSnapshot is one provider account's totals and quota.
type CapacityAccountSnapshot struct {
	Provider    string                 `json:"provider"`
	AccountID   string                 `json:"account_id"`
	Provisioned CapacityResources      `json:"provisioned"`
	Quota       provider.CapacityQuota `json:"quota"`
	CheckedAt   time.Time              `json:"checked_at"`
}

// CapacityRecord is one counted server or volume. A nil GPUs is a count
// Loomplane couldn't read.
type CapacityRecord struct {
	Kind                string               `json:"kind"`
	Provider            string               `json:"provider"`
	AccountID           string               `json:"account_id"`
	ResourceID          string               `json:"resource_id"`
	ServerID            string               `json:"server_id,omitempty"`
	ServerStateRevision int64                `json:"server_state_revision"`
	Resources           CapacityResources    `json:"resources"`
	Observation         *CapacityObservation `json:"observation,omitempty"`
	CheckedAt           time.Time            `json:"checked_at"`
	GPUKind             string               `json:"gpu_kind,omitempty"`
	GPUs                *int64               `json:"gpus,omitempty"`
}

// GPUUsage counts one GPU kind. Provisioned and Limit are nil when unknown.
// Pending is part of Reserved, so the two are never added.
type GPUUsage struct {
	Kind        string `json:"kind"`
	Provisioned *int64 `json:"provisioned"`
	Pending     int64  `json:"pending"`
	Reserved    int64  `json:"reserved"`
	Limit       *int64 `json:"limit"`
}

// FrontendUsage counts one managed target's frontend slots. A nil Maximum is unlimited.
type FrontendUsage struct {
	TargetID string `json:"target_id"`
	Maximum  *int64 `json:"maximum"`
	Counted  int64  `json:"counted"`
	Active   int64  `json:"active"`
	Pending  int64  `json:"pending"`
	Retained int64  `json:"retained"`
}

// CapacityUsage is what a Cloud counts against its ceilings.
type CapacityUsage struct {
	CloudID              string                    `json:"cloud_id"`
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
	Frontends            []FrontendUsage           `json:"frontends"`
}

// CapacityPolicyView is a Cloud's policy as the capacity-policy route
// answers it. AutomaticExpansion says whether this Loomplane server adds
// servers on its own; it's read-only and a save never takes it.
type CapacityPolicyView struct {
	CloudID            string          `json:"cloud_id"`
	Policy             *CapacityPolicy `json:"policy"`
	AutomaticExpansion bool            `json:"automatic_expansion"`
}

func (c *Client) CapacityPolicy(ctx context.Context, cloud string) (*CapacityPolicyView, error) {
	var out CapacityPolicyView
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/capacity-policy", nil, &out)
	return &out, err
}

func (c *Client) SaveCapacityPolicy(ctx context.Context, cloud string, in CapacityPolicyInput) (*CapacityPolicyView, error) {
	var out CapacityPolicyView
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/capacity-policy", in, &out)
	return &out, err
}

// CreateScalingPool creates an empty pool and saves the policy that grows it
// in one step. A policy that moved since ExpectedRevision is ErrConflict, and
// then neither the pool nor the policy changes.
func (c *Client) CreateScalingPool(ctx context.Context, cloud string, in ScalingPoolInput) (*ScalingPool, error) {
	var out ScalingPool
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/scaling-pools", in, &out)
	return &out, err
}

func (c *Client) CapacityUsage(ctx context.Context, cloud string) (*CapacityUsage, error) {
	var out CapacityUsage
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/capacity-usage", nil, &out)
	return &out, err
}

func (c *Client) RefreshCapacity(ctx context.Context, cloud string) (*CapacityUsage, error) {
	var out CapacityUsage
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/capacity-usage/refresh", nil, &out)
	return &out, err
}

func (c *Client) Expansions(ctx context.Context, cloud string) ([]Expansion, error) {
	var out ExpansionList
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/expansions", nil, &out)
	return out.Expansions, err
}

func (c *Client) ApproveExpansion(ctx context.Context, cloud, id string, expectedRevision int64) (*Expansion, error) {
	var out Expansion
	in := struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}{expectedRevision}
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/expansions/"+esc(id)+"/approve", in, &out)
	return &out, err
}

// ResumeCapacity checks capacity again for a paused deployment. It doesn't
// skip the check: a deployment still blocked pauses again.
func (c *Client) ResumeCapacity(ctx context.Context, deploymentID string, expectedRevision int64) (*Deployment, error) {
	return c.capacityWait(ctx, deploymentID, "resume", expectedRevision)
}

// CancelCapacityWait cancels a paused deployment. Steps that finished stay.
func (c *Client) CancelCapacityWait(ctx context.Context, deploymentID string, expectedRevision int64) (*Deployment, error) {
	return c.capacityWait(ctx, deploymentID, "cancel", expectedRevision)
}

func (c *Client) capacityWait(ctx context.Context, deploymentID, decision string, expectedRevision int64) (*Deployment, error) {
	var out Deployment
	in := struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}{expectedRevision}
	err := c.do(ctx, http.MethodPost, "/api/v1/deployments/"+esc(deploymentID)+"/capacity/"+decision, in, &out)
	return &out, err
}
