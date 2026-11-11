package identity

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type delayedIdentityAuthority struct {
	entered chan struct{}
	joining chan context.Context
	release chan struct{}
	drained atomic.Bool
	calls   atomic.Int32
	failure error
}

func (o *delayedIdentityAuthority) Shutdown(ctx context.Context) error {
	if o.calls.Add(1) == 1 {
		close(o.entered)
		<-ctx.Done()
		return ctx.Err()
	}
	o.joining <- ctx
	select {
	case <-o.release:
		o.drained.Store(true)
		return o.failure
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *delayedIdentityAuthority) Drained() bool { return o.drained.Load() }

func TestIdentityAuthorityShutdownJoinsCleanupBeforeDependencies(t *testing.T) {
	cleanupFailure := errors.New("late authority cleanup")
	owner := &delayedIdentityAuthority{
		entered: make(chan struct{}), joining: make(chan context.Context, 1),
		release: make(chan struct{}), failure: cleanupFailure,
	}
	defer func() {
		select {
		case <-owner.release:
		default:
			close(owner.release)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	var dependencyClosed atomic.Bool
	done := make(chan error, 1)
	go func() {
		err := shutdownCurrentAuthority(ctx, owner)
		dependencyClosed.Store(true)
		done <- err
	}()
	<-owner.entered
	select {
	case joining := <-owner.joining:
		if joining.Err() != nil || joining.Done() != nil {
			t.Fatal("final drain retained the expired caller context")
		}
	case err := <-done:
		t.Fatalf("dependency teardown began before cleanup drained: %v", err)
	case <-time.After(time.Second):
		t.Fatal("authority cleanup was not joined")
	}
	if owner.Drained() || dependencyClosed.Load() {
		t.Fatal("owned cleanup allowed dependency teardown")
	}
	close(owner.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cleanupFailure) {
			t.Fatalf("shutdown diagnostics=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("completed cleanup did not release shutdown")
	}
	if !owner.Drained() || !dependencyClosed.Load() || owner.calls.Load() != 2 {
		t.Fatal("shutdown did not release drained dependencies once")
	}
}

type drainedIdentityAuthority struct {
	failure error
	calls   int
}

func (o *drainedIdentityAuthority) Shutdown(context.Context) error {
	o.calls++
	return o.failure
}
func (*drainedIdentityAuthority) Drained() bool { return true }

func TestIdentityAuthorityShutdownRetainsCompletedDiagnostic(t *testing.T) {
	owner := &drainedIdentityAuthority{failure: errors.New("completed cleanup diagnostic")}
	if err := shutdownCurrentAuthority(t.Context(), owner); !errors.Is(err, owner.failure) {
		t.Fatal(err)
	}
	if owner.calls != 1 {
		t.Fatal("completed cleanup was treated as pending ownership")
	}
}
