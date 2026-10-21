//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

func poolRecoveryService(f *poolFixture, runner *deployments.Runner) *cloudinfra.Service {
	return cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &infraAuth{}, PoolCleanup: runner})
}
func poolReview(t *testing.T, svc *cloudinfra.Service, in cloudinfra.RecoveryInput) cloudinfra.RecoveryInput {
	t.Helper()
	v, err := svc.PreviewReservationRecovery(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	in.Review = v.Review
	return in
}
func poolPrepare(t *testing.T, f *poolFixture, plan string) {
	t.Helper()
	r := f.request("env", "api")
	p, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, plan, r, p)
	f.accept(t, plan)
}
func TestServerPoolsRecoveryBeforeEffectsFencesLateWorkers(t *testing.T) {
	f := newPoolFixture(t)
	poolPrepare(t, f, "cancel")
	executor := &poolExecutor{}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	deploymentID, _, startErr := runner.Start(t.Context(), "org", "cancel")
	if startErr != nil {
		t.Fatal(startErr)
	}
	svc := poolRecoveryService(f, nil)
	p := security.Principal{OrgID: "org", UserID: "manager"}
	in := poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "cancel", Service: "api"})
	if _, err := svc.RecoverReservation(t.Context(), security.Principal{OrgID: "org", UserID: "project-member"}, "cloud", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("project member recovered reservation", err)
	}
	if _, err := svc.RecoverReservation(t.Context(), security.Principal{OrgID: "other", UserID: "manager"}, "cloud", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("foreign org recovered reservation", err)
	}
	out, err := svc.RecoverReservation(t.Context(), p, "cloud", in)
	if err != nil || out.Phase != "released" || out.StorageRetained {
		t.Fatalf("pre-effect recovery: %+v %v", out, err)
	}
	if lock, beginErr := f.store.BeginPoolExecution(t.Context(), "org", "cancel", "api"); beginErr == nil {
		lock.Close()
		t.Fatal("late worker passed cancellation fence")
	}
	if runErr := runner.Run(t.Context(), "org", deploymentID, "service:api", nil, func([]byte) error { return nil }); runErr == nil || executor.calls != 0 {
		t.Fatal("queued worker escaped cancellation", runErr)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("held claims: %d %v", count, err)
	}
}

type cleanupPoolExecutor struct {
	poolExecutor
	cleanupCalls int
	block        bool
	resumed      bool
}

