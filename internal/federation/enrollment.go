package federation

import (
	"context"

	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"

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

type PreparedEnrollmentBackend interface {
	ReservePrepared(context.Context, workloadidentity.Binding, map[string]runtimecatalog.ExportBounds, string, string, func(context.Context) error) (*workloadidentity.EnrollmentRecord, error)
}
type enrollmentReservation func(context.Context, workloadidentity.Binding, map[string]runtimecatalog.ExportBounds, func(context.Context) error) (*workloadidentity.EnrollmentRecord, error)

func (s *EnrollmentService) Enroll(ctx context.Context, p security.Principal, request domain.EnrollmentRequest) (*domain.Enrollment, error) {
	return s.enroll(ctx, p, request, func(ctx context.Context, b workloadidentity.Binding, exports map[string]runtimecatalog.ExportBounds, _ func(context.Context) error) (*workloadidentity.EnrollmentRecord, error) {
		return s.store.Reserve(ctx, b, exports)
	})
}

// EnrollPreparation reserves through a durable receipt and reuses the same
// attestation, qualification and permission checks as explicit enrollment.
func (s *EnrollmentService) EnrollPreparation(ctx context.Context, p security.Principal, destinationID, preparationID string) (*domain.Enrollment, error) {
	if s == nil || !domain.ValidIdentitySegment(preparationID) {
		return nil, ErrInvalid
	}
	store, ok := s.store.(PreparedEnrollmentBackend)
	if !ok {
		return nil, ErrDisabled
	}
	return s.enroll(ctx, p, domain.EnrollmentRequest{DestinationID: destinationID}, func(ctx context.Context, b workloadidentity.Binding, exports map[string]runtimecatalog.ExportBounds, guard func(context.Context) error) (*workloadidentity.EnrollmentRecord, error) {
		return store.ReservePrepared(ctx, b, exports, preparationID, p.UserID, guard)
	})
}
func (s *EnrollmentService) enroll(ctx context.Context, p security.Principal, request domain.EnrollmentRequest, reserve enrollmentReservation) (*domain.Enrollment, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	if err := s.destinations.authorizeTrustChange(ctx, p, request.DestinationID); err != nil {
		return nil, err
	}
	d, err := s.destinations.verifiedDestination(ctx, p.OrgID, request.DestinationID)
	if err != nil {
		return nil, err
	}
	now := s.destinations.now()
	if !d.Capabilities.Supports(domain.WorkloadAttestation, d.Identity.Revision(), now) {
		return nil, ErrUnverified
	}
	if s.resolve == nil || s.store == nil || s.issuer == nil {
		return nil, ErrUnverified
	}
	b, exports, err := s.resolve(ctx, *d)
	if err != nil {
		return nil, err
	}
	if b.Destination.ID != d.ID || b.Destination.Scope != d.Scope || b.Destination.Identity != d.Identity || b.Destination.Generation != d.Generation {
		return nil, ErrIdentityChanged
	}
	guard := func(ctx context.Context) error {
		if checkErr := s.destinations.authorizeTrustChange(ctx, p, d.ID); checkErr != nil {
			return checkErr
		}
		current, checkErr := s.destinations.verifiedDestination(ctx, p.OrgID, d.ID)
		if checkErr != nil {
			return checkErr
		}
		if current.Scope != d.Scope || current.Identity != d.Identity || current.Generation != d.Generation || current.ConnectionID != d.ConnectionID {
			return ErrIdentityChanged
		}
		if !current.Capabilities.Supports(domain.WorkloadAttestation, current.Identity.Revision(), s.destinations.now()) {
			return ErrUnverified
		}
		return nil
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	r, err := reserve(ctx, b, exports, guard)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "federation.enrollment.reserve", r)
	if err := s.store.Issue(ctx, p.OrgID, r.ID, guardedEnrollmentIssuer{EnrollmentIssuer: s.issuer, guard: guard}); err != nil {
		s.record(ctx, p, "federation.enrollment.issue_failed", r)
		return nil, err
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
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

// Failed post-issuance authorization leaves the durable pending record available
// for cleanup. The handle never becomes ready for runtime enrollment.
type guardedEnrollmentIssuer struct {
	workloadidentity.EnrollmentIssuer
	guard func(context.Context) error
}

func (i guardedEnrollmentIssuer) Register(ctx context.Context, b workloadidentity.Binding) (*spiretypes.Entry, error) {
	if err := i.guard(ctx); err != nil {
		return nil, err
	}
	entry, err := i.EnrollmentIssuer.Register(ctx, b)
	if err != nil {
		return nil, err
	}
	if err := i.guard(ctx); err != nil {
		return nil, err
	}
	return entry, nil
}
