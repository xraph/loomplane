package federation

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// AccessBackend commits policy changes with a caller-supplied authorization check.
type AccessBackend interface {
	GetExport(context.Context, string, string) (*access.ExportRecord, error)
	GetGrant(context.Context, string, string) (*access.GrantRecord, error)
	PutExport(context.Context, domain.ServiceExport, int64, string, access.DecisionGuard) (*access.ExportRecord, error)
	ProposeGrant(context.Context, domain.AccessGrant, int64, string, access.DecisionGuard) (*access.GrantRecord, error)
	ApproveGrant(context.Context, string, string, int64, string, string, access.DecisionGuard) (*access.GrantRecord, error)
	RevokeGrant(context.Context, string, string, int64, string, access.DecisionGuard) (*access.GrantRecord, error)
	RetireExport(context.Context, string, string, int64, string, access.DecisionGuard) (*access.ExportRecord, error)
}

// AccessService derives ownership and audit actors from the authenticated principal.
type AccessService struct {
	destinations *Service
	store        AccessBackend
}

func NewAccessService(destinations *Service, store AccessBackend) *AccessService {
	return &AccessService{destinations: destinations, store: store}
}

type ExportInput struct {
	ProjectID       string                 `json:"project_id"`
	EnvironmentID   string                 `json:"environment_id"`
	ServiceID       string                 `json:"service_id"`
	DestinationIDs  []string               `json:"destination_ids"`
	Port            uint16                 `json:"port"`
	Protocol        domain.Protocol        `json:"protocol"`
	IdentityProfile domain.IdentityProfile `json:"identity_profile"`
}

type GrantInput struct {
	Source         domain.WorkloadSelector `json:"source"`
	ExportID       string                  `json:"export_id"`
	ExportRevision string                  `json:"export_revision"`
	Port           uint16                  `json:"port"`
	Protocol       domain.Protocol         `json:"protocol"`
	HTTPMethods    []string                `json:"http_methods,omitempty"`
	HTTPPaths      []string                `json:"http_paths,omitempty"`
	GRPCMethods    []string                `json:"grpc_methods,omitempty"`
	ExpiresAt      *time.Time              `json:"expires_at,omitempty"`
}

func (s *AccessService) authorize(ctx context.Context, p security.Principal, perm security.Permission, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.UserID == "" || p.OrgID == "" {
		return security.ErrUnauthenticated
	}
	if p.ReadOnly && perm != security.NetworkRead {
		return security.ErrForbidden
	}
	if s == nil || s.destinations == nil || s.destinations.authz == nil || s.store == nil {
		return ErrDisabled
	}
	return s.destinations.authorize(ctx, p, perm, id)
}

func (s *AccessService) guard(p security.Principal, perm security.Permission, id string, validate func(context.Context) error) access.DecisionGuard {
	return func(ctx context.Context) error {
		if err := s.authorize(ctx, p, perm, id); err != nil {
			return err
		}
		if validate != nil {
			return validate(ctx)
		}
		return nil
	}
}

func (s *AccessService) GetExport(ctx context.Context, p security.Principal, id string) (*access.ExportRecord, error) {
	if err := s.authorize(ctx, p, security.NetworkRead, id); err != nil {
		return nil, err
	}
	return s.store.GetExport(ctx, p.OrgID, id)
}
func (s *AccessService) GetGrant(ctx context.Context, p security.Principal, id string) (*access.GrantRecord, error) {
	if err := s.authorize(ctx, p, security.NetworkRead, id); err != nil {
		return nil, err
	}
	return s.store.GetGrant(ctx, p.OrgID, id)
}

