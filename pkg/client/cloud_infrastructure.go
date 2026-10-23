package client

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// CloudServer mirrors the public infrastructure API contract.
type CloudServer struct {
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
}

// RuntimeHealth mirrors the public infrastructure API contract.
type RuntimeHealth struct {
	EffectiveCPUMillis   int64     `json:"effective_cpu_millis"`
	EffectiveMemoryBytes int64     `json:"effective_memory_bytes"`
	DockerID             string    `json:"docker_id"`
	Architecture         string    `json:"architecture"`
	CPUs                 int       `json:"cpus"`
	MemoryBytes          int64     `json:"memory_bytes"`
	CheckedAt            time.Time `json:"checked_at"`
}

// ServerInput mirrors the public infrastructure API contract.
type ServerInput struct {
	Kind            string `json:"kind"`
	ServerID        string `json:"server_id,omitempty"`
	ConnectionID    string `json:"connection_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Region          string `json:"region,omitempty"`
	Size            string `json:"size,omitempty"`
	Image           string `json:"image,omitempty"`
	CloudRevision   int64  `json:"cloud_revision"`
	ServerRevision  int64  `json:"server_revision,omitempty"`
	ConfirmAdoption bool   `json:"confirm_adoption,omitempty"`
}

// InfrastructureOperation mirrors the public infrastructure API contract.
type InfrastructureOperation struct {
	EstimatedHourlyUSD   float64                         `json:"estimated_hourly_usd"`
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
}

// InfrastructureApplyInput mirrors the public infrastructure API contract.
type InfrastructureApplyInput struct {
	Review string `json:"review"`
}

// Pool mirrors the public infrastructure API contract.
type Pool struct {
	ID       string       `json:"id" forge:"id"`
	OrgID    string       `json:"org_id"`
	CloudID  string       `json:"cloud_id"`
	Name     string       `json:"name"`
	Revision int64        `json:"revision"`
	Members  []PoolMember `json:"members"`
}

// PoolMember mirrors the public infrastructure API contract.
type PoolMember struct {
	ServerID    string `json:"server_id"`
	CPUMillis   int64  `json:"cpu_millis"`
	MemoryBytes int64  `json:"memory_bytes"`
	Draining    bool   `json:"draining"`
}

// PoolInput mirrors the public infrastructure API contract.
type PoolInput struct {
	Name     string       `json:"name"`
	Revision int64        `json:"revision"`
	Members  []PoolMember `json:"members"`
}

// DrainReview mirrors the public infrastructure API contract.
type DrainReview struct {
	ServerID       string       `json:"server_id"`
	ServerRevision int64        `json:"server_revision"`
	Review         string       `json:"review"`
	Reservations   int          `json:"reservations"`
	Usage          *ServerUsage `json:"usage"`
}

// MoveInput mirrors the public infrastructure API contract.
type MoveInput struct {
	ProjectID           string `json:"project_id"`
	EnvironmentID       string `json:"environment_id"`
	Service             string `json:"service"`
	PoolID              string `json:"pool_id"`
	DestinationServerID string `json:"destination_server_id"`
	Review              string `json:"review,omitempty"`
}

// MoveReview mirrors the public infrastructure API contract.
type MoveReview struct {
	Input          MoveInput `json:"input"`
	SourceServerID string    `json:"source_server_id"`
	Placement      Placement `json:"placement"`
	Review         string    `json:"review"`
}

// Placement mirrors the public infrastructure API contract.
type Placement struct {
	Allocation string `json:"allocation,omitempty"`

	PoolID               string `json:"pool_id"`
	PoolRevision         int64  `json:"pool_revision"`
	ServerID             string `json:"server_id"`
	ServerRevision       int64  `json:"server_revision"`
	ConnectionID         string `json:"connection_id"`
	ConnectionGeneration int64  `json:"connection_generation"`
	CPUMillis            int64  `json:"cpu_millis"`
	MemoryBytes          int64  `json:"memory_bytes"`
}

// ServerWorkload mirrors the public infrastructure API contract.
type ServerWorkload struct {
	ExecutionRevision int64  `json:"execution_revision"`
	ProjectID         string `json:"project_id"`
	EnvironmentID     string `json:"environment_id"`
	Service           string `json:"service"`
	PlanID            string `json:"plan_id"`
	DeploymentID      string `json:"deployment_id,omitempty"`
	PoolID            string `json:"pool_id"`
	BindingID         string `json:"binding_id,omitempty"`
	Phase             string `json:"phase"`
	Active            bool   `json:"active"`
	CPUMillis         int64  `json:"cpu_millis"`
	MemoryBytes       int64  `json:"memory_bytes"`
}

// RetainedPoolStorage mirrors the public infrastructure API contract.
type RetainedPoolStorage struct {
	PlanID        string `json:"plan_id"`
	Service       string `json:"service"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
}

