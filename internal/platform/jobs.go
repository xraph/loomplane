package platform

import (
	"context"
	"errors"

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
	durable         *orchestration.DurableSupervisor
	capacity        *orchestration.DurableSupervisor
	capacityOptions drt.Options
	bindCapacity    func(*drt.Worker)
	capacityCancel  context.CancelFunc
	capacityDone    chan struct{}
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
	if err := e.durable.Start(eng.DurableWorker(), store, eng.Telemetry()); err != nil {
		return err
	}
	if e.capacity == nil {
		return nil
	}
	options := e.capacityOptions
	options.Telemetry = eng.Telemetry()
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		return errors.Join(err, e.durable.Stop(ctx), e.Extension.Stop(ctx))
	}
	e.bindCapacity(worker)
	runCtx, cancel := context.WithCancel(context.Background())
	e.capacityCancel = cancel
	e.capacityDone = make(chan struct{})
	go func() {
		defer close(e.capacityDone)
		if runErr := worker.Run(runCtx); runErr != nil && runCtx.Err() == nil {
			e.Logger().Warn("capacity workflow worker stopped", forge.F("error", runErr.Error()))
		}
	}()
	if err = e.capacity.Start(worker, store, eng.Telemetry()); err != nil {
		cancel()
		return errors.Join(err, e.durable.Stop(ctx), e.Extension.Stop(ctx))
	}
	return nil
}

// Stop stops any replacement worker before Dispatch stops the rest.
func (e *jobsExtension) Stop(ctx context.Context) error {
	var err error
	if e.capacityCancel != nil {
		e.capacityCancel()
		err = e.capacity.Stop(ctx)
		select {
		case <-e.capacityDone:
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
		}
	}
	return errors.Join(err, e.durable.Stop(ctx), e.Extension.Stop(ctx))
}

func (e *jobsExtension) WithCapacity(options drt.Options, bind func(*drt.Worker)) {
	e.capacityOptions = options
	e.bindCapacity = bind
	e.capacity = orchestration.NewDurableSupervisor(options, func(msg string, err error) { e.Logger().Warn(msg, forge.F("error", err.Error())) })
}

// Health reports the durable worker that's polling now. Dispatch's own
// check keeps failing after its worker stops, even once a replacement
// runs, so this one asks the supervisor and pings the store itself.
func (e *jobsExtension) Health(ctx context.Context) error {
	if e.capacity != nil {
		if err := e.capacity.Health(); err != nil {
			return err
		}
	}
	if err := e.durable.Health(); err != nil {
		return err
	}
	eng := e.Engine()
	if eng == nil {
		return e.Extension.Health(ctx)
	}
	return eng.Dispatcher().Store().Ping(ctx)
}
