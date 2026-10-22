package workloadidentity

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cleanupStoreFunc func(context.Context, string, int, EnrollmentIssuer) (CleanupResult, error)

func (f cleanupStoreFunc) Cleanup(ctx context.Context, org string, limit int, i EnrollmentIssuer) (CleanupResult, error) {
	return f(ctx, org, limit, i)
}
func TestCleanupStoresShareBoundsAndSurviveAnEarlierTimeout(t *testing.T) {
	calls := 0
	stores := CleanupStores{
		cleanupStoreFunc(func(ctx context.Context, org string, limit int, i EnrollmentIssuer) (CleanupResult, error) {
			if org != "org" || limit != 10 || i == nil {
				t.Error("wrong cleanup bounds")
			}
			<-ctx.Done()
			return CleanupResult{Attempted: 10, Failed: 10}, ctx.Err()
		}),
		cleanupStoreFunc(func(ctx context.Context, _ string, limit int, _ EnrollmentIssuer) (CleanupResult, error) {
			calls++
			if ctx.Err() != nil || limit != 10 {
				t.Error("later journal starved")
			}
			return CleanupResult{Attempted: 1, Deleted: 1}, nil
		}),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	result, err := stores.Cleanup(ctx, "org", 20, &SPIREIssuer{})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || result.Attempted != 11 || result.Failed != 10 || result.Deleted != 1 {
		t.Fatal("cleanup result lost", result, err, calls)
	}
	if _, err = stores.Cleanup(t.Context(), "org", 1, &SPIREIssuer{}); !errors.Is(err, ErrIdentity) {
		t.Fatal("invalid shared bound", err)
	}
}
