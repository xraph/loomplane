package platform

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/dispatch/telemetry"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/orchestration"
)

// jobsExtension is Dispatch's extension with its durable worker
// supervised, so a database timeout doesn't stop deployments until the
// process restarts. orchestration.DurableSupervisor explains why. Each
// later workflow build runs a supervised worker of its own beside it.
type jobsExtension struct {
	*dispatchext.Extension
	durable *orchestration.DurableSupervisor
	builds  []*buildWorker
}

// buildWorker runs one workflow build's worker.
type buildWorker struct {
	options    drt.Options
	bind       func(*drt.Worker)
	supervisor *orchestration.DurableSupervisor
	cancel     context.CancelFunc
	done       chan struct{}
}

func newJobs(options drt.Options, opts ...dispatchext.ExtOption) *jobsExtension {
	e := &jobsExtension{Extension: dispatchext.New(append(opts, dispatchext.WithDurableWorkflows(options))...)}
	e.durable = orchestration.NewDurableSupervisor(options, func(msg string, err error) {
		e.Logger().Warn(msg, forge.F("error", err.Error()))
	})
	return e
}

// Start starts Dispatch, then watches the durable worker it started, then
// starts each build's worker.
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
	if err := e.startBuilds(store, eng.Telemetry()); err != nil {
		return errors.Join(err, e.stopBuilds(ctx), e.durable.Stop(ctx), e.Extension.Stop(ctx))
	}
	return nil
}

// startBuilds binds, runs and supervises each build's worker.
func (e *jobsExtension) startBuilds(store durable.Store, tel *telemetry.Telemetry) error {
	for _, b := range e.builds {
		options := b.options
		options.Telemetry = tel
		worker, err := drt.NewWorker(store, options)
		if err != nil {
			return err
		}
		b.bind(worker)
		runCtx, cancel := context.WithCancel(context.Background())
		b.cancel, b.done = cancel, make(chan struct{})
		go func(build string, done chan struct{}) {
			defer close(done)
			if runErr := worker.Run(runCtx); runErr != nil && runCtx.Err() == nil {
				e.warn("workflow worker stopped", forge.F("build", build), forge.F("error", runErr.Error()))
			}
		}(options.BuildID, b.done)
		if err := b.supervisor.Start(worker, store, tel); err != nil {
			return err
		}
	}
	return nil
}

// stopBuilds stops each build's worker that started.
func (e *jobsExtension) stopBuilds(ctx context.Context) error {
	var errs []error
	for _, b := range e.builds {
		if b.cancel == nil {
			continue
		}
		b.cancel()
		errs = append(errs, b.supervisor.Stop(ctx))
		select {
		case <-b.done:
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
		}
	}
	return errors.Join(errs...)
}

// Stop stops every build's worker before Dispatch stops the rest.
func (e *jobsExtension) Stop(ctx context.Context) error {
	return errors.Join(e.stopBuilds(ctx), e.durable.Stop(ctx), e.Extension.Stop(ctx))
}

// WithBuild runs and supervises one more workflow build's worker. bind
// receives the worker once it's built, to start and signal that build's
// runs.
func (e *jobsExtension) WithBuild(options drt.Options, bind func(*drt.Worker)) {
	e.builds = append(e.builds, &buildWorker{options: options, bind: bind,
		supervisor: orchestration.NewDurableSupervisor(options, func(msg string, err error) {
			e.warn(msg, forge.F("build", options.BuildID), forge.F("error", err.Error()))
		})})
}

// warn logs once the extension has a logger, which it has from
// registration on.
func (e *jobsExtension) warn(msg string, fields ...forge.Field) {
	if l := e.Logger(); l != nil {
		l.Warn(msg, fields...)
	}
}

// WithCapacity runs the v3 build's worker.
func (e *jobsExtension) WithCapacity(options drt.Options, bind func(*drt.Worker)) {
	e.WithBuild(options, bind)
}

// Health reports the durable worker that's polling now. Dispatch's own
// check keeps failing after its worker stops, even once a replacement
// runs, so this one asks the supervisor and pings the store itself.
func (e *jobsExtension) Health(ctx context.Context) error {
	for _, b := range e.builds {
		if err := b.supervisor.Health(); err != nil {
			return fmt.Errorf("%s: %w", b.options.BuildID, err)
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