// ServerUsage mirrors the public infrastructure API contract.
type ServerUsage struct {
	RetainedStorage     []RetainedPoolStorage `json:"retained_storage"`
	ServerID            string                `json:"server_id"`
	Draining            bool                  `json:"draining"`
	ReservedCPUMillis   int64                 `json:"reserved_cpu_millis"`
	ReservedMemoryBytes int64                 `json:"reserved_memory_bytes"`
	Workloads           []ServerWorkload      `json:"workloads"`
}

// Zone mirrors the public infrastructure API contract.
type Zone struct {
	ID                   string           `json:"id" forge:"id"`
	OrgID                string           `json:"org_id"`
	Name                 string           `json:"name"`
	Mode                 string           `json:"mode"`
	ConnectionID         string           `json:"connection_id,omitempty"`
	ConnectionGeneration int64            `json:"connection_generation,omitempty"`
	Provider             string           `json:"provider,omitempty"`
	AccountID            string           `json:"account_id,omitempty"`
	RecordOwnership      bool             `json:"record_ownership"`
	ProviderZone         provider.DNSZone `json:"provider_zone"`
	ProjectIDs           []string         `json:"project_ids"`
	ProofName            string           `json:"proof_name,omitempty"`
	ProofValue           string           `json:"proof_value,omitempty"`
	ProofExpiresAt       time.Time        `json:"proof_expires_at"`
	VerifiedAt           time.Time        `json:"verified_at"`
	Revision             int64            `json:"revision"`
}

// ZoneInput mirrors the public infrastructure API contract.
type ZoneInput struct {
	Name         string   `json:"name"`
	Mode         string   `json:"mode"`
	ConnectionID string   `json:"connection_id,omitempty"`
	ProjectIDs   []string `json:"project_ids"`
}

// ZoneUpdate mirrors the public infrastructure API contract.
type ZoneUpdate struct {
	Revision   int64    `json:"revision"`
	ProjectIDs []string `json:"project_ids"`
	RenewProof bool     `json:"renew_proof"`
}

// ProjectZone mirrors the public infrastructure API contract.
type ProjectZone struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Verified bool   `json:"verified"`
}

// RouteInput mirrors the public infrastructure API contract.
type RouteInput struct {
	ExternalTTLSeconds int    `json:"external_ttl_seconds,omitempty"`
	ZoneID             string `json:"zone_id"`
	CloudID            string `json:"cloud_id"`
	EnvironmentID      string `json:"environment_id"`
	Service            string `json:"service"`
	Hostname           string `json:"hostname"`
	Port               int    `json:"port"`
}

// RouteBinding mirrors the public infrastructure API contract.
type RouteBinding struct {
	RuntimeID            string `json:"runtime_id"`
	BindingID            string `json:"binding_id"`
	ConfigDigest         string `json:"config_digest"`
	ServerID             string `json:"server_id"`
	TargetID             string `json:"target_id"`
	ConnectionID         string `json:"connection_id"`
	ConnectionGeneration int64  `json:"connection_generation"`
	InstanceID           string `json:"instance_id"`
	ReleaseID            string `json:"release_id"`
	ContainerID          string `json:"container_id"`
	Address              string `json:"address"`
}

