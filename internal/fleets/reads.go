package fleets

import (
	"context"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// ReadStore loads only public records. Authority and provider receipts stay internal.
type ReadStore interface {
	GetInstance(context.Context, string, string) (*fleet.Instance, error)
	GetOperation(context.Context, string, string) (*fleet.Operation, error)
	ListInstances(context.Context, FleetScope, fleet.ListInstancesInput) (*fleet.InstancePage, error)
}

// GetFleet checks current permissions even for previously visible identities.
func (s *Service) GetFleet(ctx context.Context, p security.Principal, id string) (*fleet.Fleet, error) {
	if p.OrgID == "" {
		return nil, security.ErrNoOrganization
	}
	if p.IsFleet() && (p.Fleet == nil || p.Fleet.FleetID != id) {
		return nil, ErrNotFound
	}
	f, err := s.store.GetFleet(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if p.IsFleet() {
		err = s.credentialScope(ctx, p, FleetScope{OrgID: f.OrgID, ProjectID: f.ProjectID, FleetID: f.ID})
	} else {
		err = s.authz.Require(ctx, p, security.ProjectRead, f.ProjectID)
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

// GetInstance resolves ownership from storage before checking its fleet scope.
func (s *Service) GetInstance(ctx context.Context, p security.Principal, id string) (*fleet.Instance, error) {
	store, ok := s.store.(ReadStore)
	if !ok {
		return nil, ErrInvalid
	}
	i, err := store.GetInstance(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if _, err = s.GetFleet(ctx, p, i.FleetID); err != nil {
		return nil, err
	}
	return i, nil
}

// GetOperation preserves recovery visibility without disclosing execution credentials.
func (s *Service) GetOperation(ctx context.Context, p security.Principal, id string) (*fleet.Operation, error) {
	store, ok := s.store.(ReadStore)
	if !ok {
		return nil, ErrInvalid
	}
	op, err := store.GetOperation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if _, err = s.GetInstance(ctx, p, op.InstanceID); err != nil {
		return nil, err
	}
	return op, nil
}

func instancePageInput(in fleet.ListInstancesInput) (fleet.ListInstancesInput, error) {
	if in.Limit < 0 || in.Limit > 200 || len(in.Status) > 64 || len(in.ExternalRef) > 128 {
		return in, ErrInvalid
	}
	if in.After != "" {
		if _, err := uuid.Parse(in.After); err != nil {
			return in, ErrInvalid
		}
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	return in, nil
}

// ListInstances applies filters inside the authorized fleet before pagination.
func (s *Service) ListInstances(ctx context.Context, p security.Principal, id string, in fleet.ListInstancesInput) (*fleet.InstancePage, error) {
	f, err := s.GetFleet(ctx, p, id)
	if err != nil {
		return nil, err
	}
	in, err = instancePageInput(in)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(ReadStore)
	if !ok {
		return nil, ErrInvalid
	}
	return store.ListInstances(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, in)
}

// GetRelease reads an immutable template within the caller's current scope.
func (s *Service) GetRelease(ctx context.Context, p security.Principal, fleetID, id string) (*fleet.TemplateRelease, error) {
	f, err := s.GetFleet(ctx, p, fleetID)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(ReleaseStore)
	if !ok {
		return nil, ErrInvalid
	}
	return store.GetRelease(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, id)
}