func (s *AccessService) CreateExport(ctx context.Context, p security.Principal, in ExportInput) (*access.ExportRecord, error) {
	if err := s.authorize(ctx, p, security.ExportManage, in.ProjectID); err != nil {
		return nil, err
	}
	id, err := typeid.Generate("sexp")
	if err != nil {
		return nil, err
	}
	export := domain.ServiceExport{Metadata: domain.Metadata{Scope: domain.Scope{OrgID: p.OrgID, ProjectID: in.ProjectID, EnvironmentID: in.EnvironmentID}, ID: id.String(), Generation: 1}, ServiceID: in.ServiceID, DestinationIDs: slices.Clone(in.DestinationIDs), Port: in.Port, Protocol: in.Protocol, IdentityProfile: in.IdentityProfile}
	if _, checkErr := access.ExportRevision(export); checkErr != nil {
		return nil, checkErr
	}
	guard := s.guard(p, security.ExportManage, in.ProjectID, func(ctx context.Context) error { return s.validateExport(ctx, export, false) })
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	result, err := s.store.PutExport(ctx, export, 0, p.UserID, guard)
	if err == nil {
		s.record(ctx, p, "federation.export.create", result.Metadata, result.Revision)
	}
	return result, err
}

func (s *AccessService) ProposeGrant(ctx context.Context, p security.Principal, in GrantInput) (*access.GrantRecord, error) {
	if err := s.authorize(ctx, p, security.GrantPropose, in.ExportID); err != nil {
		return nil, err
	}
	export, err := s.store.GetExport(ctx, p.OrgID, in.ExportID)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("grant")
	if err != nil {
		return nil, err
	}
	in.Source.OrgID = p.OrgID
	grant := domain.AccessGrant{Metadata: domain.Metadata{Scope: export.Scope, ID: id.String(), Generation: 1}, Source: in.Source, ExportID: in.ExportID, ExportRevision: in.ExportRevision, Port: in.Port, Protocol: in.Protocol, HTTPMethods: slices.Clone(in.HTTPMethods), HTTPPaths: slices.Clone(in.HTTPPaths), GRPCMethods: slices.Clone(in.GRPCMethods), OwnerID: p.UserID}
	if in.ExpiresAt != nil {
		expiry := *in.ExpiresAt
		grant.ExpiresAt = &expiry
	}
	if _, checkErr := access.GrantRevision(grant); checkErr != nil {
		return nil, checkErr
	}
	guard := s.guard(p, security.GrantPropose, in.ExportID, func(ctx context.Context) error { return s.validateGrant(ctx, grant, export, false) })
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	result, err := s.store.ProposeGrant(ctx, grant, 0, p.UserID, guard)
	if err == nil {
		s.record(ctx, p, "federation.grant.propose", result.Metadata, result.Revision)
	}
	return result, err
}

