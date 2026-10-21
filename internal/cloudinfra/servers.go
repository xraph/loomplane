package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
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
)

// Server is organization capacity, independent of project release access.
type Server struct {
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

// RuntimeHealth comes from the pinned Docker daemon, never a provider size slug.
type RuntimeHealth struct {
	DockerID     string    `json:"docker_id"`
	Architecture string    `json:"architecture"`
	CPUs         int       `json:"cpus"`
	MemoryBytes  int64     `json:"memory_bytes"`
	CheckedAt    time.Time `json:"checked_at"`
}

// ServerInput describes exactly one reviewed lifecycle change. Adoption refers
// to an existing pinned SSH connection and never transfers provider ownership.
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

// Operation retains the immutable review and public execution checkpoints.
// Bootstrap credentials are stored only in Vault, keyed by this operation ID.
type Operation struct {
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
	// Principal is private persisted approval identity, rechecked before new work.
	Principal security.Principal `json:"-"`
}

type ApplyInput struct {
	Review string `json:"review"`
}

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
	Store                Store
	Clouds               Clouds
	Connections          Credentials
	RuntimeConnections   connections.Store
	Secrets              connections.Secrets
	Authz                connections.Authorizer
	Adapters             *Adapters
	Workflow             Workflow
	Runtime              Runtime
	Audit                projects.Auditor
	EnrollmentURL        string
	AllowLocalEnrollment bool
}

type Service struct {
	Deps
	now func() time.Time
}

func NewService(d Deps) *Service {
	if d.Runtime == nil {
		d.Runtime = SSHRuntime{}
	}
	return &Service{Deps: d, now: time.Now}
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

// CheckServer refreshes health through the recorded pinned runtime connection.
func (s *Service) CheckServer(ctx context.Context, p security.Principal, cloud, id string) (*Server, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	v, err := s.Store.GetServer(ctx, p.OrgID, cloud, id)
	if err != nil {
		return nil, err
	}
	if v.Status != "ready" && v.Status != "unhealthy" {
		return nil, ErrConflict
	}
	conn, raw, err := s.Connections.Credential(ctx, p.OrgID, v.RuntimeConnectionID)
	if err != nil {
		return nil, err
	}
	health, checkErr := s.Runtime.Inspect(ctx, raw, conn.Details, false)
	if checkErr != nil || health.DockerID != v.Health.DockerID {
		v.Status = "unhealthy"
	} else {
		if v.Health.CPUs != health.CPUs || v.Health.MemoryBytes != health.MemoryBytes || v.Health.Architecture != health.Architecture {
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
