//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/providers"
)

// gpuDaemon stands in for one Docker daemon's GPU device labels. Like
// the server runtime's gpuDevices, an apply reads which devices are free
// and assigns one later, so two applies that overlap both pick the same.
type gpuDaemon struct {
	mu     sync.Mutex
	labels map[string]string // service -> device
}

func (d *gpuDaemon) free() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, device := range []string{"GPU-0", "GPU-1"} {
		taken := false
		for _, used := range d.labels {
			taken = taken || used == device
		}
		if !taken {
			return device
		}
	}
	return ""
}

func (d *gpuDaemon) assign(service, device string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.labels[service] = device
}

func (d *gpuDaemon) devices() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]string{}
	for k, v := range d.labels {
		out[k] = v
	}
	return out
}

// gpuDaemonExecutor takes the daemon's GPU lock as the server runtime
// does, reads a free device, reports that it entered, and holds its read
// until released before it writes the device label.
type gpuDaemonExecutor struct {
	poolExecutor
	daemon  *gpuDaemon
	locks   *owned.GPULocks
	entered chan struct{}
	release chan struct{}
}

func newGPUDaemonExecutor(f *poolFixture, d *gpuDaemon, hold bool) *gpuDaemonExecutor {
	e := &gpuDaemonExecutor{daemon: d, locks: owned.NewGPULocks(f.db), entered: make(chan struct{}), release: make(chan struct{})}
	if !hold {
		close(e.release)
	}
	return e
}

func (e *gpuDaemonExecutor) Apply(ctx context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	unlock, err := e.locks.LockGPUs(ctx, j.Connection.Details["docker_id"])
	if err != nil {
		return nil, err
	}
	defer unlock()
	device := e.daemon.free()
	close(e.entered)
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if device == "" {
		return nil, errors.New("no free GPU")
	}
	e.daemon.assign(j.Action.Name, device)
	unlock()
	return e.poolExecutor.Apply(ctx, j)
}

// gpuApply is one approved plan, started and ready to run.
type gpuApply struct {
	name       string
	deployment string
	runner     *deployments.Runner
	executor   *gpuDaemonExecutor
}

func (a gpuApply) run(t *testing.T) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- a.runner.Run(t.Context(), "org", a.deployment, "service:"+a.name, nil, func([]byte) error { return nil })
	}()
	return done
}

func startGPUApply(t *testing.T, f *poolFixture, plan, name string, d *gpuDaemon, hold bool) gpuApply {
	t.Helper()
	executor := newGPUDaemonExecutor(f, d, hold)
	runner := poolTestRunner(f, executor)
	dep, _, err := runner.Start(t.Context(), "org", plan)
	if err != nil {
		t.Fatalf("start %s: %v", plan, err)
	}
	return gpuApply{name: name, deployment: dep, runner: runner, executor: executor}
}

// withServiceGPU gives a plan's service the runtime GPU request planning
// writes, the one the server runtime turns into its GPU label.
func withServiceGPU(t *testing.T, f *poolFixture, plan string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"kind": "nvidia-l4", "count": 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,gpu}',$2::jsonb) WHERE id=$1`, plan, raw); err != nil {
		t.Fatal(err)
	}
}

// directGPUPlan writes and approves a direct GPU plan on the GPU daemon.
func directGPUPlan(t *testing.T, f *poolFixture, plan, env, name string) {
	t.Helper()
	r := f.request(env, name)
	f.plan(t, plan, r, &cloudinfra.Placement{ConnectionID: "runtime-gpu"})
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(jsonb_set(jsonb_set(result,'{actions,0,service,cloud_placement}','null'),'{actions,0,connection_id}','"runtime-gpu"'),'{actions,0,service,target}','"runtime-gpu"') WHERE id=$1`, plan); err != nil {
		t.Fatal(err)
	}
	withServiceGPU(t, f, plan)
	f.accept(t, plan)
}

// pooledGPUPlan writes and approves a plan that reserves one L4 in the pool.
func pooledGPUPlan(t *testing.T, f *poolFixture, plan, env, name string) {
	t.Helper()
	r := gpuRequest(f, name)
	r.EnvironmentID = env
	p, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil || p.ServerID != "server-gpu" || p.GPUs != 1 {
		t.Fatalf("GPU placement %s: %+v %v", plan, p, err)
	}
	gpuPlan(t, f, plan, r, p)
	withServiceGPU(t, f, plan)
	f.accept(t, plan)
}

func dropGPUMembers(t *testing.T, f *poolFixture) {
	t.Helper()
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members WHERE server_id='server-gpu'`); err != nil {
		t.Fatal(err)
	}
}

