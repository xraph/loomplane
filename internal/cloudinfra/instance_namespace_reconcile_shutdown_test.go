package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/currentauthority"
)

func TestNamespacePostgresShutdownWaitsForPrimaryReconciliation(t *testing.T) {
	f := newNamespacePGFixture(t, "receipt-commit")
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	gate := &namespaceReconcileGate{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(gate.unblock)
	f.proxy.mu.Lock()
	f.proxy.reconcileGate = gate
	f.proxy.mu.Unlock()
	type outcome struct {
		result *namespaceResult
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		result, replayErr := f.transactions.run(t.Context(), f.action)
		completed <- outcome{result, replayErr}
	}()
	select {
	case <-gate.started:
	case result := <-completed:
		t.Fatalf("replay returned before primary query gate: %v", result.err)
	case <-time.After(6 * time.Second):
		t.Fatal("replay did not reach main-pool primary reconciliation")
	}
	if f.transactions.Drained() {
		t.Fatal("adapter reported drained before closing")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = f.transactions.Shutdown(ctx)
	if err != nil && !errors.Is(err, currentauthority.ErrBusy) {
		t.Fatal(err)
	}
	if err == nil && !f.transactions.Drained() {
		t.Fatal("shutdown returned before ownership released")
	}
	// The query remains gated. Shutdown must cancel it without waiting for data.
	select {
	case result := <-completed:
		if result.result != nil || !errors.Is(result.err, currentauthority.ErrCommitUnknown) {
			t.Fatalf("interrupted verification claimed a result: %v", result.err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("shutdown failed to cancel primary reconciliation")
	}
	if err = f.transactions.Shutdown(context.WithoutCancel(ctx)); err != nil || !f.transactions.Drained() {
		t.Fatalf("final drain: %v", err)
	}
	if _, err = f.transactions.run(t.Context(), f.action); !errors.Is(err, currentauthority.ErrUnavailable) {
		t.Fatal("drained adapter admitted another run")
	}
	gate.unblock()
	in, digest, err := freezeNamespaceAction(f.action)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := f.transactions.store.reconcileNamespaceAttempt(t.Context(), f.schema, in, digest)
	if err != nil || !namespaceEqual(recovered, first) {
		t.Fatalf("shutdown lost durable commit evidence: %v", err)
	}
}

func TestNamespacePostgresRunHonorsCallerCancellation(t *testing.T) {
	f := newNamespacePGFixture(t)
	blocker, observer := namespaceFixtureConnection(t, f), namespaceFixtureConnection(t, f)
	hold, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err = hold.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('cloud-organization/org',0))`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, runErr := f.transactions.run(ctx, f.action); done <- runErr }()
	namespaceAwaitBlocked(t, observer, blocker.PgConn().PID())
	cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("cancelled authority run succeeded")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("authority run ignored caller cancellation")
	}
	if err = hold.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled run wrote evidence: %d %v", count, err)
	}
	if err = f.transactions.Shutdown(t.Context()); err != nil || !f.transactions.Drained() {
		t.Fatalf("cancelled run retained ownership: %v", err)
	}
}
