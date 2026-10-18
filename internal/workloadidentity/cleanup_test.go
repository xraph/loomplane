package workloadidentity

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type cleanupFixture struct{ calls atomic.Int32 }

func (c *cleanupFixture) Cleanup(context.Context, string, int, EnrollmentIssuer) (CleanupResult, error) {
	c.calls.Add(1)
	return CleanupResult{Attempted: 1, Failed: 1}, errors.New("issuer unavailable")
}
func TestCleanupWorkerRetriesWithoutOverlapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	store := &cleanupFixture{}
	seen := 0
	worker := CleanupWorker{OrgID: "org", Store: store, Issuer: &SPIREIssuer{}, Interval: time.Millisecond, OnResult: func(result CleanupResult, err error) {
		seen++
		if err == nil || result.Failed != 1 || result.Attempted != 1 {
			t.Error("cleanup failure hidden")
		}
		if seen == 2 {
			cancel()
		}
	}}
	err := worker.Run(ctx)
	if !errors.Is(err, context.Canceled) || seen != 2 || store.calls.Load() != 2 {
		t.Fatalf("cleanup retries: calls=%d seen=%d error=%v", store.calls.Load(), seen, err)
	}
}