// Route mirrors the public infrastructure API contract.
type Route struct {
	ID           string              `json:"id" forge:"id"`
	OrgID        string              `json:"org_id"`
	ProjectID    string              `json:"project_id"`
	Input        RouteInput          `json:"input"`
	Binding      RouteBinding        `json:"binding"`
	Status       string              `json:"status"`
	OperationID  string              `json:"operation_id"`
	Receipt      *provider.DNSRecord `json:"receipt,omitempty"`
	Verification RouteVerification   `json:"verification"`
	Revision     int64               `json:"revision"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// RouteOperation mirrors the public infrastructure API contract.
type RouteOperation struct {
	CreatedAt       time.Time           `json:"created_at"`
	Source          *Route              `json:"source,omitempty"`
	HandoffID       string              `json:"handoff_id,omitempty"`
	NotBefore       time.Time           `json:"not_before"`
	ID              string              `json:"id" forge:"id"`
	Route           Route               `json:"route"`
	Kind            string              `json:"kind"`
	Status          string              `json:"status"`
	Message         string              `json:"message"`
	Review          string              `json:"review"`
	ExpiresAt       time.Time           `json:"expires_at"`
	Attempt         int                 `json:"attempt"`
	Revision        int64               `json:"revision"`
	Generation      int64               `json:"generation"`
	DNSAttempted    bool                `json:"dns_attempted"`
	DesiredRecord   provider.DNSRecord  `json:"desired_record"`
	PreviousReceipt *provider.DNSRecord `json:"previous_receipt,omitempty"`
	Receipt         *provider.DNSRecord `json:"receipt,omitempty"`
}

// RouteVerification mirrors the public infrastructure API contract.
type RouteVerification struct {
	DNS          bool      `json:"dns"`
	TLS          bool      `json:"tls"`
	Backend      bool      `json:"backend"`
	CheckedAt    time.Time `json:"checked_at"`
	CertExpires  time.Time `json:"cert_expires"`
	DNSError     string    `json:"dns_error,omitempty"`
	TLSError     string    `json:"tls_error,omitempty"`
	BackendError string    `json:"backend_error,omitempty"`
}

// RouteTTLInput mirrors the public infrastructure API contract.
type RouteTTLInput struct {
	Revision           int64 `json:"revision"`
	ExternalTTLSeconds int   `json:"external_ttl_seconds"`
}

func infrastructurePath(cloud string) string { return "/api/v1/clouds/" + esc(cloud) }
func routePath(project string) string        { return "/api/v1/projects/" + esc(project) }
func (c *Client) CloudServers(ctx context.Context, cloud string) ([]CloudServer, error) {
	var out struct {
		Servers []CloudServer `json:"servers"`
	}
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/servers", nil, &out)
	return out.Servers, err
}
func (c *Client) CloudServerOptions(ctx context.Context, cloud, connection string) (*provider.ServerOptions, error) {
	var out provider.ServerOptions
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/server-options/"+esc(connection), nil, &out)
	return &out, err
}
func (c *Client) CheckCloudServer(ctx context.Context, cloud, server string) (*CloudServer, error) {
	var out CloudServer
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/servers/"+esc(server)+"/check", nil, &out)
	return &out, err
}
func (c *Client) PreviewCloudServer(ctx context.Context, cloud string, in ServerInput) (*InfrastructureOperation, error) {
	var out InfrastructureOperation
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/servers/preview", in, &out)
	return &out, err
}
func (c *Client) InfrastructureOperations(ctx context.Context, cloud string) ([]InfrastructureOperation, error) {
	var out struct {
		Operations []InfrastructureOperation `json:"operations"`
	}
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/infrastructure-operations", nil, &out)
	return out.Operations, err
}
func (c *Client) InfrastructureOperation(ctx context.Context, cloud, id string) (*InfrastructureOperation, error) {
	var out InfrastructureOperation
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/infrastructure-operations/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) ApplyInfrastructureOperation(ctx context.Context, cloud, id string, in InfrastructureApplyInput) (*InfrastructureOperation, error) {
	var out InfrastructureOperation
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/infrastructure-operations/"+esc(id)+"/apply", in, &out)
	return &out, err
}
func (c *Client) RetryInfrastructureOperation(ctx context.Context, cloud, id string) (*InfrastructureOperation, error) {
	var out InfrastructureOperation
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/infrastructure-operations/"+esc(id)+"/retry", nil, &out)
	return &out, err
}
func (c *Client) CancelInfrastructureOperation(ctx context.Context, cloud, id string) (*InfrastructureOperation, error) {
	var out InfrastructureOperation
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/infrastructure-operations/"+esc(id)+"/cancel", nil, &out)
	return &out, err
}
func (c *Client) CloudServerUsage(ctx context.Context, cloud, server string) (*ServerUsage, error) {
	var out ServerUsage
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/servers/"+esc(server)+"/usage", nil, &out)
	return &out, err
}
func (c *Client) PreviewCloudServerDrain(ctx context.Context, cloud, server string) (*DrainReview, error) {
	var out DrainReview
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/servers/"+esc(server)+"/drain/preview", nil, &out)
	return &out, err
}
func (c *Client) ApplyCloudServerDrain(ctx context.Context, cloud, server string, in InfrastructureApplyInput) (*CloudServer, error) {
	var out CloudServer
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/servers/"+esc(server)+"/drain/apply", in, &out)
	return &out, err
}
func (c *Client) CloudPools(ctx context.Context, cloud string) ([]Pool, error) {
	var out struct {
		Pools []Pool `json:"pools"`
	}
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/pools", nil, &out)
	return out.Pools, err
}
func (c *Client) CreateCloudPool(ctx context.Context, cloud string, in PoolInput) (*Pool, error) {
	var out Pool
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/pools", in, &out)
	return &out, err
}
func (c *Client) UpdateCloudPool(ctx context.Context, cloud, pool string, in PoolInput) (*Pool, error) {
	var out Pool
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/pools/"+esc(pool), in, &out)
	return &out, err
}
func (c *Client) PreviewCloudMove(ctx context.Context, cloud string, in MoveInput) (*MoveReview, error) {
	var out MoveReview
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/placements/preview-move", in, &out)
	return &out, err
}
func (c *Client) AcceptCloudMove(ctx context.Context, cloud string, in MoveInput) (*MoveReview, error) {
	var out MoveReview
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/placements/accept-move", in, &out)
	return &out, err
}
func (c *Client) DNSZones(ctx context.Context) ([]Zone, error) {
	var out struct {
		Zones []Zone `json:"zones"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/dns-zones", nil, &out)
	return out.Zones, err
}
func (c *Client) ProjectDNSZones(ctx context.Context, project string) ([]ProjectZone, error) {
	var out struct {
		Zones []ProjectZone `json:"zones"`
	}
	err := c.do(ctx, http.MethodGet, routePath(project)+"/dns-zones", nil, &out)
	return out.Zones, err
}
func (c *Client) CreateDNSZone(ctx context.Context, in ZoneInput) (*Zone, error) {
	var out Zone
	err := c.do(ctx, http.MethodPost, "/api/v1/dns-zones", in, &out)
	return &out, err
}
func (c *Client) UpdateDNSZone(ctx context.Context, id string, in ZoneUpdate) (*Zone, error) {
	var out Zone
	err := c.do(ctx, http.MethodPost, "/api/v1/dns-zones/"+esc(id)+"/update", in, &out)
	return &out, err
}
func (c *Client) VerifyDNSZone(ctx context.Context, id string) (*Zone, error) {
	var out Zone
	err := c.do(ctx, http.MethodPost, "/api/v1/dns-zones/"+esc(id)+"/verify", nil, &out)
	return &out, err
}
func (c *Client) RemoveDNSZone(ctx context.Context, id string, in ResourceRevisionInput) error {
	return c.do(ctx, http.MethodPost, "/api/v1/dns-zones/"+esc(id)+"/remove", in, nil)
}

