// Package federation manages scoped destinations and observed capabilities.
package federation

import (
	"context"
	"errors"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// Domain errors expose no provider credentials or foreign resource details.
var (
	ErrNotFound        = errors.New("federation destination or scoped resource not found")
	ErrInvalid         = errors.New("invalid federation destination")
	ErrConflict        = errors.New("federation destination changed or already exists")
	ErrDisabled        = errors.New("federation management is disabled")
	ErrUnverified      = errors.New("destination identity requires a fresh connection check")
	ErrIdentityChanged = errors.New("destination physical identity changed; register a new destination")
)

// Store persists destination state with optimistic generation checks.
type Store interface {
	Create(ctx context.Context, d *domain.Destination) error
	Get(ctx context.Context, orgID, id string) (*domain.Destination, error)
	Update(ctx context.Context, d *domain.Destination, previousGeneration int64) error
}
