package workloadidentity

import (
	"context"
	"errors"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// CleanupStores gives each journal a share of one worker's time and item budget.
// A failed or slow journal cannot consume the following journal's entire sweep.
type CleanupStores []CleanupStore

func (stores CleanupStores) Cleanup(ctx context.Context, org string, limit int, issuer EnrollmentIssuer) (CleanupResult, error) {
	result := CleanupResult{}
	if len(stores) == 0 || limit < len(stores) || limit > 100 || issuer == nil || !domain.ValidIdentitySegment(org) {
		return result, ErrIdentity
	}
	for _, store := range stores {
		if store == nil {
			return result, ErrIdentity
		}
	}
	var failures error
	for n, store := range stores {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(failures, err)
		}
		remaining := len(stores) - n
		budget := limit / remaining
		duration := 10 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			duration = time.Until(deadline) / time.Duration(remaining)
		}
		sweep, cancel := context.WithTimeout(ctx, duration)
		got, err := store.Cleanup(sweep, org, budget, issuer)
		cancel()
		result.Attempted += got.Attempted
		result.Deleted += got.Deleted
		result.Failed += got.Failed
		result.Skipped += got.Skipped
		failures = errors.Join(failures, err)
		limit -= budget
	}
	return result, failures
}