// ResourceRevisionInput fences removal against the version you reviewed.
type ResourceRevisionInput struct {
	Revision int64 `json:"revision"`
}

func (c *Client) DomainRoutes(ctx context.Context, project string) ([]Route, error) {
	var out struct {
		Routes []Route `json:"routes"`
	}
	err := c.do(ctx, http.MethodGet, routePath(project)+"/domain-routes", nil, &out)
	return out.Routes, err
}
func (c *Client) PreviewDomainRoute(ctx context.Context, project string, in RouteInput) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/domain-routes/preview", in, &out)
	return &out, err
}
func (c *Client) DomainRouteOperation(ctx context.Context, project, id string) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodGet, routePath(project)+"/route-operations/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) ApplyDomainRoute(ctx context.Context, project, id string, in InfrastructureApplyInput) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/route-operations/"+esc(id)+"/apply", in, &out)
	return &out, err
}
func (c *Client) RetryDomainRoute(ctx context.Context, project, id string) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/route-operations/"+esc(id)+"/retry", nil, &out)
	return &out, err
}
func (c *Client) VerifyDomainRoute(ctx context.Context, project, id string) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/domain-routes/"+esc(id)+"/verify", nil, &out)
	return &out, err
}
func (c *Client) RemoveDomainRoute(ctx context.Context, project, id string, in ResourceRevisionInput) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/domain-routes/"+esc(id)+"/remove", in, &out)
	return &out, err
}
func (c *Client) UpdateDomainRouteTTL(ctx context.Context, project, id string, in RouteTTLInput) (*Route, error) {
	var out Route
	err := c.do(ctx, http.MethodPost, routePath(project)+"/domain-routes/"+esc(id)+"/ttl", in, &out)
	return &out, err
}
func (c *Client) DomainRouteHistory(ctx context.Context, project, id string) ([]RouteOperation, error) {
	var out struct {
		Operations []RouteOperation `json:"operations"`
	}
	err := c.do(ctx, http.MethodGet, routePath(project)+"/domain-routes/"+esc(id)+"/operations", nil, &out)
	return out.Operations, err
}

