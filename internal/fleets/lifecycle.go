package fleets

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type ActionStore interface {
	ReserveAction(context.Context, FleetScope, security.Principal, string, fleet.ActionInput) (*fleet.Operation, error)
}

// ActionIntent preserves the original actor and desired snapshot for review.
// Acceptance does not change live resources or delegate execution authority.
type ActionIntent struct {
	Principal      security.Principal `json:"principal"`
	Input          fleet.ActionInput  `json:"input"`
	Snapshot       InstanceSnapshot   `json:"snapshot"`
	PreviousStatus string             `json:"previous_status"`
	Deadline       time.Time          `json:"deadline"`
}

func (s *Service) RequestAction(ctx context.Context, p security.Principal, instanceID string, in fleet.ActionInput) (*fleet.Operation, error) {
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	instance, err := s.GetInstance(ctx, p, instanceID)
	if err != nil {
		return nil, err
	}
	f, err := s.GetFleet(ctx, p, instance.FleetID)
	if err != nil {
		return nil, err
	}
	if !p.IsFleet() {
		if authErr := s.authz.Require(ctx, p, security.DeploymentCreate, f.ProjectID); authErr != nil {
			return nil, authErr
		}
	}
	if _, err = actionRequestHash(in); err != nil {
		return nil, err
	}
	store, ok := s.store.(ActionStore)
	if !ok {
		return nil, ErrInvalid
	}
	op, err := store.ReserveAction(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, p, instanceID, in)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, f.ProjectID, "fleet.instance."+in.Kind, instanceID)
	return op, nil
}

func actionStateAllowed(kind, status string) error {
	switch kind {
	case "update":
		if status == "ready" || status == "degraded" || status == "failed" {
			return nil
		}
	case "resize", "scale", "suspend":
		if status == "ready" || status == "degraded" {
			return nil
		}
	case "resume":
		if status == "suspended" {
			return nil
		}
	case "retire", "delete", "restore", "move":
		// Retention, legal holds, isolated restoration and data fencing need
		// their native reviewed adapters before an intent can be admitted.
		return ErrApprovalRequired
	default:
		return ErrInvalid
	}
	return ErrConflict
}
