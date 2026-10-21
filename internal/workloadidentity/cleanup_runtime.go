package workloadidentity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

type cleanupAdmin interface {
	EnrollmentIssuer
	NodeReader
	Health(context.Context) error
	Close() error
}

// CleanupStatus separates the last sweep's backlog failures from worker health.
type CleanupStatus struct {
	LastSweep time.Time
	Result    CleanupResult
	Err       error
}

// CleanupRuntime owns the administrative connection and one sequential worker.
// Construction performs no I/O. Start must follow database migrations.
type CleanupRuntime struct {
	cfg              config.FederationIdentityConfig
	store            CleanupStore
	onResult         func(CleanupResult, error)
	openAdmin        func(context.Context, string, string, string) (cleanupAdmin, error)
	lifecycle        sync.Mutex
	mu               sync.Mutex
	admin            cleanupAdmin
	cancel           context.CancelFunc
	done             chan struct{}
	running          bool
	runErr, closeErr error
	status           CleanupStatus
}

func NewCleanupRuntime(cfg config.FederationIdentityConfig, store CleanupStore, onResult func(CleanupResult, error)) (*CleanupRuntime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled && store == nil {
		return nil, errors.New("federation identity cleanup requires database")
	}
	return &CleanupRuntime{cfg: cfg, store: store, onResult: onResult, openAdmin: func(ctx context.Context, address, td, org string) (cleanupAdmin, error) {
		return OpenAdmin(ctx, address, td, org)
	}}, nil
}

func (r *CleanupRuntime) Start(ctx context.Context) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if !r.cfg.Enabled {
		return nil
	}
	r.mu.Lock()
	started := r.done != nil
	r.mu.Unlock()
	if started {
		return errors.New("federation identity cleanup already started")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	startup, cancelStartup := context.WithTimeout(ctx, adminTimeout)
	defer cancelStartup()
	admin, err := r.openAdmin(startup, r.cfg.AdminSocket, r.cfg.TrustDomain, r.cfg.OrganizationID)
	if err != nil {
		return fmt.Errorf("federation identity administration: %w", err)
	}
	if err := startup.Err(); err != nil {
		return errors.Join(err, admin.Close())
	}
	workerContext, cancel := context.WithCancel(ctx)
	worker := &CleanupWorker{OrgID: r.cfg.OrganizationID, Store: r.store, Issuer: admin, OnResult: func(result CleanupResult, err error) {
		r.mu.Lock()
		r.status = CleanupStatus{LastSweep: time.Now(), Result: result, Err: err}
		r.mu.Unlock()
		if r.onResult != nil {
			r.onResult(result, err)
		}
	}}
	r.mu.Lock()
	r.admin = admin
	r.cancel = cancel
	r.done = make(chan struct{})
	r.running = true
	r.mu.Unlock()
	go func() {
		runErr := worker.Run(workerContext)
		cancel()
		closeErr := admin.Close()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.running = false
		if !errors.Is(runErr, context.Canceled) {
			r.runErr = runErr
		}
		r.closeErr = closeErr
		close(r.done)
	}()
	return nil
}

func (r *CleanupRuntime) Stop(ctx context.Context) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.Lock()
	done, cancel := r.done, r.cancel
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return errors.Join(r.runErr, r.closeErr)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *CleanupRuntime) Health(ctx context.Context) error {
	if !r.cfg.Enabled {
		return nil
	}
	r.mu.Lock()
	admin, running, runErr := r.admin, r.running, r.runErr
	r.mu.Unlock()
	if !running {
		return errors.Join(errors.New("federation identity cleanup is not running"), runErr)
	}
	return admin.Health(ctx)
}

func (r *CleanupRuntime) Status() CleanupStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}
