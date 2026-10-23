package platform

import (
	"context"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/orchestration"
)

// jobsExtension is Dispatch's extension with its durable worker
// supervised, so a database timeout doesn't stop deployments until the
// process restarts. orchestration.DurableSupervisor explains why.
type jobsExtension struct {
	*dispatchext.Extension
	durable *orchestration.DurableSupervisor
}

func newJobs(options drt.Options, opts ...dispatchext.ExtOption) *jobsExtension {
	e := &jobsExtension{Extension: dispatchext.New(append(opts, dispatchext.WithDurableWorkflows(options))...)}
	e.durable = orchestration.NewDurableSupervisor(options, func(msg string, err error) {
		e.Logger().Warn(msg, forge.F("error", err.Error()))
	})
	return e
}

// Start starts Dispatch, then watches the durable worker it started.
func (e *jobsExtension) Start(ctx context.Context) error {
	if err := e.Extension.Start(ctx); err != nil {
		return err
	}
	eng := e.Engine()
	store, ok := eng.Dispatcher().Store().(durable.Store)
	if !ok {
		return engine.ErrDurableUnsupported
	}
	return e.durable.Start(eng.DurableWorker(), store, eng.Telemetry())
}

// Stop stops any replacement worker before Dispatch stops the rest.
func (e *jobsExtension) Stop(ctx context.Context) error {
	if err := e.durable.Stop(ctx); err != nil {
		return err
	}
	return e.Extension.Stop(ctx)
}

// Health reports the durable worker that's polling now. Dispatch's own
// check keeps failing after its worker stops, even once a replacement
// runs, so this one asks the supervisor and pings the store itself.
func (e *jobsExtension) Health(ctx context.Context) error {
	if err := e.durable.Health(); err != nil {
		return err
	}
	eng := e.Engine()
	if eng == nil {
		return e.Extension.Health(ctx)
	}
	return eng.Dispatcher().Store().Ping(ctx)
}
