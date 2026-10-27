package security

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/permission"
	"github.com/xraph/warden/store/memory"
)

type gatedCatalogStore struct {
	Store
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
	active  atomic.Bool
	first   atomic.Bool
}

var errCatalogOverlap = errors.New("concurrent role permission replacement")

func (s *gatedCatalogStore) SetRolePermissions(ctx context.Context, tenant string, role wardenid.RoleID, refs []permission.Ref) error {
	s.calls.Add(1)
	if !s.active.CompareAndSwap(false, true) {
		return errCatalogOverlap
	}
	defer s.active.Store(false)
	if s.first.CompareAndSwap(false, true) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.SetRolePermissions(ctx, tenant, role, refs)
}
func TestEnsureTenantCoalescesConcurrentCatalogWrites(t *testing.T) {
	store := &gatedCatalogStore{Store: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	authorizer := NewAuthorizer(&recordingEngine{}, store, nil)
	const callers = 24
	results := make(chan error, callers)
	go func() { results <- authorizer.EnsureTenant(t.Context(), "org") }()
	<-store.entered
	var started sync.WaitGroup
	started.Add(callers - 1)
	for range callers - 1 {
		go func() { started.Done(); results <- authorizer.EnsureTenant(t.Context(), "org") }()
	}
	started.Wait()
	// Hold the first real store write while the other callers reach the same
	// initialization. Concurrent replacements are unsafe on PostgreSQL.
	timer := time.NewTimer(100 * time.Millisecond)
	<-timer.C
	close(store.release)
	for range callers {
		if err := <-results; err != nil {
			t.Errorf("concurrent initialization: %v", err)
		}
	}
	// One replacement per member role, and one for the automation's.
	if calls := store.calls.Load(); int(calls) != len(Catalog())+1 {
		t.Fatalf("role catalog was replaced %d times, want %d", calls, len(Catalog())+1)
	}
}
func TestEnsureTenantCancelledWaiterDoesNotRewriteCatalog(t *testing.T) {
	store := &gatedCatalogStore{Store: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	authorizer := NewAuthorizer(&recordingEngine{}, store, nil)
	first := make(chan error, 1)
	go func() { first <- authorizer.EnsureTenant(t.Context(), "org") }()
	<-store.entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := authorizer.EnsureTenant(ctx, "org")
	close(store.release)
	if firstErr := <-first; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter rewrote an active catalog: %v", err)
	}
	if int(store.calls.Load()) != len(Catalog())+1 {
		t.Fatal("cancelled waiter accessed role replacement")
	}
}
