package federation

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ApplicationTarget names an existing pod. Live controller ownership decides
// whether it belongs to the binding; callers cannot choose SPIRE selectors.
type ApplicationTarget struct{ BindingID, PodName, PodUID string }
type ApplicationResolver func(context.Context, domain.Destination, ApplicationTarget) (workloadidentity.WorkloadIntent, error)
type ApplicationBackend interface {
	Reserve(context.Context, workloadidentity.WorkloadIntent, func(context.Context) error) (*workloadidentity.WorkloadRecord, error)
	Get(context.Context, string, string) (*workloadidentity.WorkloadRecord, error)
	Issue(context.Context, string, string, workloadidentity.WorkloadIssuer, func(context.Context) error) error
	Revoke(context.Context, string, string, workloadidentity.WorkloadRevoker) error
}
type ApplicationAuthority interface {
	workloadidentity.WorkloadIssuer
	workloadidentity.WorkloadRevoker
}

// ApplicationService connects fresh deployment evidence to the durable issuance
// journal. Reservation and issuance are separate so interrupted operations retain
// a handle for revocation. It installs no cluster resources or service grants.
type ApplicationService struct {
	destinations *Service
	store        ApplicationBackend
	resolve      ApplicationResolver
	authority    ApplicationAuthority
}

func NewApplicationService(destinations *Service, store ApplicationBackend, resolve ApplicationResolver, authority ApplicationAuthority) *ApplicationService {
	return &ApplicationService{destinations: destinations, store: store, resolve: resolve, authority: authority}
}
func (s *ApplicationService) principal(ctx context.Context, p security.Principal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.destinations == nil || s.store == nil || s.resolve == nil || s.authority == nil {
		return ErrDisabled
	}
	if p.OrgID == "" || p.UserID == "" {
		return security.ErrUnauthenticated
	}
	if p.ReadOnly {
		return security.ErrForbidden
	}
	return nil
}
func (s *ApplicationService) authorize(ctx context.Context, p security.Principal, destination string) error {
	if s == nil || s.destinations == nil || s.store == nil || s.resolve == nil || s.authority == nil {
		return ErrDisabled
	}
	return s.destinations.authorizeTrustChange(ctx, p, destination)
}
func (s *ApplicationService) destination(ctx context.Context, p security.Principal, id string) (*domain.Destination, error) {
	if err := s.authorize(ctx, p, id); err != nil {
		return nil, err
	}
	d, err := s.destinations.verifiedDestination(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if err := s.destinations.authorize(ctx, p, security.DeploymentApprove, d.ProjectID); err != nil {
		return nil, err
	}
	if !d.Capabilities.Supports(domain.WorkloadAttestation, d.Identity.Revision(), s.destinations.now()) {
		return nil, ErrUnverified
	}
	return d, nil
}
func (s *ApplicationService) evidence(ctx context.Context, d domain.Destination, target ApplicationTarget) (workloadidentity.WorkloadIntent, error) {
	denied := workloadidentity.WorkloadIntent{}
	if !domain.ValidIdentitySegment(target.BindingID) || !domain.ValidIdentitySegment(target.PodUID) || len(validation.IsDNS1123Subdomain(target.PodName)) != 0 {
		return denied, ErrInvalid
	}
	in, err := s.resolve(ctx, d, target)
	if err != nil {
		return denied, err
	}
	b := in.Workload.Binding
	if in.BindingID != target.BindingID || in.PodName != target.PodName || b.PodUID != target.PodUID || b.EntryID != "" || b.Method != "kubernetes" || b.Agent != (domain.AgentIdentity{}) || in.Controller.Scope != d.Scope || in.Controller.ConnectionID != d.ConnectionID || in.Workload.Workload.Scope != d.Scope || in.Workload.Workload.DestinationID != d.ID || in.Workload.Workload.ServiceID != in.Controller.ServiceID || !sameApplicationDestination(b.Destination, d) || b.CheckedAt.IsZero() || b.CheckedAt.Before(s.destinations.now().Add(-time.Minute)) || b.CheckedAt.After(s.destinations.now().Add(30*time.Second)) {
		return denied, ErrIdentityChanged
	}
	return in, nil
}
func sameApplicationDestination(a, b domain.Destination) bool {
	return a.Scope == b.Scope && a.ID == b.ID && a.ConnectionID == b.ConnectionID && a.Identity == b.Identity && a.Generation == b.Generation && !a.Quarantined && !b.Quarantined
}
func (s *ApplicationService) guard(p security.Principal, expected workloadidentity.WorkloadIntent) func(context.Context) error {
	return func(ctx context.Context) error {
		d, err := s.destination(ctx, p, expected.Workload.Binding.Destination.ID)
		if err != nil {
			return err
		}
		if !sameApplicationDestination(*d, expected.Workload.Binding.Destination) {
			return ErrIdentityChanged
		}
		current, err := s.evidence(ctx, *d, ApplicationTarget{BindingID: expected.BindingID, PodName: expected.PodName, PodUID: expected.Workload.Binding.PodUID})
		if err != nil {
			return err
		}
		// Fresh timestamps and a renewed capability observation do not change the
		// workload identity. Every controller, parent, image and selector pin must match.
		if !expected.Workload.PruneAfter.After(s.destinations.now()) || !slices.EqualFunc(current.ImageIndexes, expected.ImageIndexes, bytes.Equal) {
			return ErrIdentityChanged
		}
		current.ImageIndexes = expected.ImageIndexes
		current.Workload.Binding.Destination = expected.Workload.Binding.Destination
		current.Workload.Binding.CheckedAt = expected.Workload.Binding.CheckedAt
		current.Workload.Binding.EntryID = expected.Workload.Binding.EntryID
		current.Workload.Binding.TTL = expected.Workload.Binding.TTL
		current.Workload.PruneAfter = expected.Workload.PruneAfter
		if !reflect.DeepEqual(current, expected) {
			return ErrIdentityChanged
		}
		return s.authorize(ctx, p, d.ID)
	}
}
func (s *ApplicationService) Reserve(ctx context.Context, p security.Principal, destination string, target ApplicationTarget) (*workloadidentity.WorkloadRecord, error) {
	if err := s.principal(ctx, p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	d, err := s.destination(ctx, p, destination)
	if err != nil {
		return nil, err
	}
	in, err := s.evidence(ctx, *d, target)
	if err != nil {
		return nil, err
	}
	in.Workload.Binding.TTL = time.Minute
	in.Workload.PruneAfter = s.destinations.now().Add(4 * time.Minute).UTC().Truncate(time.Second)
	record, err := s.store.Reserve(ctx, in, s.guard(p, in))
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "federation.application.reserve", record)
	return record, nil
}
func (s *ApplicationService) Issue(ctx context.Context, p security.Principal, id string) (*workloadidentity.WorkloadRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.principal(ctx, p); err != nil {
		return nil, err
	}
	record, err := s.store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if authErr := s.authorize(ctx, p, record.Intent.Workload.Binding.Destination.ID); authErr != nil {
		return nil, authErr
	}
	guard := s.guard(p, record.Intent)
	err = s.store.Issue(ctx, p.OrgID, id, s.authority, guard)
	if err == nil {
		record, err = s.store.Get(ctx, p.OrgID, id)
	}
	if err == nil && (record.Phase != "ready" || !record.Settled) {
		err = ErrConflict
	}
	if err == nil {
		err = guard(ctx)
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return nil, errors.Join(err, s.store.Revoke(cleanup, p.OrgID, id, s.authority))
	}
	s.record(ctx, p, "federation.application.issue", record)
	return record, nil
}
func (s *ApplicationService) Revoke(ctx context.Context, p security.Principal, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.principal(ctx, p); err != nil {
		return err
	}
	record, err := s.store.Get(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, p, record.Intent.Workload.Binding.Destination.ID); err != nil {
		return err
	}
	if err := s.store.Revoke(ctx, p.OrgID, id, s.authority); err != nil {
		return err
	}
	s.record(ctx, p, "federation.application.revoke", record)
	return nil
}
func (s *ApplicationService) record(ctx context.Context, p security.Principal, action string, r *workloadidentity.WorkloadRecord) {
	if s.destinations.audit != nil {
		s.destinations.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: r.Intent.Controller.ProjectID, Principal: p, Attributes: map[string]any{"registration_id": r.ID, "binding_id": r.Intent.BindingID, "destination_id": r.Intent.Workload.Binding.Destination.ID}})
	}
}