// WaitInfrastructureOperation returns the latest persisted checkpoint on timeout.
func (c *Client) WaitInfrastructureOperation(ctx context.Context, cloud, id string) (*InfrastructureOperation, error) {
	var latest *InfrastructureOperation
	out, err := wait(ctx, func() (*InfrastructureOperation, bool, error) {
		v, e := c.InfrastructureOperation(ctx, cloud, id)
		if e != nil {
			return nil, false, e
		}
		latest = v
		return v, v.Status == "succeeded" || v.Status == "failed" || v.Status == "waiting" || v.Status == "cancelled" || v.Status == "preview", nil
	})
	if err != nil {
		return latest, err
	}
	return out, nil
}

// WaitDomainRouteOperation stops when the route needs verification or operator action.
func (c *Client) WaitDomainRouteOperation(ctx context.Context, project, id string) (*RouteOperation, error) {
	var latest *RouteOperation
	out, err := wait(ctx, func() (*RouteOperation, bool, error) {
		v, e := c.DomainRouteOperation(ctx, project, id)
		if e != nil {
			return nil, false, e
		}
		latest = v
		return v, v.Status != "pending" && v.Status != "running", nil
	})
	if err != nil {
		return latest, err
	}
	return out, nil
}

// RouteHandoffInput pins the source route and destination Cloud after a binding commits.
type RouteHandoffInput struct {
	Revision           int64  `json:"revision"`
	DestinationCloudID string `json:"destination_cloud_id"`
}

// RouteHandoffReleaseInput supplies the reviewed drain interval.
type RouteHandoffReleaseInput struct {
	DrainSeconds int `json:"drain_seconds"`
}

func (c *Client) PrepareDomainRouteHandoff(ctx context.Context, project, id string, in RouteHandoffInput) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/domain-routes/"+esc(id)+"/prepare-handoff", in, &out)
	return &out, err
}
func (c *Client) CutoverDomainRouteHandoff(ctx context.Context, project, id string) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/route-operations/"+esc(id)+"/cutover", nil, &out)
	return &out, err
}
func (c *Client) RollbackDomainRouteHandoff(ctx context.Context, project, id string) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/route-operations/"+esc(id)+"/rollback", nil, &out)
	return &out, err
}
func (c *Client) ReleaseDomainRouteHandoff(ctx context.Context, project, id string, in RouteHandoffReleaseInput) (*RouteOperation, error) {
	var out RouteOperation
	err := c.do(ctx, http.MethodPost, routePath(project)+"/route-operations/"+esc(id)+"/release-handoff", in, &out)
	return &out, err
}

// ReservationRecovery contains the accepted reservation's public cleanup state.
type ReservationRecovery struct {
	PlanID          string `json:"plan_id"`
	Service         string `json:"service"`
	ServerID        string `json:"server_id"`
	Phase           string `json:"phase"`
	Started         bool   `json:"started"`
	Active          bool   `json:"active"`
	Revision        int64  `json:"revision"`
	Review          string `json:"review"`
	StorageRetained bool   `json:"storage_retained"`
}

// RecoveryInput reviews cleanup of the exact accepted plan and service.
type RecoveryInput struct {
	PlanID  string `json:"plan_id"`
	Service string `json:"service"`
	Review  string `json:"review,omitempty"`
}

func (c *Client) PreviewCloudReservationRecovery(ctx context.Context, cloud string, in RecoveryInput) (*ReservationRecovery, error) {
	var out ReservationRecovery
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/placements/preview-recovery", in, &out)
	return &out, err
}
func (c *Client) RecoverCloudReservation(ctx context.Context, cloud string, in RecoveryInput) (*ReservationRecovery, error) {
	var out ReservationRecovery
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/placements/recover", in, &out)
	return &out, err
}
