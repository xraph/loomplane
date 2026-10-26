//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
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

// gpuDaemonExecutor reads a free device, reports that it entered, and
// holds its read until released before it writes the device label.
type gpuDaemonExecutor struct {
	poolExecutor
	daemon  *gpuDaemon
	entered chan struct{}
	release chan struct{}
}

func newGPUDaemonExecutor(d *gpuDaemon, hold bool) *gpuDaemonExecutor {
	e := &gpuDaemonExecutor{daemon: d, entered: make(chan struct{}), release: make(chan struct{})}
	if !hold {
		close(e.release)
	}
	return e
}

func (e *gpuDaemonExecutor) Apply(ctx context.Context, j *deployments.Job) (*deployments.Outcome, error) {
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
	executor := newGPUDaemonExecutor(d, hold)
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

// waitAdmissionWaiter waits until a session queues on an advisory lock.
func waitAdmissionWaiter(t *testing.T, f *poolFixture) {
	t.Helper()
	waitPoolSQL(t, f, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory' AND query LIKE '%host-admission%')`)
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
// runs second. Second must wait on the daemon's admission lock until
// first has written its label, and then take the other device.
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
	waitAdmissionWaiter(t, f)
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
	waitAdmissionWaiter(t, f)
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

// A direct apply on a pooled GPU daemon waits for the pooled apply that
// holds it, and is then refused for the pooled host without touching a
// device. The direct plan was approved and started before the server
// joined the pool.
func TestGPUAdmissionDirectWaitsForPooledApply(t *testing.T) {
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
		close(pooled.executor.release)
		<-pooledDone
		t.Fatalf("direct apply settled while a pooled GPU apply held the daemon: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	waitAdmissionWaiter(t, f)
	close(pooled.executor.release)
	if err := <-pooledDone; err != nil {
		t.Fatalf("pooled GPU apply: %v", err)
	}
	if err := <-directDone; err == nil || !strings.Contains(err.Error(), "shared host requires a reviewed pool reservation") {
		t.Fatalf("direct apply on a pooled daemon: %v", err)
	}
	if entered(direct.executor, 0) {
		t.Fatal("direct apply reached a pooled daemon")
	}
	if got := d.devices(); got["llm-a"] != "GPU-0" || got["llm-b"] != "" {
		t.Fatalf("device labels: %v", got)
	}
}
