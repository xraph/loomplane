//go:build integration

package cloudinfra

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"
)

func TestFleetNamespaceShutdownRetainsPostgresUntilFinalizerReturns(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("LOOMPLANE_TEST_DATABASE_URL is required")
	}
	db := pgdriver.New()
	openCtx, cancelOpen := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelOpen()
	if err := db.Open(openCtx, dsn); err != nil {
		t.Fatal("could not open synthetic PostgreSQL fixture")
	}
	t.Cleanup(func() { _ = db.Close() })
	cleanupErr := errors.New("synthetic finalizer diagnostic")
	drain := &namespaceDrainProbe{closing: make(chan struct{}), done: make(chan struct{}), err: cleanupErr}
	var release sync.Once
	releaseFinalizer := func() { release.Do(func() { close(drain.done) }) }
	t.Cleanup(releaseFinalizer)
	workerDone := make(chan struct{})
	close(workerDone)
	e := &Extension{done: workerDone, namespaces: drain}
	dependencyClosed := make(chan struct{})
	stopResult := make(chan error, 1)
	pool := &fleetStopProbe{BaseExtension: forge.NewBaseExtension("main-db", "1", "synthetic main PostgreSQL pool"), stop: func(context.Context) error { err := db.Close(); close(dependencyClosed); return err }}
	consumer := &fleetStopProbe{BaseExtension: forge.NewBaseExtension("namespace-consumer", "1", "namespace shutdown"), dependencies: []string{"main-db"}, stop: func(ctx context.Context) error { err := e.Stop(ctx); stopResult <- err; return err }}
	app := forge.New(forge.WithAppName("namespace-postgres-stop-test"), forge.WithExtensions(pool, consumer))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := startFleetTestShutdown(ctx, t, app, releaseFinalizer)
	select {
	case <-drain.closing:
	case <-time.After(time.Second):
		t.Fatal("namespace shutdown did not begin")
	}
	select {
	case <-dependencyClosed:
		t.Fatal("main pool closed before finalizer release")
	case <-time.After(50 * time.Millisecond):
	}
	readCtx, cancelRead := context.WithTimeout(t.Context(), time.Second)
	defer cancelRead()
	var one int
	if err := db.QueryRow(readCtx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatal("main pool became unavailable while finalizer retained ownership")
	}
	releaseFinalizer()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after finalizer release")
	}
	if err := <-stopResult; !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupErr) {
		t.Fatalf("shutdown lost diagnostics: %v", err)
	}
	select {
	case <-dependencyClosed:
	default:
		t.Fatal("main pool did not close after drain")
	}
	closedCtx, cancelClosed := context.WithTimeout(t.Context(), time.Second)
	defer cancelClosed()
	if err := db.Ping(closedCtx); err == nil {
		t.Fatal("main pool remained usable after dependency teardown")
	}
	if err := closedCtx.Err(); err != nil {
		t.Fatalf("pool closure check exhausted its own deadline: %v", err)
	}
}