func (e *cleanupPoolExecutor) Apply(ctx context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	e.resumed = len(j.Resume) > 0
	return e.poolExecutor.Apply(ctx, j)
}
func (e *cleanupPoolExecutor) CleanupPoolWorkload(_ context.Context, j *deployments.Job, raw []byte, save func([]byte) error) error {
	e.cleanupCalls++
	if len(j.Resume) == 0 {
		return errors.New("lost apply checkpoint")
	}
	if e.block {
		return errors.New("ownership unknown")
	}
	if len(raw) == 0 {
		return save([]byte(`{"owned_compute":"fixture"}`))
	}
	return nil
}
func TestServerPoolsRecoveryRestartAndStalePins(t *testing.T) {
	f := newPoolFixture(t)
	poolPrepare(t, f, "failed")
	executor := &cleanupPoolExecutor{poolExecutor: poolExecutor{fail: true}}
	accounts := &syncAccounts{store: connections.NewPgStore(f.db)}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: accounts, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	dep, _, err := runner.Start(t.Context(), "org", "failed")
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("fixture failure disappeared")
	}
	svc := poolRecoveryService(f, runner)
	principal := security.Principal{OrgID: "org", UserID: "manager"}
	// Simulate process death after a durable checkpoint: lock connection closes,
	// active stays true. Abandonment cannot use the missing lock as proof.
	lock, err := f.store.BeginPoolExecution(t.Context(), "org", "failed", "api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.BeginPoolExecution(t.Context(), "org", "failed", "api"); err == nil {
		t.Fatal("concurrent runtime entered")
	}
	lock.Close()
	in := poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "failed", Service: "api"})
	if err = runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil }); err == nil || !executor.resumed {
		t.Fatalf("restart did not reconcile checkpoint: %v %v", executor.resumed, err)
	}
	in = poolReview(t, svc, in)
	if _, err = f.clouds.SetProjectGrant(t.Context(), "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET generation=generation+1 WHERE id='runtime-1'`); err != nil {
		t.Fatal(err)
	}
	executor.block = true
	if _, err = svc.RecoverReservation(t.Context(), principal, "cloud", in); err == nil {
		t.Fatal("unknown ownership released capacity")
	}
	usage, err := svc.ServerUsage(t.Context(), principal, "cloud", "server-1")
	if err != nil || len(usage.Workloads) != 1 || usage.Workloads[0].Phase != "blocked" {
		t.Fatalf("blocked projection: %+v %v", usage, err)
	}
	if err = runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("closed operation resumed effects")
	}
	executor.block = false
	in = poolReview(t, svc, in)
	out, err := svc.RecoverReservation(t.Context(), principal, "cloud", in)
	if err != nil || out.Phase != "released" || !out.StorageRetained || executor.cleanupCalls != 2 {
		t.Fatalf("recorded cleanup: %+v %v calls %d", out, err, executor.cleanupCalls)
	}
	usage, err = svc.ServerUsage(t.Context(), principal, "cloud", "server-1")
	if err != nil || len(usage.Workloads) != 0 || len(usage.RetainedStorage) != 1 {
		t.Fatalf("retained storage projection: %+v %v", usage, err)
	}
}
func TestServerPoolsUsageScopeAndReviewChanges(t *testing.T) {
	f := newPoolFixture(t)
	poolPrepare(t, f, "first")
	svc := poolRecoveryService(f, nil)
	p := security.Principal{OrgID: "org", UserID: "manager"}
	usage, err := svc.ServerUsage(t.Context(), p, "cloud", "server-1")
	if err != nil || usage.ReservedCPUMillis != 1000 || len(usage.Workloads) != 1 || usage.Workloads[0].ProjectID != "proj" {
		t.Fatalf("usage %+v %v", usage, err)
	}
	raw, _ := json.Marshal(usage)
	var obj map[string]any
	if err = json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ServerUsage(t.Context(), security.Principal{OrgID: "org", UserID: "project-member"}, "cloud", "server-1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("usage disclosed to project member", err)
	}
	drain, err := svc.PreviewDrain(t.Context(), p, "cloud", "server-1")
	if err != nil || drain.Usage == nil {
		t.Fatal(err)
	}
	in := poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "first", Service: "api"})
	lock, err := f.store.BeginPoolExecution(t.Context(), "org", "first", "api")
	if err != nil {
		t.Fatal(err)
	}
	if err = lock.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RecoverReservation(t.Context(), p, "cloud", in); err == nil {
		t.Fatal("stale review accepted")
	}
	if _, err = svc.ApplyDrain(t.Context(), p, "cloud", "server-1", cloudinfra.ApplyInput{Review: drain.Review}); err == nil {
		t.Fatal("drain ignored changed workload phase")
	}
}

func TestServerPoolsPreflightNoEffectRecovery(t *testing.T) {
	f := newPoolFixture(t)
	poolPrepare(t, f, "preflight")
	executor := &poolExecutor{}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	dep, _, err := runner.Start(t.Context(), "org", "preflight")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.clouds.SetProjectGrant(t.Context(), "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	if err = runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil }); err == nil || executor.calls != 0 {
		t.Fatal("preflight reached executor", err)
	}
	svc := poolRecoveryService(f, runner)
	in := poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "preflight", Service: "api"})
	out, err := svc.RecoverReservation(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "cloud", in)
	if err != nil || out.Started || out.Phase != "released" {
		t.Fatalf("no-effect claim stranded: %+v %v", out, err)
	}
	if err = runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil }); err == nil || executor.calls != 0 {
		t.Fatal("late worker escaped fence", err)
	}
}

// The fixture models the daemon lock, including a live old request whose
// PostgreSQL session no longer exists. The SSH fixture checks that physical
// contract against Docker as well.
type fencedPoolExecutor struct {
	poolExecutor
	mu         sync.Mutex
	generation int64
	preparing  chan int64
	entered    chan int64
	release    chan struct{}
}

