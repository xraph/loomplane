package cloudinfra

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/projects"
)

type reconcileDrainStore struct {
	ClusterLifecycleStore
	lease func(context.Context) (*ClusterOperation, error)
	calls atomic.Int64
}

func (s *reconcileDrainStore) LeaseClusterOperation(ctx context.Context, _, _, _ string, _ time.Duration) (*ClusterOperation, error) {
	s.calls.Add(1)
	return s.lease(ctx)
}

type reconcileDrainAudit struct {
	entered chan context.Context
	release chan struct{}
}

func (a *reconcileDrainAudit) Record(ctx context.Context, _ projects.AuditEvent) {
	a.entered <- ctx
	<-a.release
}

func TestClusterReconcileDrainRejectsLeaseAfterShutdown(t *testing.T) {
	adapter := &InstanceNamespaceTransactions{}
	if err := adapter.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := &reconcileDrainStore{lease: func(context.Context) (*ClusterOperation, error) { return nil, ErrPending }}
	svc := NewService(Deps{Clusters: store, InstanceNamespaces: adapter})
	if err := svc.ReconcileClusterOperation(t.Context(), "org", "operation", nil); !errors.Is(err, currentauthority.ErrUnavailable) {
		t.Errorf("closed adapter admitted reconciliation: %v", err)
	}
	if store.calls.Load() != 0 {
		t.Fatal("shutdown still leased an operation")
	}
}

func TestClusterReconcileDrainOwnsLeaseAndCallerContext(t *testing.T) {
	type callerKey struct{}
	for _, mode := range []string{"caller", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			adapter := &InstanceNamespaceTransactions{}
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			defer close(release)
			store := &reconcileDrainStore{lease: func(ctx context.Context) (*ClusterOperation, error) {
				entered <- ctx
				<-release
				return nil, ctx.Err()
			}}
			svc := NewService(Deps{Clusters: store, InstanceNamespaces: adapter})
			deadline := time.Now().Add(time.Minute)
			caller, cancel := context.WithDeadline(context.WithValue(t.Context(), callerKey{}, "value"), deadline)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- svc.ReconcileClusterOperation(caller, "org", "operation", nil) }()
			var call context.Context
			select {
			case call = <-entered:
			case <-time.After(time.Second):
				t.Fatal("lease did not start")
			}
			got, ok := call.Deadline()
			if !ok || !got.Equal(deadline) || call.Value(callerKey{}) != "value" {
				t.Error("lost caller context")
			}
			if mode == "caller" {
				cancel()
				select {
				case <-call.Done():
				case <-time.After(time.Second):
					t.Error("caller cancellation did not reach lease before shutdown")
				}
			}
			closing, stop := context.WithCancel(t.Context())
			stop()
			if err := adapter.Shutdown(closing); !errors.Is(err, currentauthority.ErrBusy) || adapter.Drained() {
				t.Errorf("shutdown released an unfinished lease: %v", err)
			}
			select {
			case <-call.Done():
			case <-time.After(time.Second):
				t.Error("lease was not cancelled")
			}
			cancel()
			release <- struct{}{}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("lease cancellation: %v", err)
			}
			if err := adapter.Shutdown(t.Context()); err != nil || !adapter.Drained() {
				t.Fatalf("final drain: %v", err)
			}
		})
	}
}

func TestClusterReconcileDrainIncludesDeferredAudit(t *testing.T) {
	adapter := &InstanceNamespaceTransactions{}
	audit := &reconcileDrainAudit{entered: make(chan context.Context, 1), release: make(chan struct{})}
	defer close(audit.release)
	store := &reconcileDrainStore{lease: func(context.Context) (*ClusterOperation, error) {
		return &ClusterOperation{ID: "operation", OrgID: "org", Kind: "invalid"}, nil
	}}
	svc := NewService(Deps{Clusters: store, InstanceNamespaces: adapter, Audit: audit})
	done := make(chan error, 1)
	go func() { done <- svc.ReconcileClusterOperation(t.Context(), "org", "operation", nil) }()
	var auditContext context.Context
	select {
	case auditContext = <-audit.entered:
	case <-time.After(time.Second):
		t.Fatal("deferred audit did not start")
	}
	closing, cancel := context.WithCancel(t.Context())
	cancel()
	if err := adapter.Shutdown(closing); !errors.Is(err, currentauthority.ErrBusy) || adapter.Drained() {
		t.Errorf("shutdown released a blocked deferred audit: %v", err)
	}
	select {
	case <-auditContext.Done():
	case <-time.After(time.Second):
		t.Error("audit did not receive shutdown cancellation")
	}
	audit.release <- struct{}{}
	if err := <-done; !errors.Is(err, ErrInvalid) {
		t.Errorf("reconcile result: %v", err)
	}
	if err := adapter.Shutdown(t.Context()); err != nil || !adapter.Drained() {
		t.Fatalf("audit drain: %v", err)
	}
}

func TestClusterReconcileWithoutNamespaceAdapter(t *testing.T) {
	store := &reconcileDrainStore{lease: func(context.Context) (*ClusterOperation, error) { return nil, ErrPending }}
	svc := NewService(Deps{Clusters: store})
	if err := svc.ReconcileClusterOperation(t.Context(), "org", "operation", nil); err != nil {
		t.Fatal(err)
	}
	if store.calls.Load() != 1 {
		t.Fatal("ordinary reconciliation did not lease")
	}
}
