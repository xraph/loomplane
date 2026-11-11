package cloudinfra

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"
)

type fleetStopProbe struct {
	*forge.BaseExtension
	dependencies []string
	stop         func(context.Context) error
}

type namespaceDrainProbe struct {
	mu      sync.Mutex
	closing chan struct{}
	done    chan struct{}
	closed  bool
	err     error
}

func (p *namespaceDrainProbe) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.closing)
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *namespaceDrainProbe) Drained() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func TestFleetNamespaceShutdownDrainsBeforeForgeDependencyClose(t *testing.T) {
	cleanupErr := errors.New("synthetic cleanup diagnostic")
	drain := &namespaceDrainProbe{closing: make(chan struct{}), done: make(chan struct{}), err: cleanupErr}
	releaseDrain := releaseFleetTestGate(drain.done)
	workerDone := make(chan struct{})
	releaseWorker := releaseFleetTestGate(workerDone)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	t.Cleanup(cancelWorker)
	e := &Extension{cancel: cancelWorker, done: workerDone, namespaces: drain}
	closed := make(chan struct{})
	stopResult := make(chan error, 1)
	db := &fleetStopProbe{BaseExtension: forge.NewBaseExtension("main-db", "1", "test database lifetime"), stop: func(context.Context) error { close(closed); return nil }}
	consumer := &fleetStopProbe{BaseExtension: forge.NewBaseExtension("namespace-consumer", "1", "test namespace shutdown"), dependencies: []string{"main-db"}, stop: func(ctx context.Context) error { err := e.Stop(ctx); stopResult <- err; return err }}
	app := forge.New(forge.WithAppName("namespace-drain-test"), forge.WithExtensions(db, consumer))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := startFleetTestShutdown(ctx, t, app, func() { releaseWorker(); releaseDrain() })
	select {
	case <-drain.closing:
	case <-time.After(time.Second):
		t.Fatal("namespace admission stayed open")
	}
	select {
	case <-workerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("worker was not cancelled")
	}
	releaseWorker()
	select {
	case <-closed:
		t.Fatal("database closed before namespace finalizer drained")
	case <-time.After(50 * time.Millisecond):
	}
	if drain.Drained() {
		t.Fatal("retained finalizer reported drained")
	}
	releaseDrain()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after namespace drain")
	}
	if err := <-stopResult; !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupErr) {
		t.Fatalf("shutdown lost diagnostics: %v", err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("dependency did not close after namespace drain")
	}
}

func (p *fleetStopProbe) Dependencies() []string         { return p.dependencies }
func (p *fleetStopProbe) Stop(ctx context.Context) error { return p.stop(ctx) }

func TestFleetNamespaceShutdownKeepsDependenciesUntilWorkersDrain(t *testing.T) {
	workerDone := make(chan struct{})
	releaseWorker := releaseFleetTestGate(workerDone)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	t.Cleanup(cancelWorker)
	e := &Extension{cancel: cancelWorker, done: workerDone}
	closed := make(chan struct{})
	db := &fleetStopProbe{BaseExtension: forge.NewBaseExtension("main-db", "1", "test database lifetime"), stop: func(context.Context) error { close(closed); return nil }}
	consumer := &fleetStopProbe{BaseExtension: forge.NewBaseExtension("namespace-consumer", "1", "test namespace shutdown"), dependencies: []string{"main-db"}, stop: e.Stop}
	app := forge.New(forge.WithAppName("namespace-shutdown-test"), forge.WithExtensions(db, consumer))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := startFleetTestShutdown(ctx, t, app, releaseWorker)
	select {
	case <-workerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("worker cancellation was not requested")
	}
	premature := false
	select {
	case <-closed:
		premature = true
	case <-time.After(50 * time.Millisecond):
	}
	releaseWorker()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not settle after worker drain")
	}
	if premature {
		t.Fatal("Forge closed the database dependency while a namespace worker still owned work")
	}
	select {
	case <-closed:
	default:
		t.Fatal("database dependency was not released after drain")
	}
}

func releaseFleetTestGate(gate chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func startFleetTestShutdown(ctx context.Context, t *testing.T, app forge.App, release func()) <-chan struct{} {
	t.Helper()
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = app.Stop(ctx) }()
	t.Cleanup(func() {
		release()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("test shutdown did not join after releasing its gates")
		}
	})
	return stopped
}
