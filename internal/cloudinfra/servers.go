package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

var (
	ErrInvalid  = errors.New("invalid infrastructure request")
	ErrNotFound = errors.New("infrastructure resource not found")
	ErrConflict = errors.New("infrastructure changed; review again")
	ErrBusy     = errors.New("infrastructure operation is already running")
	ErrPending  = errors.New("infrastructure operation is waiting for verification")
	// ErrDirectApplyRunning refuses a change to a server while a direct
	// deployment runs on its daemon. errors.Is reports it as ErrBusy.
	ErrDirectApplyRunning error = directApplyRunningError{}
)

type directApplyRunningError struct{}

func (directApplyRunningError) Error() string {
	return "a direct deployment is running on this server; try again after it finishes"
}

func (directApplyRunningError) Is(target error) bool { return target == ErrBusy }

// Server is organization capacity, independent of project release access.
type Server struct {
	Draining            bool          `json:"draining"`
	StateRevision       int64         `json:"state_revision"`
	ID                  string        `json:"id" forge:"id"`
	OrgID               string        `json:"org_id"`
	CloudID             string        `json:"cloud_id"`
	Name                string        `json:"name"`
	Ownership           string        `json:"ownership"`
	Provider            string        `json:"provider"`
	ConnectionID        string        `json:"connection_id"`
	AccountID           string        `json:"account_id"`
	ResourceID          string        `json:"resource_id"`
	CreationOperationID string        `json:"creation_operation_id"`
	RuntimeConnectionID string        `json:"runtime_connection_id"`
	Region              string        `json:"region"`
	Size                string        `json:"size"`
	Status              string        `json:"status"`
	Revision            int64         `json:"revision"`
	Health              RuntimeHealth `json:"health"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
	// Accelerator and the hourly price come from the catalog size the server
	// was bought or resized to, so a GPU's cost stays on screen while it runs.
	Accelerator      *provider.Accelerator `json:"accelerator,omitempty"`
	HourlyUSD        float64               `json:"hourly_usd,omitempty"`
	HourlyPriceKnown bool                  `json:"hourly_price_known,omitempty"`
	// PricedSize is the size the price above belongs to. A server whose size
	// moved on, or whose price is unknown, is priced again from the catalog.
	PricedSize string `json:"priced_size,omitempty"`
}

// RuntimeHealth comes from the pinned Docker daemon, never a provider size slug.
type RuntimeHealth struct {
	StorageBytes         *int64               `json:"storage_bytes"`
	FreeStorageBytes     *int64               `json:"free_storage_bytes"`
	EffectiveCPUMillis   int64                `json:"effective_cpu_millis"`
	EffectiveMemoryBytes int64                `json:"effective_memory_bytes"`
	DockerID             string               `json:"docker_id"`
	Architecture         string               `json:"architecture"`
	CPUs                 int                  `json:"cpus"`
	MemoryBytes          int64                `json:"memory_bytes"`
	GPUs                 []provider.GPUDevice `json:"gpus,omitempty"`
	GPURuntime           string               `json:"gpu_runtime,omitempty"` // a provider.GPUStatus* value
	GPUMessage           string               `json:"gpu_message,omitempty"`
	CheckedAt            time.Time            `json:"checked_at"`
}

// ServerInput describes exactly one reviewed lifecycle change. Adoption refers
// to an existing pinned SSH connection and never transfers provider ownership.
type ServerInput struct {
	AcknowledgeUnknownCosts bool   `json:"acknowledge_unknown_costs,omitempty"`
	Kind                    string `json:"kind"`
	ServerID                string `json:"server_id,omitempty"`
	ConnectionID            string `json:"connection_id,omitempty"`
	Name                    string `json:"name,omitempty"`
	Region                  string `json:"region,omitempty"`
	Size                    string `json:"size,omitempty"`
	Image                   string `json:"image,omitempty"`
	CloudRevision           int64  `json:"cloud_revision"`
	ServerRevision          int64  `json:"server_revision,omitempty"`
	ConfirmAdoption         bool   `json:"confirm_adoption,omitempty"`
}

// Operation retains the immutable review and public execution checkpoints.
// Bootstrap credentials are stored only in Vault, keyed by this operation ID.
type Operation struct {
	BudgetReview         *BudgetReview                   `json:"budget_review,omitempty"`
	MoneyEstimate        *MoneyEstimate                  `json:"money_estimate,omitempty"`
	CapacityTarget       *ResourceVector                 `json:"capacity_target,omitempty"`
	CapacityCheckedAt    time.Time                       `json:"capacity_checked_at,omitempty,omitzero"`
	AbsenceConfirmedAt   time.Time                       `json:"absence_confirmed_at,omitempty,omitzero"`
	EstimatedHourlyUSD   float64                         `json:"estimated_hourly_usd"`
	HourlyPriceKnown     bool                            `json:"hourly_price_known,omitempty"`
	Accelerator          *provider.Accelerator           `json:"accelerator,omitempty"`
	StateRevision        int64                           `json:"state_revision"`
	ID                   string                          `json:"id" forge:"id"`
	OrgID                string                          `json:"org_id"`
	CloudID              string                          `json:"cloud_id"`
	ServerID             string                          `json:"server_id"`
	Review               string                          `json:"review"`
	Input                ServerInput                     `json:"input"`
	Provider             string                          `json:"provider"`
	AccountID            string                          `json:"account_id"`
	ConnectionID         string                          `json:"connection_id"`
	ConnectionGeneration int64                           `json:"connection_generation,omitempty"`
	ConnectionRevision   time.Time                       `json:"connection_revision"`
	Ownership            string                          `json:"ownership"`
	ResourceID           string                          `json:"resource_id"`
	Status               string                          `json:"status"`
	Message              string                          `json:"message"`
	Attempt              int                             `json:"attempt"`
	Attempted            bool                            `json:"attempted"`
	Actions              []provider.InfrastructureAction `json:"actions"`
	PublicIPv4           []string                        `json:"public_ipv4"`
	HostKey              string                          `json:"host_key,omitempty"`
	Enrolled             bool                            `json:"enrolled"`
	BootstrapExpiresAt   time.Time                       `json:"bootstrap_expires_at"`
	CreatedAt            time.Time                       `json:"created_at"`
	ExpiresAt            time.Time                       `json:"expires_at"`
	UpdatedAt            time.Time                       `json:"updated_at"`
	// Principal is private persisted approval identity, rechecked before new work.
	Principal security.Principal `json:"-"`
}

type ApplyInput struct {
	Review string `json:"review"`
}

// Workflow starts durable server operations. The same value may also be a
// RouteWorkflow and a RegistrationWorkflow; the service checks for each.
type Workflow interface {
	StartInfrastructure(context.Context, string, string, int) error
}
type Credentials interface {
	Credential(context.Context, string, string) (*connections.Connection, []byte, error)
}
type Clouds interface {
	GetProfile(context.Context, string, string, string) (*cloudprofiles.Profile, error)
}
type Runtime interface {
	Inspect(context.Context, []byte, map[string]string, bool) (RuntimeHealth, error)
}

type Deps struct {
	RegistryFiles           *backupfiles.Store
	ManagedClusters         config.ManagedClustersConfig
	ClusterWorkflow         ClusterWorkflow
	Clusters                ClusterStore
	CapacityPrincipal       security.PrincipalVerifier
	CapacityDemandAuthority CapacityDemandAuthority
	CapacityDemandWakeup    CapacityDemandWakeup
	// EnableAutomaticCapacity stays false until integrated qualification.
	EnableAutomaticCapacity bool
	Domains                 DomainStore
	Registrations           RegistrationStore
	Notices                 RenewalNotifier
	Resolver                DomainResolver
	PoolCleanup             PoolCleanup
	Store                   Store
	Clouds                  Clouds
	Connections             Credentials
	RuntimeConnections      connections.Store
	Secrets                 connections.Secrets
	Authz                   connections.Authorizer
	Adapters                *Adapters
	Workflow                Workflow
	Runtime                 Runtime
	Audit                   projects.Auditor
	EnrollmentURL           string
	AllowLocalEnrollment    bool
	// Warn reports a failure the service works around, such as a price it
	// couldn't save. Nil drops the report.
	Warn func(string, error)
	// ACMEDirectory is where Kubernetes routes order certificates. Empty
	// means Let's Encrypt's production directory.
	ACMEDirectory string
}

type Service struct {
	Deps
	now func() time.Time
	// started is when this process made the service. Every verified zone's
	// record is rechecked once after it, then daily.
	started           time.Time
	expansionSchedule *expansionSchedule
	catalogs          *catalogCache
	options           *optionsCache
	// kubernetesRoutes and routeHTTPS reach a route's cluster and public
	// hostname. Tests replace them.
	kubernetesRoutes func(ctx context.Context, route Route, cleanup bool) (kubernetesRouteClient, error)
	routeHTTPS       func(ctx context.Context, hostname string) (time.Time, int, error)
	// ingressImagePinned and ingressNeedsUpgrade tell a wildcard preview
	// whether its server's proxy has to restart. Tests replace them.
	ingressImagePinned  func() bool
	ingressNeedsUpgrade func(ctx context.Context, route Route) (bool, error)
	// writeDNSToken puts a challenge token on a server. Tests replace it.
	writeDNSToken func(ctx context.Context, c *server.Client, org, serverID, zoneID string, token []byte) error
}

func NewService(d Deps) *Service {
	if d.Runtime == nil {
		d.Runtime = SSHRuntime{}
	}
	s := &Service{Deps: d, now: time.Now, started: time.Now().UTC(), expansionSchedule: &expansionSchedule{}, catalogs: &catalogCache{entries: map[string]catalogEntry{}}, options: &optionsCache{entries: map[string]optionsEntry{}}, routeHTTPS: probeRouteHTTPS, ingressImagePinned: server.IngressImagePinned, writeDNSToken: server.WriteIngressDNSToken}
	s.kubernetesRoutes = s.openKubernetesRoute
	s.ingressNeedsUpgrade = s.routeIngressNeedsUpgrade
	return s
}

func (s *Service) warn(msg string, err error) {
	if s.Warn != nil {
		s.Warn(msg, err)
	}
}

func (s *Service) authorize(ctx context.Context, p security.Principal, cloud string) (*cloudprofiles.Profile, error) {
	if p.OrgID == "" || p.UserID == "" {
		return nil, security.ErrUnauthenticated
	}
	if err := s.Authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	return s.Clouds.GetProfile(ctx, p.OrgID, "", cloud)
}
func (s *Service) ListServers(ctx context.Context, p security.Principal, cloud string) ([]*Server, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	return s.Store.ListServers(ctx, p.OrgID, cloud)
}
func (s *Service) GetOperation(ctx context.Context, p security.Principal, cloud, id string) (*Operation, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	return s.Store.GetOperation(ctx, p.OrgID, cloud, id)
}
func (s *Service) ListOperations(ctx context.Context, p security.Principal, cloud string) ([]*Operation, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	return s.Store.ListOperations(ctx, p.OrgID, cloud)
}

// OpenServerCreates lists the cloud's create operations that were approved
// and haven't finished, however old, so a caller naming a new server can
// keep clear of theirs. See PgStore.OpenCreates.
func (s *Service) OpenServerCreates(ctx context.Context, p security.Principal, cloud string) ([]*Operation, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	return s.Store.OpenCreates(ctx, p.OrgID, cloud)
}

// CheckServer refreshes health through the recorded pinned runtime connection.
func (s *Service) CheckServer(ctx context.Context, p security.Principal, cloud, id string) (*Server, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	return s.checkServer(ctx, p.OrgID, cloud, id)
}
func (s *Service) checkServer(ctx context.Context, org, cloud, id string) (*Server, error) {
	v, err := s.Store.GetServer(ctx, org, cloud, id)
	if err != nil {
		return nil, err
	}
	if v.Status != "ready" && v.Status != "unhealthy" {
		return nil, ErrConflict
	}
	conn, raw, err := s.Connections.Credential(ctx, org, v.RuntimeConnectionID)
	if err != nil {
		return nil, err
	}
	health, checkErr := s.Runtime.Inspect(ctx, raw, conn.Details, false)
	if checkErr != nil || health.DockerID != v.Health.DockerID {
		v.Status = "unhealthy"
	} else {
		health = gpuHealth(health, v.Accelerator)
		// Planning reads GPUs from the runtime connection, so a check keeps it current.
		if syncErr := s.checkedRuntimeGPUs(ctx, org, conn, health); syncErr != nil {
			return nil, syncErr
		}
		if v.Health.EffectiveCPUMillis != health.EffectiveCPUMillis || v.Health.EffectiveMemoryBytes != health.EffectiveMemoryBytes || v.Health.CPUs != health.CPUs || v.Health.MemoryBytes != health.MemoryBytes || v.Health.Architecture != health.Architecture {
			v.Revision++
		}
		v.Status = "ready"
		v.Health = health
	}
	v.UpdatedAt = s.now().UTC()
	if saveErr := s.Store.SaveServer(ctx, v); saveErr != nil {
		return nil, saveErr
	}
	return v, nil
}
