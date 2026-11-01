// Package fleets owns instance identities, policy and lifecycle coordination.
package fleets

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/pkg/fleet"
)

// Domain errors retain stable identity through storage and API adapters.
var (
	ErrNotFound = errors.New("fleet resource not found")
	ErrInvalid  = errors.New("invalid fleet request")
	ErrConflict = errors.New("fleet request conflicts with existing state")
	ErrCapacity = errors.New("fleet instance limit reached")
)

// FleetScope is resolved from the authenticated organization and project.
type FleetScope struct{ OrgID, ProjectID, FleetID string }

// Store persists fleet metadata; reservation is an internal workflow primitive.
type Store interface {
	CreateFleet(context.Context, *fleet.Fleet) error
	GetFleet(context.Context, string, string) (*fleet.Fleet, error)
	CreateStage(context.Context, FleetScope, *fleet.Stage) error
	ReserveInstance(context.Context, FleetScope, fleet.CreateInstanceInput) (*fleet.OperationResult, error)
	ManagedEnvironment(context.Context, string, string) (*fleet.Instance, error)
}
