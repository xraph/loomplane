package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/telemetry"
)

const (
	supervisorCheck      = time.Second
	supervisorMinBackoff = time.Second
	supervisorMaxBackoff = 30 * time.Second
)

// DurableSupervisor keeps a durable worker polling after a store error.
//
// Since v1.9.3 Dispatch retries a claim the store doesn't answer, so a
// claim timeout no longer stops its durable worker. A store error while
// the worker processes a claimed task still does, once its own retries
// run out, and a stopped worker can't be started again. The engine still
// starts, signals and queries runs through that worker, since those calls
// go straight to the store, but nothing claims their tasks: a plan stays
// in planning until the process restarts. The supervisor watches the
// engine's worker and, once it fails, runs a replacement on the same
// store after a backoff, and another each time a replacement fails.
//
// A replacement never runs a task twice. The store hands out only tasks
// whose lease is unset or expired, and each claim takes a new epoch that
// fences any older holder's commits. A task the failed worker may have
// claimed, even one whose claim reply was lost, waits out its lease.
type DurableSupervisor struct {
	options drt.Options
	warn    func(msg string, err error)

	check, minBackoff, maxBackoff time.Duration

	mu      sync.Mutex
	store   durable.Store
	current *drt.Worker
	since   time.Time
	failure error
	cancel  context.CancelFunc
	done    chan struct{}
	runs    sync.WaitGroup
}

// NewDurableSupervisor supervises workers built from options, the same
// options the dispatch extension was given. warn reports each restart.
func NewDurableSupervisor(options drt.Options, warn func(msg string, err error)) *DurableSupervisor {
	if warn == nil {
		warn = func(string, error) {}
	}
	// Each replacement is a new process incarnation, so Dispatch needs a
	// new runtime ID for it; NewWorker makes one when it's empty.
	options.RuntimeID = ""
	return &DurableSupervisor{options: options, warn: warn,
		check: supervisorCheck, minBackoff: supervisorMinBackoff, maxBackoff: supervisorMaxBackoff}
}

// Start watches initial, the engine's own worker, which the engine runs.
// Replacements use store and tel. Call it after the engine has started.
func (s *DurableSupervisor) Start(initial *drt.Worker, store durable.Store, tel *telemetry.Telemetry) error {
	if initial == nil || store == nil {
		return errors.New("durable supervisor: a worker and its store are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return errors.New("durable supervisor: already started")
	}
	s.options.Telemetry = tel
	s.store, s.current, s.since = store, initial, time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.watch(ctx)
	return nil
}

// Stop ends supervision and waits for any replacement to finish its work.
// The engine stops its own worker.
func (s *DurableSupervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	runs := make(chan struct{})
	go func() {
		<-done
		s.runs.Wait()
		close(runs)
	}()
	select {
	case <-runs:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Health reports a worker that has stopped and not yet been replaced, and
// one whose claims the store hasn't been answering. It reads the current
// worker's own state, so it's current between checks; Dispatch's engine
// health only knows about the engine's first worker.
func (s *DurableSupervisor) Health() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil
	}
	status := s.current.Status()
	if status.State != drt.WorkerFailed {
		if status.ClaimFailures > 0 {
			return fmt.Errorf("the durable workflow worker can't claim tasks: %w: %d attempts since %s",
				drt.ErrClaimUnknown, status.ClaimFailures, status.ClaimFailingSince.Format(time.RFC3339))
		}
		return nil
	}
	if s.failure != nil {
		return fmt.Errorf("the durable workflow worker stopped and is restarting: %w", s.failure)
	}
	return fmt.Errorf("the durable workflow worker stopped (%s) and is restarting", status.Failure)
}

func (s *DurableSupervisor) watch(ctx context.Context) {
	defer close(s.done)
	backoff := s.minBackoff
	for {
		if !s.failed() {
			if wait(ctx, s.check) != nil {
				return
			}
			continue
		}
		s.mu.Lock()
		// A worker that ran a good while before failing had a transient
		// fault, not a persistent one, so the backoff starts over.
		if time.Since(s.since) > s.maxBackoff {
			backoff = s.minBackoff
		}
		failure := s.failure
		s.mu.Unlock()
		if failure == nil {
			failure = errors.New("the worker stopped")
		}
		s.warn(fmt.Sprintf("durable workflow worker stopped; starting a replacement in %s", backoff), failure)
		if wait(ctx, backoff) != nil {
			return
		}
		backoff = min(2*backoff, s.maxBackoff)
		s.replace(ctx)
	}
}

func (s *DurableSupervisor) failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.Status().State == drt.WorkerFailed
}

func (s *DurableSupervisor) replace(ctx context.Context) {
	worker, err := drt.NewWorker(s.store, s.options)
	if err != nil {
		// The options were valid for the engine's worker, so this doesn't
		// happen; if it does, the next check tries again after a backoff.
		s.warn("build a replacement durable workflow worker", err)
		return
	}
	s.mu.Lock()
	s.current, s.since, s.failure = worker, time.Now(), nil
	s.mu.Unlock()
	s.runs.Go(func() {
		if runErr := worker.Run(ctx); runErr != nil && ctx.Err() == nil {
			s.mu.Lock()
			if s.current == worker {
				s.failure = runErr
			}
			s.mu.Unlock()
		}
	})
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
