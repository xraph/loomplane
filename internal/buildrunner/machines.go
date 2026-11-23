package buildrunner

import (
	"context"
	"time"
)

// Machine states. A machine is recorded as creating before the cloud is
// asked for it, so a crash never loses one.
const (
	MachineCreating = "creating"
	MachineBooting  = "booting"
	MachineReady    = "ready"
	MachineRetiring = "retiring"
	MachineGone     = "gone"
)

// Machine is a machine an on-demand pool started in its cloud account.
type Machine struct {
	ID     string `json:"id"`
	PoolID string `json:"pool_id"`
	OrgID  string `json:"org_id"`
	// Name is the machine's hostname, runner-<id>. It finds a machine
	// whose create answered before its ID was recorded.
	Name       string `json:"name"`
	ProviderID string `json:"provider_id,omitempty"`
	Region     string `json:"region"`
	Size       string `json:"size"`
	// Warm machines take builds until replaced; cold ones take one.
	Warm   bool   `json:"warm"`
	State  string `json:"state"`
	NodeID string `json:"node_id,omitempty"`
	// InFlight is the builds running on it now.
	InFlight int `json:"in_flight"`
	Builds   int `json:"builds"`
	// Reason says why it is retiring or gone.
	Reason    string     `json:"reason,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	JoinedAt  *time.Time `json:"joined_at,omitempty"`
	// ActiveAt is when it last started or finished a build, or joined.
	ActiveAt time.Time  `json:"active_at"`
	GoneAt   *time.Time `json:"gone_at,omitempty"`
}

// MachineStore keeps an on-demand pool's machines.
type MachineStore interface {
	CreateMachine(ctx context.Context, m *Machine) error
	UpdateMachine(ctx context.Context, m *Machine) error
	ListMachines(ctx context.Context, poolID string) ([]Machine, error)
	MachineByNode(ctx context.Context, nodeID string) (*Machine, error)
	// AdjustInFlight adds delta to a machine's builds in flight, and counts
	// a finished one.
	AdjustInFlight(ctx context.Context, nodeID string, delta int, finished bool, at time.Time) error
	// OnDemandPools lists every on-demand pool, across organizations.
	OnDemandPools(ctx context.Context) ([]Pool, error)
}

// MachineSpec is a machine to start.
type MachineSpec struct {
	// ID is the machine's ID, which the cloud's operation tag carries, so
	// a retry finds what an earlier attempt started.
	ID, Name, Region, Size, Image string
	Tags                          []string
	// PoolTag is the tag every machine of the pool carries.
	PoolTag  string
	UserData string
	// Spot asks for an interruptible machine.
	Spot bool
	// ReconcileOnly finds an earlier attempt's machine without starting one.
	ReconcileOnly bool
}

// Provisioner starts and removes machines in a pool's cloud account. The
// runners extension satisfies it for DigitalOcean.
type Provisioner interface {
	Create(ctx context.Context, orgID, cloudConnection string, spec MachineSpec) (providerID string, err error)
	Delete(ctx context.Context, orgID, cloudConnection, region, providerID string) error
	// List names the machines carrying tag, by provider ID and name.
	List(ctx context.Context, orgID, cloudConnection, region, tag string) (map[string]string, error)
}
