package releasewatch

import (
	"context"
	"time"
)

// Store persists watches, scoped to an organization.
type Store interface {
	// Insert records w unless its deployment already has a watch, and
	// reports whether it did. It sets w's Revision.
	Insert(ctx context.Context, w *Watch) (bool, error)
	// ForDeployment returns a deployment's watch, or ErrNotFound.
	ForDeployment(ctx context.Context, orgID, deploymentID string) (*Watch, error)
	// Latest returns an environment's newest watch, or ErrNotFound.
	Latest(ctx context.Context, orgID, environmentID string) (*Watch, error)
	// Due claims up to limit open watches nobody checked in the last
	// 50 seconds, across organizations, stamping their CheckedAt with now.
	// Two callers never claim the same watch.
	Due(ctx context.Context, now time.Time, limit int) ([]*Watch, error)
	// Save writes w when its Revision is still the stored one and bumps
	// it, or returns ErrConflict.
	Save(ctx context.Context, w *Watch) error
}
