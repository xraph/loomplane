package federation

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type PreparationEnrollmentRevoker func(context.Context, string, string, string) error

type PreparationRetirementService struct {
	preparation *PreparationService
	launch      *LaunchRetirementService
	store       PreparationRetirementStore
	revoke      PreparationEnrollmentRevoker
}

func NewPreparationRetirementService(preparation *PreparationService, launch *LaunchRetirementService, revoke PreparationEnrollmentRevoker) *PreparationRetirementService {
	s := &PreparationRetirementService{preparation: preparation, launch: launch, revoke: revoke}
	if preparation != nil {
		if store, ok := preparation.store.(PreparationRetirementStore); ok {
			s.store = store
		}
	}
	return s
}

// Retire fences enrollment before touching Kubernetes and retains its last saved
// state on every failure. Retries use recorded identities and never issue trust.
func (s *PreparationRetirementService) Retire(ctx context.Context, p security.Principal, destination, id string) (*Preparation, error) {
	if s == nil || s.preparation == nil || s.preparation.launch == nil || s.preparation.launch.installations == nil || s.preparation.open == nil || s.launch == nil || s.launch.store == nil || s.launch.launch != s.preparation.launch || s.store == nil || s.revoke == nil {
		return nil, ErrDisabled
	}
	if p.OrgID != s.preparation.launch.org {
		return nil, security.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	installations := s.preparation.launch.installations
	if err := installations.authorize(ctx, p, destination, true); err != nil {
		return nil, err
	}
	r, err := s.store.GetPreparation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if r.DestinationID != destination {
		return nil, security.ErrForbidden
	}
	authorize := func(ctx context.Context) (*domain.Destination, error) {
		if checkErr := installations.authorize(ctx, p, destination, true); checkErr != nil {
			return nil, checkErr
		}
		d, checkErr := installations.destinations.verifiedDestination(ctx, p.OrgID, destination)
		if checkErr != nil {
			return nil, checkErr
		}
		if d.Scope != r.Scope || d.Identity.Revision() != r.IdentityRevision || d.Identity.NamespaceUID != r.NamespaceUID {
			return nil, ErrIdentityChanged
		}
		return d, nil
	}
	if _, err = authorize(ctx); err != nil {
		return r, err
	}
	if r.State == PreparationRetired {
		return r, nil
	}
	guard := func(ctx context.Context) error {
		if _, checkErr := authorize(ctx); checkErr != nil {
			return checkErr
		}
		current, checkErr := s.store.GetPreparation(ctx, p.OrgID, id)
		if checkErr != nil {
			return checkErr
		}
		if current.Generation != r.Generation || current.State != r.State || current.PreparationIntent != r.PreparationIntent || current.AccountUID != r.AccountUID || current.PodUID != r.PodUID {
			return ErrConflict
		}
		return nil
	}
	if r.State != PreparationRetiring {
		next, stepErr := s.store.BeginPreparationRetirement(ctx, p.OrgID, id, r.Generation, p.UserID, guard)
		if stepErr != nil {
			return r, stepErr
		}
		r = next
		s.preparation.record(ctx, p, r)
	}
	if checkErr := guard(ctx); checkErr != nil {
		return r, checkErr
	}
	if revokeErr := s.revoke(ctx, p.OrgID, destination, id); revokeErr != nil {
		return r, revokeErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return r, checkErr
	}
	launch, readErr := s.launch.store.GetLaunch(ctx, p.OrgID, id)
	if readErr != nil && !errors.Is(readErr, ErrNotFound) {
		return r, readErr
	}
	if launch != nil {
		if launch.Scope != r.Scope || launch.DestinationID != destination || launch.IdentityRevision != r.IdentityRevision || launch.PodUID != r.PodUID || launch.Namespace != r.Namespace || launch.ConfigMapName != "loomplane-agent-"+r.ID {
			return r, ErrIdentityChanged
		}
		if _, retireErr := s.launch.Retire(ctx, p, destination, id); retireErr != nil {
			return r, retireErr
		}
	}
	d, err := authorize(ctx)
	if err != nil {
		return r, err
	}
	client, namespace, err := s.preparation.open(ctx, *d)
	if err != nil {
		return r, err
	}
	if namespace != r.Namespace {
		return r, ErrIdentityChanged
	}
	capture := func(ctx context.Context, account, pod string) (*Preparation, error) {
		next, saveErr := s.store.CapturePreparationOwnership(ctx, p.OrgID, id, r.Generation, account, pod, p.UserID, guard)
		if saveErr != nil {
			return nil, saveErr
		}
		r = next
		s.preparation.record(ctx, p, r)
		return r, nil
	}
	r, err = cleanupPreparationResources(ctx, client, r, capture, guard)
	if err != nil {
		return r, err
	}
	retired, err := s.store.CompletePreparationRetirement(ctx, p.OrgID, id, r.Generation, p.UserID, guard)
	if err != nil {
		return r, err
	}
	s.preparation.record(ctx, p, retired)
	return retired, nil
}