func (s *AccessService) ApproveGrant(ctx context.Context, p security.Principal, id string, expected int64, reviewed string) (*access.GrantRecord, error) {
	if err := s.authorize(ctx, p, security.GrantApprove, id); err != nil {
		return nil, err
	}
	grant, err := s.store.GetGrant(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if expected < 1 || grant.Generation != expected || reviewed == "" || grant.Revision != reviewed || grant.ApprovedRevision != "" || grant.RevokedAt != nil {
		return nil, access.ErrPolicyConflict
	}
	export, err := s.store.GetExport(ctx, p.OrgID, grant.ExportID)
	if err != nil {
		return nil, err
	}
	guard := s.guard(p, security.GrantApprove, id, func(ctx context.Context) error { return s.validateGrant(ctx, grant.AccessGrant, export, true) })
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	result, err := s.store.ApproveGrant(ctx, p.OrgID, id, expected, reviewed, p.UserID, guard)
	if err == nil {
		s.record(ctx, p, "federation.grant.approve", result.Metadata, result.Revision)
	}
	return result, err
}

func (s *AccessService) RevokeGrant(ctx context.Context, p security.Principal, id string, expected int64) (*access.GrantRecord, error) {
	guard := s.guard(p, security.GrantRevoke, id, nil)
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	result, err := s.store.RevokeGrant(ctx, p.OrgID, id, expected, p.UserID, guard)
	if err == nil {
		s.record(ctx, p, "federation.grant.revoke", result.Metadata, result.Revision)
	}
	return result, err
}
func (s *AccessService) RetireExport(ctx context.Context, p security.Principal, id string, expected int64) (*access.ExportRecord, error) {
	guard := s.guard(p, security.ExportManage, id, nil)
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	result, err := s.store.RetireExport(ctx, p.OrgID, id, expected, p.UserID, guard)
	if err == nil {
		s.record(ctx, p, "federation.export.retire", result.Metadata, result.Revision)
	}
	return result, err
}

func (s *AccessService) validateGrant(ctx context.Context, grant domain.AccessGrant, export *access.ExportRecord, qualified bool) error {
	if export.RetiredAt != nil || export.Scope != grant.Scope || export.Revision != grant.ExportRevision || export.Port != grant.Port || export.Protocol != grant.Protocol {
		return access.ErrPolicyConflict
	}
	if grant.Source.OrgID != grant.OrgID {
		return access.ErrPolicyNotFound
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(s.destinations.now()) {
		return access.ErrPolicyConflict
	}
	if err := s.validateExport(ctx, export.ServiceExport, qualified); err != nil {
		return err
	}
	if err := s.environment(ctx, grant.Source.Scope); err != nil {
		return err
	}
	return s.destination(ctx, grant.Source.Scope, grant.Source.DestinationID, export.IdentityProfile, grant.Protocol, qualified, false)
}
func (s *AccessService) validateExport(ctx context.Context, export domain.ServiceExport, qualified bool) error {
	if err := s.environment(ctx, export.Scope); err != nil {
		return err
	}
	for _, id := range export.DestinationIDs {
		if err := s.destination(ctx, export.Scope, id, export.IdentityProfile, export.Protocol, qualified, true); err != nil {
			return err
		}
	}
	return nil
}
func (s *AccessService) environment(ctx context.Context, scope domain.Scope) error {
	if s.destinations.environments == nil {
		return ErrUnverified
	}
	env, err := s.destinations.environments.GetEnvironment(ctx, scope.OrgID, scope.ProjectID, scope.EnvironmentID)
	if errors.Is(err, projects.ErrEnvironmentNotFound) || errors.Is(err, projects.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if env == nil || env.ID != scope.EnvironmentID || env.ProjectID != scope.ProjectID {
		return ErrNotFound
	}
	return nil
}
func (s *AccessService) destination(ctx context.Context, scope domain.Scope, id string, profile domain.IdentityProfile, protocol domain.Protocol, qualified, target bool) error {
	d, err := s.destinations.verifiedDestination(ctx, scope.OrgID, id)
	if err != nil {
		return err
	}
	if d.Scope != scope {
		return ErrNotFound
	}
	if !qualified {
		return nil
	}
	identity := domain.WorkloadAttestation
	if profile == domain.GatewayIdentity {
		identity = domain.GatewayAttestation
	}
	protocolCapability := map[domain.Protocol]domain.Capability{domain.HTTP: domain.HTTPProtocol, domain.GRPC: domain.GRPCProtocol, domain.TCP: domain.TCPProtocol}[protocol]
	required := []domain.Capability{identity, protocolCapability, domain.PolicyEnforcement}
	if target {
		required = append(required, domain.BackendIsolation)
	}
	for _, capability := range required {
		if !d.Capabilities.Supports(capability, d.Identity.Revision(), s.destinations.now()) {
			return ErrUnverified
		}
	}
	return nil
}
func (s *AccessService) record(ctx context.Context, p security.Principal, action string, metadata domain.Metadata, revision string) {
	if s.destinations.audit != nil {
		s.destinations.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: metadata.ProjectID, Principal: p, Attributes: map[string]any{"resource_id": metadata.ID, "generation": metadata.Generation, "revision": revision}})
	}
}
