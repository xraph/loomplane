package fleets

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type ProjectFleetStore interface {
	GetFleetForProject(context.Context, string, string) (*fleet.Fleet, error)
}

// GetProjectFleet resolves a project's single fleet under current read authority.
func (s *Service) GetProjectFleet(ctx context.Context, p security.Principal, projectID string) (*fleet.Fleet, error) {
	if p.IsFleet() && (p.Fleet == nil || p.Fleet.ProjectID != projectID) {
		return nil, ErrNotFound
	}
	store, ok := s.store.(ProjectFleetStore)
	if !ok {
		return nil, ErrInvalid
	}
	f, err := store.GetFleetForProject(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	return s.GetFleet(ctx, p, f.ID)
}

func (s *PgStore) GetFleetForProject(ctx context.Context, org, project string) (*fleet.Fleet, error) {
	var f fleet.Fleet
	err := s.db.QueryRow(ctx, `SELECT id,org_id,project_id,coalesce(control_project_id,''),revision,status,created_at,updated_at FROM loomplane_fleets WHERE org_id=$1 AND project_id=$2`, org, project).Scan(&f.ID, &f.OrgID, &f.ProjectID, &f.ControlProjectID, &f.Revision, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	f.CreatedAt, f.UpdatedAt = f.CreatedAt.UTC(), f.UpdatedAt.UTC()
	return &f, nil
}

// GetFleetInstance checks the requested fleet before disclosing an instance.
func (s *Service) GetFleetInstance(ctx context.Context, p security.Principal, fleetID, id string) (*fleet.Instance, error) {
	f, err := s.GetFleet(ctx, p, fleetID)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(ReadStore)
	if !ok {
		return nil, ErrInvalid
	}
	instance, err := store.GetInstance(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if instance.FleetID != f.ID {
		return nil, ErrNotFound
	}
	return instance, nil
}

// GetFleetOperation keeps sibling operations hidden behind the selected fleet.
func (s *Service) GetFleetOperation(ctx context.Context, p security.Principal, fleetID, id string) (*fleet.Operation, error) {
	if _, err := s.GetFleet(ctx, p, fleetID); err != nil {
		return nil, err
	}
	store, ok := s.store.(ReadStore)
	if !ok {
		return nil, ErrInvalid
	}
	operation, err := store.GetOperation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if _, err = s.GetFleetInstance(ctx, p, fleetID, operation.InstanceID); err != nil {
		return nil, err
	}
	return operation, nil
}
