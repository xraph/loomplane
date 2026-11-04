package currentauthority

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type delayedCleanupTx struct {
	nativeTx
	started chan struct{}
	release <-chan struct{}
	failure error
}

func (t delayedCleanupTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if query != "SELECT 42" {
		return t.nativeTx.Exec(ctx, query, args...)
	}
	close(t.started)
	// This fixture deliberately violates pgx's cancellation contract so the
	// coordinator must retain resource ownership after its cleanup budget ends.
	<-t.release
	return pgconn.CommandTag{}, ctx.Err()
}

func (t delayedCleanupTx) Rollback(ctx context.Context) error {
	return errors.Join(t.nativeTx.Rollback(ctx), t.failure)
}

func TestPostgresLateCleanupFailureSurvivesShutdownAndRepeatedClose(t *testing.T) {
	f := newPGFixture(t)
	lateFailure := errors.New("injected late rollback failure")
	started := make(chan struct{})
	release := make(chan struct{})
	callDone := make(chan error, 1)
	f.coordinator.backend = interceptBackend{backend: f.native, wrap: func(tx nativeTx) nativeTx {
		return delayedCleanupTx{nativeTx: tx, started: started, release: release, failure: lateFailure}
	}}
	// The expected diagnostic is asserted below; don't report it a second time
	// from the fixture's cleanup callback.
	t.Cleanup(func() { f.coordinator.mu.Lock(); f.coordinator.cleanupErr = nil; f.coordinator.mu.Unlock() })
	err := f.coordinator.Run(context.Background(), testBinding(), func(ctx context.Context, a *Attempt) error {
		go func() { _, queryErr := a.Tx().Exec(ctx, "SELECT 42"); callDone <- queryErr }()
		<-started
		return nil
	})
	if !errors.Is(err, ErrPending) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded cleanup result: %v", err)
	}
	if err := f.coordinator.Close(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Close before cleanup drains: %v", err)
	}
	shutdownCtx, stopShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopShutdown()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- f.coordinator.Shutdown(shutdownCtx) }()
	close(release)
	if err := <-callDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("scoped call: %v", err)
	}
	if err := <-shutdownDone; !errors.Is(err, lateFailure) {
		t.Fatalf("Shutdown lost cleanup failure: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := f.coordinator.Close(); !errors.Is(err, lateFailure) {
			t.Fatalf("Close %d lost cleanup failure: %v", i, err)
		}
		if err := f.coordinator.Shutdown(context.Background()); !errors.Is(err, lateFailure) {
			t.Fatalf("Shutdown %d lost cleanup failure: %v", i, err)
		}
	}
	if f.count(t) != 0 {
		t.Fatal("late cleanup committed candidate")
	}
}
