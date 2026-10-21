//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
	if _, err = svc.RecoverReservation(t.Context(), principal, "cloud", in); err == nil {
		t.Fatal("interrupted worker treated as absent")
	}
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
