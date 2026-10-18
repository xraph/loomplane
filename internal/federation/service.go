package federation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ConnectionReader reads server-verified connection metadata without credentials.
type ConnectionReader interface {
	GetConnection(ctx context.Context, orgID, id string) (*connections.Connection, error)
}

// EnvironmentReader validates all three levels of destination ownership.
type EnvironmentReader interface {
	GetEnvironment(ctx context.Context, orgID, projectID, id string) (*projects.Environment, error)
}

// Service owns destination registration. It has no provider mutation interface.
type Service struct {
	store        Store
	connections  ConnectionReader
	environments EnvironmentReader
	authz        projects.Authorizer
	audit        projects.Auditor
	enabled      bool
	now          func() time.Time
}

// NewService returns a management service. Runtime activation is not implemented.
func NewService(store Store, connectionReader ConnectionReader, environments EnvironmentReader, authz projects.Authorizer, audit projects.Auditor, enabled bool) *Service {
	return &Service{store: store, connections: connectionReader, environments: environments, authz: authz, audit: audit, enabled: enabled, now: time.Now}
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (s *Service) authorize(ctx context.Context, p security.Principal, perm security.Permission, id string) error {
	if err := s.authz.Require(ctx, p, perm, id); err != nil {
		return err
	}
	if !s.enabled {
		return ErrDisabled
	}
	if p.OrgID == "" {
		return security.ErrUnauthenticated
	}
	return nil
}

// CreateDestination derives organization, identity and capabilities on the server.
// Importing only records an alias. It never installs an agent or edits a cluster.
func (s *Service) CreateDestination(ctx context.Context, p security.Principal, in domain.Destination) (*domain.Destination, error) {
	if err := s.authorize(ctx, p, security.DestinationManage, in.ProjectID); err != nil {
		return nil, err
	}
	if !namePattern.MatchString(in.Name) || in.Ownership != domain.Imported {
		return nil, ErrInvalid
	}
	env, err := s.environments.GetEnvironment(ctx, p.OrgID, in.ProjectID, in.EnvironmentID)
	if errors.Is(err, projects.ErrEnvironmentNotFound) || errors.Is(err, projects.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	conn, err := s.connection(ctx, p.OrgID, in.ConnectionID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	physical, err := verifiedIdentity(conn, now)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("dest")
	if err != nil {
		return nil, fmt.Errorf("generate destination ID: %w", err)
	}
	d := &domain.Destination{Scope: domain.Scope{OrgID: p.OrgID, ProjectID: env.ProjectID, EnvironmentID: env.ID}, ID: id.String(), Name: in.Name, ConnectionID: conn.ID, Ownership: domain.Imported, Identity: physical, Generation: 1, CreatedAt: now, UpdatedAt: now}
	d.Capabilities = unverifiedCapabilities(physical, now)
	if createErr := s.store.Create(ctx, d); createErr != nil {
		return nil, createErr
	}
	s.record(ctx, p, "federation.destination.create", d)
	return d, nil
}

// GetDestination never returns usable capabilities for changed or stale identity.
func (s *Service) GetDestination(ctx context.Context, p security.Principal, id string) (*domain.Destination, error) {
	if err := s.authorize(ctx, p, security.DestinationRead, id); err != nil {
		return nil, err
	}
	d, err := s.store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	conn, err := s.connection(ctx, p.OrgID, d.ConnectionID)
	if err != nil {
		return nil, err
	}
	physical, identityErr := verifiedIdentity(conn, s.now())
	if identityErr != nil || physical != d.Identity || d.Quarantined {
		d.Capabilities = domain.CapabilitySet{}
	}
	return d, nil
}

// RefreshCapabilities revalidates physical identity against checked metadata.
// No provider is qualified yet; an observation cannot invent support.
func (s *Service) RefreshCapabilities(ctx context.Context, p security.Principal, id string) (*domain.CapabilitySet, error) {
	if err := s.authorize(ctx, p, security.DestinationManage, id); err != nil {
		return nil, err
	}
	d, err := s.store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if d.Quarantined {
		return nil, ErrIdentityChanged
	}
	conn, err := s.connection(ctx, p.OrgID, d.ConnectionID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	physical, identityErr := verifiedIdentity(conn, now)
	previous := d.Generation
	d.Generation++
	d.UpdatedAt = now
	d.Capabilities = domain.CapabilitySet{}
	if identityErr == nil && physical != d.Identity {
		d.Quarantined = true
		identityErr = ErrIdentityChanged
	}
	if identityErr == nil {
		d.Capabilities = unverifiedCapabilities(physical, now)
	}
	if updateErr := s.store.Update(ctx, d, previous); updateErr != nil {
		return nil, updateErr
	}
	s.record(ctx, p, "federation.destination.check", d)
	if identityErr != nil {
		return nil, identityErr
	}
	return &d.Capabilities, nil
}

func (s *Service) connection(ctx context.Context, org, id string) (*connections.Connection, error) {
	c, err := s.connections.GetConnection(ctx, org, id)
	if errors.Is(err, connections.ErrNotFound) {
		return nil, ErrNotFound
	}
	return c, err
}

// Existing connection checks pin a Kubernetes namespace UID or Docker daemon ID.
// Other providers need a qualified adapter before they can become destinations.
func verifiedIdentity(c *connections.Connection, now time.Time) (domain.PhysicalIdentity, error) {
	if c == nil || c.CheckedAt == nil || c.CheckedAt.After(now) || !c.CheckedAt.Add(5*time.Minute).After(now) || c.Status != connections.StatusReady {
		return domain.PhysicalIdentity{}, ErrUnverified
	}
	p := domain.PhysicalIdentity{Provider: c.Cloud}
	switch c.Cloud {
	case connections.Kubernetes:
		p.Account = c.Details["cluster_ca"]
		p.NamespaceUID = c.Details["namespace_uid"]
		p.ResourceID = p.NamespaceUID
	case connections.Server:
		p.Account = c.Details["docker_id"]
		p.ResourceID = p.Account
	default:
		return p, ErrUnverified
	}
	if p.Revision() == "" {
		return p, ErrUnverified
	}
	return p, nil
}

func unverifiedCapabilities(p domain.PhysicalIdentity, now time.Time) domain.CapabilitySet {
	// A deterministic revision represents qualification content, not check time.
	return domain.CapabilitySet{Revision: "unqualified-v1", IdentityRevision: p.Revision(), CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute), Results: map[domain.Capability]domain.CapabilityResult{
		domain.Observe:             {State: domain.Unverified, Reason: "runtime observer is not qualified"},
		domain.StableDNS:           {State: domain.Unverified, Reason: "replacement discovery has not been qualified"},
		domain.WorkloadAttestation: {State: domain.Unverified, Reason: "workload attestation is not qualified"},
		domain.GatewayAttestation:  {State: domain.Unverified, Reason: "gateway identity and backend isolation are not qualified"},
		domain.ImportedNetwork:     {State: domain.Unverified, Reason: "no verified path observation"},
	}}
}

func (s *Service) record(ctx context.Context, p security.Principal, action string, d *domain.Destination) {
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: d.ProjectID, Principal: p, Attributes: map[string]any{"destination_id": d.ID, "generation": d.Generation, "quarantined": d.Quarantined}})
	}
}
