package fleets

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// OperationStore accepts the original request and dispatch intent in one transaction.
type OperationStore interface {
	ReserveCreate(context.Context, FleetScope, security.Principal, fleet.CreateInstanceInput) (*fleet.OperationResult, error)
}

// CreateIntent is internal recovery input, never a public API response.
type CreateIntent struct {
	Principal security.Principal        `json:"principal"`
	Input     fleet.CreateInstanceInput `json:"input"`
	Snapshot  InstanceSnapshot          `json:"snapshot"`
	Deadline  time.Time                 `json:"deadline"`
}

// CreateInstance accepts a bounded request for review. It performs no provider work.
func (s *Service) CreateInstance(ctx context.Context, p security.Principal, fleetID string, in fleet.CreateInstanceInput) (*fleet.OperationResult, error) {
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	f, err := s.GetFleet(ctx, p, fleetID)
	if err != nil {
		return nil, err
	}
	if !p.IsFleet() {
		if checkErr := s.authz.Require(ctx, p, security.DeploymentCreate, f.ProjectID); checkErr != nil {
			return nil, checkErr
		}
	}
	if _, err = reservationHash(in); err != nil {
		return nil, err
	}
	scope := FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}
	releases, ok := s.store.(ReleaseStore)
	if !ok {
		return nil, ErrInvalid
	}
	release, err := releases.GetRelease(ctx, scope, in.ReleaseID)
	if err != nil {
		return nil, err
	}
	// Validate the template input before quota reservation. The transaction renders
	// again with its actual instance and environment identities.
	candidate := fleet.Instance{ID: "validation", EnvironmentID: "validation", FleetID: f.ID, StageID: in.StageID, ProfileID: in.ProfileID, DesiredReleaseID: in.ReleaseID, DesiredGeneration: 1, OwnershipEpoch: 1}
	if _, err = RenderInstance(*release, candidate, in); err != nil {
		return nil, err
	}
	store, ok := s.store.(OperationStore)
	if !ok {
		return nil, ErrInvalid
	}
	result, err := store.ReserveCreate(ctx, scope, p, in)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, f.ProjectID, "fleet.instance.create", result.Instance.ID)
	return result, nil
}
