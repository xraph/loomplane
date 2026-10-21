package federation

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type LaunchEnrollmentRevoker func(context.Context, string, string) error

type LaunchRetirementService struct {
	launch  *LaunchService
	store   LaunchRetirementStore
	revoke  LaunchEnrollmentRevoker
	cleanup func(context.Context, domain.Destination, *LaunchAttempt, func(context.Context) error) error
}

func NewLaunchRetirementService(launch *LaunchService, revoke LaunchEnrollmentRevoker) *LaunchRetirementService {
	s := &LaunchRetirementService{launch: launch, revoke: revoke}
	if launch != nil {
		if store, ok := launch.attempts.(LaunchRetirementStore); ok {
			s.store = store
		}
	}
	s.cleanup = s.cleanupKubernetes
	return s
}

// Retire persists its intent before revoking authority. Each retry checks current
// human permission and the saved physical identity; it never issues enrollment.
func (s *LaunchRetirementService) Retire(ctx context.Context, p security.Principal, destinationID, id string) (*LaunchAttempt, error) {
	if s == nil || s.launch == nil || s.launch.installations == nil || s.store == nil || s.revoke == nil || s.cleanup == nil {
		return nil, ErrDisabled
	}
	if p.OrgID != s.launch.org {
		return nil, security.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.launch.installations.authorize(ctx, p, destinationID, true); err != nil {
		return nil, err
	}
	attempt, err := s.store.GetLaunch(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if attempt.DestinationID != destinationID {
		return nil, security.ErrForbidden
	}
	authorize := func(ctx context.Context) (*domain.Destination, error) {
		if checkErr := s.launch.installations.authorize(ctx, p, destinationID, true); checkErr != nil {
			return nil, checkErr
		}
		d, checkErr := s.launch.installations.destinations.verifiedDestination(ctx, p.OrgID, destinationID)
		if checkErr != nil {
			return nil, checkErr
		}
		// A newer installation may fence this one. It must not prevent cleanup of
		// the old UID on the same verified cluster and namespace.
		if d.Scope != attempt.Scope || d.Identity.Revision() != attempt.IdentityRevision {
			return nil, ErrIdentityChanged
		}
		return d, nil
	}
	d, err := authorize(ctx)
	if err != nil {
		return nil, err
	}
	if attempt.State == LaunchRetired {
		return attempt, nil
	}
	guard := func(ctx context.Context) error { _, checkErr := authorize(ctx); return checkErr }
	if attempt.State != LaunchRetiring {
		attempt, err = s.store.BeginLaunchRetirement(ctx, p.OrgID, id, attempt.Generation, p.UserID, guard)
		if err != nil {
			return nil, err
		}
		s.launch.recordLaunch(ctx, p, "federation.launch.retiring", attempt)
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if revokeErr := s.revoke(ctx, p.OrgID, attempt.EnrollmentID); revokeErr != nil {
		return nil, revokeErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if cleanupErr := s.cleanup(ctx, *d, attempt, guard); cleanupErr != nil {
		return nil, cleanupErr
	}
	retired, err := s.store.CompleteLaunchRetirement(ctx, p.OrgID, id, attempt.Generation, p.UserID, guard)
	if err != nil {
		return nil, err
	}
	s.launch.recordLaunch(ctx, p, "federation.launch.retired", retired)
	return retired, nil
}