// waitDaemonWaiter waits until a session queues on the daemon's
// admission lock or its GPU lock.
func waitDaemonWaiter(t *testing.T, f *poolFixture) {
	t.Helper()
	waitPoolSQL(t, f, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory' AND (query LIKE '%host-admission%' OR query LIKE '%gpu-devices%'))`)
}

func entered(e *gpuDaemonExecutor, within time.Duration) bool {
	select {
	case <-e.entered:
		return true
	case <-time.After(within):
		return false
	}
}

// serializeGPUApplies runs first until it has read a free device, then
// runs second. Second must wait on the daemon's admission lock (two direct
// applies) or its GPU lock until first has written its label, and then
// take the other device.
func serializeGPUApplies(t *testing.T, f *poolFixture, d *gpuDaemon, first, second gpuApply) {
	t.Helper()
	firstDone := first.run(t)
	if !entered(first.executor, 5*time.Second) {
		t.Fatal("first GPU apply never reached the daemon")
	}
	secondDone := second.run(t)
	if entered(second.executor, 300*time.Millisecond) {
		close(first.executor.release)
		<-firstDone
		<-secondDone
		t.Fatalf("second GPU apply read the daemon's devices while the first held one unlabelled: %v", d.devices())
	}
	waitDaemonWaiter(t, f)
	close(first.executor.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first GPU apply: %v", err)
	}
	if !entered(second.executor, 5*time.Second) {
		t.Fatal("second GPU apply never reached the daemon after the first finished")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second GPU apply: %v", err)
	}
	got := d.devices()
	if got[first.name] != "GPU-0" || got[second.name] != "GPU-1" {
		t.Fatalf("second GPU apply missed the first's device label: %v", got)
	}
}

func TestGPUAdmissionSerializesDirectApplies(t *testing.T) {
	f := newGPUPoolFixture(t)
	dropGPUMembers(t, f)
	directGPUPlan(t, f, "direct-a", "env", "llm-a")
	directGPUPlan(t, f, "direct-b", "stage", "llm-b")
	d := &gpuDaemon{labels: map[string]string{}}
	serializeGPUApplies(t, f, d, startGPUApply(t, f, "direct-a", "llm-a", d, true), startGPUApply(t, f, "direct-b", "llm-b", d, false))
}

func TestGPUAdmissionSerializesPooledApplies(t *testing.T) {
	f := newGPUPoolFixture(t)
	pooledGPUPlan(t, f, "pooled-a", "env", "llm-a")
	pooledGPUPlan(t, f, "pooled-b", "stage", "llm-b")
	d := &gpuDaemon{labels: map[string]string{}}
	serializeGPUApplies(t, f, d, startGPUApply(t, f, "pooled-a", "llm-a", d, true), startGPUApply(t, f, "pooled-b", "llm-b", d, false))
}

// A direct apply already holding the daemon makes a pooled GPU apply on
// the same daemon wait, and the pooled one then takes the other device.
// The direct plan was approved before the server joined the pool, and the
// pool membership is restored only while the direct apply runs.
func TestGPUAdmissionSerializesPooledAfterDirect(t *testing.T) {
	f := newGPUPoolFixture(t)
	pooledGPUPlan(t, f, "pooled", "env", "llm-a")
	var member []byte
	if err := f.sql.QueryRow(t.Context(), `SELECT to_jsonb(m) FROM loomplane_server_pool_members m WHERE server_id='server-gpu'`).Scan(&member); err != nil {
		t.Fatal(err)
	}
	dropGPUMembers(t, f)
	directGPUPlan(t, f, "direct", "stage", "llm-b")
	d := &gpuDaemon{labels: map[string]string{}}
	direct := startGPUApply(t, f, "direct", "llm-b", d, true)
	directDone := direct.run(t)
	if !entered(direct.executor, 5*time.Second) {
		t.Fatal("direct GPU apply never reached the daemon")
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_server_pool_members SELECT * FROM jsonb_populate_record(NULL::loomplane_server_pool_members,$1::jsonb)`, member); err != nil {
		t.Fatal(err)
	}
	pooled := startGPUApply(t, f, "pooled", "llm-a", d, false)
	pooledDone := pooled.run(t)
	if entered(pooled.executor, 300*time.Millisecond) {
		close(direct.executor.release)
		<-directDone
		<-pooledDone
		t.Fatalf("pooled GPU apply read the daemon's devices while a direct apply held one unlabelled: %v", d.devices())
	}
	waitDaemonWaiter(t, f)
	close(direct.executor.release)
	if err := <-directDone; err != nil {
		t.Fatalf("direct GPU apply: %v", err)
	}
	if err := <-pooledDone; err != nil {
		t.Fatalf("pooled GPU apply: %v", err)
	}
	if got := d.devices(); got["llm-b"] != "GPU-0" || got["llm-a"] != "GPU-1" {
		t.Fatalf("pooled GPU apply missed the direct apply's device label: %v", got)
	}
}

// A direct apply on a pooled GPU daemon is refused for the pooled host
// while a pooled apply holds the daemon's GPUs, without touching a device
// and without waiting on the pooled apply: pooled applies hold no
// admission lock. The direct plan was approved and started before the
// server joined the pool.
func TestGPUAdmissionRefusesDirectApplyOnPooledDaemon(t *testing.T) {
	f := newGPUPoolFixture(t)
	var member []byte
	if err := f.sql.QueryRow(t.Context(), `SELECT to_jsonb(m) FROM loomplane_server_pool_members m WHERE server_id='server-gpu'`).Scan(&member); err != nil {
		t.Fatal(err)
	}
	dropGPUMembers(t, f)
	directGPUPlan(t, f, "direct", "stage", "llm-b")
	d := &gpuDaemon{labels: map[string]string{}}
	direct := startGPUApply(t, f, "direct", "llm-b", d, false)
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_server_pool_members SELECT * FROM jsonb_populate_record(NULL::loomplane_server_pool_members,$1::jsonb)`, member); err != nil {
		t.Fatal(err)
	}
	pooledGPUPlan(t, f, "pooled", "env", "llm-a")
	pooled := startGPUApply(t, f, "pooled", "llm-a", d, true)
	pooledDone := pooled.run(t)
	if !entered(pooled.executor, 5*time.Second) {
		t.Fatal("pooled GPU apply never reached the daemon")
	}
	directDone := direct.run(t)
	select {
	case err := <-directDone:
		if err == nil || !strings.Contains(err.Error(), "shared host requires a reviewed pool reservation") {
			close(pooled.executor.release)
			<-pooledDone
			t.Fatalf("direct apply on a pooled daemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(pooled.executor.release)
		<-pooledDone
		t.Fatal("direct apply on a pooled daemon waited on the pooled GPU apply")
	}
	if entered(direct.executor, 0) {
		t.Fatal("direct apply reached a pooled daemon")
	}
	close(pooled.executor.release)
	if err := <-pooledDone; err != nil {
		t.Fatalf("pooled GPU apply: %v", err)
	}
	if got := d.devices(); got["llm-a"] != "GPU-0" || got["llm-b"] != "" {
		t.Fatalf("device labels: %v", got)
	}
}

// pausingAccounts holds the first credential read of one connection until
// released. The runner reads the target's credential after admission and
// before any step that takes the organization lock, so a pause here sits
// where 56fca942 left a pooled GPU apply holding the daemon.
// It pauses only once armed, after the deployment has started.
type pausingAccounts struct {
	syncAccounts
	connection string
	armed      atomic.Bool
	entered    chan struct{}
	release    chan struct{}
}

func (a *pausingAccounts) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	if id == a.connection {
		if a.armed.CompareAndSwap(true, false) {
			close(a.entered)
			select {
			case <-a.release:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
	}
	return a.syncAccounts.Credential(ctx, org, id)
}

// A pooled GPU apply mustn't hold anything the organization's lock users
// wait on. Saving the GPU pool and approving another plan on its daemon
// both take the organization lock and then the daemon's connection rows.
// They run while a pooled GPU apply is paused, and then the apply goes on.
// Paused before its own organization-lock steps, where 56fca942 had it
// holding the daemon's admission lock, all of it has to finish: the apply
// must not wait on them while they wait on it. Paused holding the daemon's
// GPU lock, they have to finish before the apply goes on.
func TestGPUAdmissionPooledApplyLetsOrganizationLockUsersThrough(t *testing.T) {
	others := []struct {
		name  string
		setup func(t *testing.T, f *poolFixture)
		run   func(ctx context.Context, f *poolFixture) error
	}{
		{"pool save", func(*testing.T, *poolFixture) {}, func(ctx context.Context, f *poolFixture) error {
			pool, err := f.store.GetPool(ctx, "org", "cloud", "gpu-pool")
			if err != nil {
				return err
			}
			pool.Name = "L4 renamed"
			return f.store.SavePool(ctx, pool, pool.Revision)
		}},
		{"plan approval", func(t *testing.T, f *poolFixture) {
			r := gpuRequest(f, "llm-b")
			r.EnvironmentID = "stage"
			p, err := f.store.PreviewPlacement(t.Context(), r)
			if err != nil || p.ServerID != "server-gpu" {
				t.Fatalf("second GPU placement: %+v %v", p, err)
			}
			gpuPlan(t, f, "second", r, p)
			withServiceGPU(t, f, "second")
		}, func(ctx context.Context, f *poolFixture) error {
			ok, err := f.plans.Decide(ctx, "org", "second", "approved", "usr", "", time.Now())
			if err == nil && !ok {
				err = errors.New("approval was refused")
			}
			return err
		}},
	}
	for _, pause := range []string{"before effects", "holding devices"} {
		for _, other := range others {
			t.Run(pause+"/"+other.name, func(t *testing.T) {
				f := newGPUPoolFixture(t)
				pooledGPUPlan(t, f, "pooled", "env", "llm-a")
				other.setup(t, f)
				d := &gpuDaemon{labels: map[string]string{}}
				holdDevices := pause == "holding devices"
				executor := newGPUDaemonExecutor(f, d, holdDevices)
				accounts := &pausingAccounts{syncAccounts: syncAccounts{store: connections.NewPgStore(f.db)}, connection: "runtime-gpu", entered: make(chan struct{}), release: make(chan struct{})}
				runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: accounts, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
				dep, _, err := runner.Start(t.Context(), "org", "pooled")
				if err != nil {
					t.Fatal(err)
				}
				entered, release := accounts.entered, accounts.release
				if holdDevices {
					entered, release = executor.entered, executor.release
				} else {
					accounts.armed.Store(true)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				applyDone := make(chan error, 1)
				go func() {
					applyDone <- runner.Run(ctx, "org", dep, "service:llm-a", nil, func([]byte) error { return nil })
				}()
				select {
				case <-entered:
				case err = <-applyDone:
					t.Fatalf("pooled GPU apply finished before its pause: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("pooled GPU apply never reached its pause")
				}
				otherDone := make(chan error, 1)
				go func() { otherDone <- other.run(ctx, f) }()
				var otherErr error
				otherFinished := false
				if holdDevices {
					select {
					case otherErr = <-otherDone:
						otherFinished = true
					case <-time.After(15 * time.Second):
						cancel()
						t.Fatalf("%s waited on a pooled GPU apply holding the daemon's devices\n%s", other.name, lockReport(f))
					}
				} else {
					otherFinished, otherErr = settleOrQueue(t, f, otherDone)
				}
				close(release)
				timeout := time.After(15 * time.Second)
				var applyErr error
				applyFinished := false
				for !applyFinished || !otherFinished {
					select {
					case applyErr = <-applyDone:
						applyFinished = true
					case otherErr = <-otherDone:
						otherFinished = true
					case <-timeout:
						report := lockReport(f)
						cancel()
						t.Fatalf("deadlock: apply finished %v, %s finished %v after 15s\n%s", applyFinished, other.name, otherFinished, report)
					}
				}
				if applyErr != nil {
					t.Fatalf("pooled GPU apply: %v", applyErr)
				}
				if otherErr != nil {
					t.Fatalf("%s: %v", other.name, otherErr)
				}
				if got := d.devices(); got["llm-a"] != "GPU-0" {
					t.Fatalf("device labels: %v", got)
				}
			})
		}
	}
}

// settleOrQueue waits until the other transaction finishes or some session
// in the test database queues on a lock.
func settleOrQueue(t *testing.T, f *poolFixture, done <-chan error) (bool, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return true, err
		default:
		}
		var waiting bool
		if err := f.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return false, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false, nil
}

// lockReport lists the test database's other sessions and what they wait on.
func lockReport(f *poolFixture) string {
	rows, err := f.sql.Query(context.Background(), `SELECT state||' '||coalesce(wait_event_type,'')||' '||coalesce(wait_event,'')||': '||left(query,120) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`)
	if err != nil {
		return err.Error()
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if rows.Scan(&line) == nil {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
