package federation

import (
	"context"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// AttestationResolver checks the destination's actual workload and parent node.
// No selector or parent identity is accepted from the management request.
type AttestationResolver func(context.Context, domain.Destination) (workloadidentity.Binding, map[string]runtimecatalog.ExportBounds, error)
type EnrollmentBackend interface {
	Reserve(context.Context, workloadidentity.Binding, map[string]runtimecatalog.ExportBounds) (*workloadidentity.EnrollmentRecord, error)
	Issue(context.Context, string, string, workloadidentity.EnrollmentIssuer) error
	Revoke(context.Context, string, string, workloadidentity.EnrollmentIssuer) error
}
type EnrollmentService struct {
	destinations *Service
	store        EnrollmentBackend
	resolve      AttestationResolver
	issuer       workloadidentity.EnrollmentIssuer
}

func NewEnrollmentService(destinations *Service, store EnrollmentBackend, resolve AttestationResolver, issuer workloadidentity.EnrollmentIssuer) *EnrollmentService {
	return &EnrollmentService{destinations: destinations, store: store, resolve: resolve, issuer: issuer}
}
func (s *EnrollmentService) Enroll(ctx context.Context, p security.Principal, request domain.EnrollmentRequest) (*domain.Enrollment, error) {
	if p.UserID == "" {
		return nil, security.ErrUnauthenticated
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	if stepErr := s.destinations.authorize(ctx, p, security.DestinationManage, request.DestinationID); stepErr != nil {
		return nil, stepErr
	}
	if stepErr := s.destinations.authorize(ctx, p, security.TrustManage, request.DestinationID); stepErr != nil {
		return nil, stepErr
	}
	d, err := s.destinations.verifiedDestination(ctx, p.OrgID, request.DestinationID)
	if err != nil {
		return nil, err
	}
	now := s.destinations.now()
	if !d.Capabilities.Supports(domain.WorkloadAttestation, d.Identity.Revision(), now) {
		return nil, ErrUnverified
	}
	if s.resolve == nil || s.store == nil {
		return nil, ErrUnverified
	}
	b, exports, err := s.resolve(ctx, *d)
	if err != nil {
		return nil, err
	}
	if b.Destination.ID != d.ID || b.Destination.Scope != d.Scope || b.Destination.Identity != d.Identity || b.Destination.Generation != d.Generation {
		return nil, ErrIdentityChanged
	}
	r, err := s.store.Reserve(ctx, b, exports)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "federation.enrollment.reserve", r)
	if err := s.store.Issue(ctx, p.OrgID, r.ID, s.issuer); err != nil {
		s.record(ctx, p, "federation.enrollment.issue_failed", r)
		return nil, err
	}
	s.record(ctx, p, "federation.enrollment.issue", r)
	return &domain.Enrollment{Metadata: domain.Metadata{Scope: d.Scope, ID: r.ID, Generation: 1}, DestinationID: d.ID, ExpiresAt: r.ExpiresAt, Agent: r.Binding.Agent, Identity: d.Identity, AttestationMethod: r.Binding.Method}, nil
}
func (s *EnrollmentService) record(ctx context.Context, p security.Principal, action string, r *workloadidentity.EnrollmentRecord) {
	if s.destinations.audit == nil {
		return
	}
	s.destinations.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: r.Binding.Agent.ProjectID, Principal: p, Attributes: map[string]any{"enrollment_id": r.ID, "destination_id": r.Binding.Agent.DestinationID, "agent_id": r.Binding.Agent.AgentID, "epoch": r.Binding.Agent.Epoch}})
}