func (e *fencedPoolExecutor) PreparePoolEffects(ctx context.Context, _ *deployments.Job) error {
	f, ok := server.EffectFenceFrom(ctx)
	if !ok {
		return errors.New("no fence")
	}
	e.preparing <- f.Generation
	e.mu.Lock()
	defer e.mu.Unlock()
	if f.Generation < e.generation {
		return errors.New("stale effect generation")
	}
	e.generation = f.Generation
	return nil
}
func (e *fencedPoolExecutor) Apply(ctx context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	f, _ := server.EffectFenceFrom(ctx)
	if err := j.Checkpoint([]byte(`{"attempted":true}`)); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if f.Generation != e.generation {
		return nil, errors.New("stale mutation")
	}
	e.entered <- f.Generation
	<-e.release
	return &deployments.Outcome{ExternalID: "fenced-api", Revision: "digest"}, nil
}
func (e *fencedPoolExecutor) CleanupPoolWorkload(ctx context.Context, _ *deployments.Job, _ []byte, save func([]byte) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, _ := server.EffectFenceFrom(ctx)
	if f.Generation != e.generation {
		return errors.New("stale cleanup")
	}
	return save([]byte(`{"removed":true}`))
}
func killPoolLockSession(t *testing.T, f *poolFixture) {
	t.Helper()
	var killed bool
	if err := f.sql.QueryRow(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND state='idle in transaction' AND query LIKE 'SELECT pg_try_advisory_xact_lock%'`).Scan(&killed); err != nil || !killed {
		t.Fatal("locking session was not terminated", err)
	}
}
func awaitPoolGeneration(t *testing.T, ch <-chan int64, want int64) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("generation %d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor barrier was not reached")
	}
}
func TestServerPoolsLostSessionLiveExecutor(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprint("resume=", resume), func(t *testing.T) {
			f := newPoolFixture(t)
			poolPrepare(t, f, "live")
			e := &fencedPoolExecutor{preparing: make(chan int64, 4), entered: make(chan int64, 4), release: make(chan struct{}, 2)}
			runner := poolTestRunner(f, e)
			dep, _, err := runner.Start(t.Context(), "org", "live")
			if err != nil {
				t.Fatal(err)
			}
			old := make(chan error, 1)
			go func() {
				old <- runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil })
			}()
			awaitPoolGeneration(t, e.preparing, 1)
			awaitPoolGeneration(t, e.entered, 1)
			killPoolLockSession(t, f)
			svc := poolRecoveryService(f, runner)
			principal := security.Principal{OrgID: "org", UserID: "manager"}
			if resume {
				newer := make(chan error, 1)
				go func() {
					newer <- runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil })
				}()
				awaitPoolGeneration(t, e.preparing, 2)
				review := poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "live", Service: "api"})
				if _, err = svc.RecoverReservation(t.Context(), principal, "cloud", review); err == nil {
					t.Fatal("recovery passed live resumed executor")
				}
				select {
				case generation := <-e.entered:
					t.Fatal("overlapping mutation", generation)
				case <-time.After(100 * time.Millisecond):
				}
				e.release <- struct{}{}
				awaitPoolGeneration(t, e.entered, 2)
				if err = <-old; err == nil {
					t.Fatal("stale successful executor published a binding")
				}
				var active bool
				var generation int64
				if err = f.sql.QueryRow(t.Context(), `SELECT active,generation FROM loomplane_pool_execution WHERE plan_id='live'`).Scan(&active, &generation); err != nil || !active || generation != 2 {
					t.Fatal("stale Finish cleared successor", active, generation, err)
				}
				e.release <- struct{}{}
				if err = <-newer; err != nil {
					t.Fatal(err)
				}
			} else {
				review := poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "live", Service: "api"})
				recovered := make(chan error, 1)
				go func() {
					_, recoverErr := svc.RecoverReservation(t.Context(), principal, "cloud", review)
					recovered <- recoverErr
				}()
				awaitPoolGeneration(t, e.preparing, 2)
				select {
				case err = <-recovered:
					t.Fatal("recovery passed live old mutation", err)
				case <-time.After(100 * time.Millisecond):
				}
				var count int
				if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&count); err != nil || count != 1 {
					t.Fatal("live mutation lost capacity", count, err)
				}
				e.release <- struct{}{}
				if err = <-old; err == nil {
					t.Fatal("abandoned worker published stale binding")
				}
				if err = <-recovered; err != nil {
					t.Fatal(err)
				}
				if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&count); err != nil || count != 0 {
					t.Fatal("quiescent cleanup retained compute claim", count, err)
				}
			}
		})
	}
}
