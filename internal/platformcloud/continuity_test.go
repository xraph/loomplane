//go:build integration

package platformcloud

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

// redeploy places name again, as a deploy's session does, and records the
// instance the VM runs under. It returns the new placement.
func (e *placerEnv) redeploy(t *testing.T, name string, vcpus, mem int) *Placement {
	t.Helper()
	p, err := e.svc.Reserve(t.Context(), serviceReq(name, vcpus, mem))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.setInstance(t.Context(), p.ID, "inst_"+name); err != nil {
		t.Fatal(err)
	}
	return e.placement(t, p.ID)
}

// hostSamples is a usage reader that answers each host with its own samples.
type hostSamples struct {
	mu sync.Mutex
	by map[string][]fc.UsageSample
}

func (h *hostSamples) set(by map[string][]fc.UsageSample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.by = by
}

func (h *hostSamples) usage(_ context.Context, hostID string) ([]fc.UsageSample, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.by[hostID], nil
}

func vmSample(p *Placement, vm, counter string, bytes uint64) fc.UsageSample {
	return fc.UsageSample{Scope: p.Scope(), VMID: vm, CounterID: counter, Bytes: bytes}
}

// sampleEgressSteps samples egress once a minute from 10:01, one step per
// minute, each step being what every host reports then, and runs between
// before the step it is keyed by. It then closes the hour.
func sampleEgressSteps(t *testing.T, e *placerEnv, usage *hostSamples, steps []map[string][]fc.UsageSample, between map[int]func()) {
	t.Helper()
	for i, step := range steps {
		if fn := between[i]; fn != nil {
			fn()
		}
		usage.set(step)
		if err := e.svc.SampleEgress(t.Context(), meterHour.Add(time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	usage.set(nil)
	if err := e.svc.SampleEgress(t.Context(), meterHour.Add(time.Hour+emitDelay)); err != nil {
		t.Fatal(err)
	}
}

// The old VM keeps sending while its service is redeployed on the same host:
// every byte either VM sent is billed once.
func TestEgressBilledOnceAcrossARedeployOnTheSameHost(t *testing.T) {
	e, m := meterEnv(t)
	a := e.readyHost(t, "a", bigAllotment)
	old := e.placement(t, e.running(t, "web").ID)
	usage := &hostSamples{}
	e.svc.usage = usage.usage
	var moved *Placement
	steps := []map[string][]fc.UsageSample{
		{a.ID: {vmSample(old, "vm-old", "c-old", 300)}},
		{a.ID: {vmSample(old, "vm-old", "c-old", 600), vmSample(old, "vm-new", "c-new", 50)}},
		{a.ID: {vmSample(old, "vm-old", "c-old", 1100), vmSample(old, "vm-new", "c-new", 200)}},
		{a.ID: {vmSample(old, "vm-new", "c-new", 400)}},
	}
	sampleEgressSteps(t, e, usage, steps, map[int]func(){
		1: func() { moved = e.redeploy(t, "web", 1, 512) },
		2: func() {
			if err := e.svc.Running(t.Context(), "org_a", moved.ID, "inst_web", 4); err != nil {
				t.Fatal(err)
			}
		},
	})
	if hostOf(t, moved) != a.ID {
		t.Fatalf("the redeploy moved to %s", hostOf(t, moved))
	}
	// The old VM sent 1100 bytes, the new one 400.
	if got, want := m.total(FeatureEgressGB), 1500/1e9; !near(got, want) {
		t.Fatalf("egress = %v, want %v", got, want)
	}
}

// A move between hosts: the old VM reports on its host, the new one on
// its own, under the same instance, with counters of their own.
func TestEgressBilledOnceAcrossAMove(t *testing.T) {
	e, m := meterEnv(t)
	a := e.readyHost(t, "a", bigAllotment)
	old := e.placement(t, e.running(t, "web").ID)
	b := e.readyHost(t, "b", bigAllotment)
	if _, err := e.svc.DrainHost(t.Context(), "", a.ID); err != nil {
		t.Fatal(err)
	}
	usage := &hostSamples{}
	e.svc.usage = usage.usage
	var moved *Placement
	steps := []map[string][]fc.UsageSample{
		{a.ID: {vmSample(old, "vm-old", "c-old", 300)}},
		{a.ID: {vmSample(old, "vm-old", "c-old", 600)}, b.ID: {vmSample(old, "vm-new", "c-new", 50)}},
		{a.ID: {vmSample(old, "vm-old", "c-old", 1100)}, b.ID: {vmSample(old, "vm-new", "c-new", 200)}},
		{b.ID: {vmSample(old, "vm-new", "c-new", 400)}},
	}
	sampleEgressSteps(t, e, usage, steps, map[int]func(){
		1: func() { moved = e.redeploy(t, "web", 1, 512) },
		2: func() {
			if err := e.svc.Running(t.Context(), "org_a", moved.ID, "inst_web", 4); err != nil {
				t.Fatal(err)
			}
		},
	})
	if hostOf(t, moved) != b.ID {
		t.Fatalf("the move went to %s, want %s", hostOf(t, moved), b.ID)
	}
	if got, want := m.total(FeatureEgressGB), 1500/1e9; !near(got, want) {
		t.Fatalf("egress = %v, want %v", got, want)
	}
}

// A redeploy that fails: the old VM keeps sending under the old placement.
func TestEgressBilledOnceAcrossAFailedRedeploy(t *testing.T) {
	e, m := meterEnv(t)
	a := e.readyHost(t, "a", bigAllotment)
	old := e.placement(t, e.running(t, "web").ID)
	usage := &hostSamples{}
	e.svc.usage = usage.usage
	var moved *Placement
	steps := []map[string][]fc.UsageSample{
		{a.ID: {vmSample(old, "vm-old", "c-old", 300)}},
		{a.ID: {vmSample(old, "vm-old", "c-old", 600), vmSample(old, "vm-new", "c-new", 50)}},
		{a.ID: {vmSample(old, "vm-old", "c-old", 900)}},
		{a.ID: {vmSample(old, "vm-old", "c-old", 1000)}},
	}
	sampleEgressSteps(t, e, usage, steps, map[int]func(){
		1: func() { moved = e.redeploy(t, "web", 1, 512) },
		2: func() {
			if err := e.svc.Abandon(t.Context(), "org_a", moved.ID); err != nil {
				t.Fatal(err)
			}
		},
	})
	if got, want := m.total(FeatureEgressGB), 1050/1e9; !near(got, want) {
		t.Fatalf("egress = %v, want %v", got, want)
	}
}

func sampleMinutes(t *testing.T, e *placerEnv, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		if err := e.svc.SampleCompute(t.Context(), meterHour.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
}

// A redeploy whose apply fails leaves the old placement running and billed;
// the new one is never billed.
func TestFailedRedeployKeepsTheOldPlacementRunningAndBilled(t *testing.T) {
	e, m := meterEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	old := e.running(t, "web")
	sampleMinutes(t, e, 1, 5)
	e.clock.set(meterHour.Add(5*time.Minute + 30*time.Second))
	moved := e.redeploy(t, "web", 2, 1024)
	if moved.Replaces != old.ID || e.placementState(t, old.ID) != stateRunning {
		t.Fatalf("the redeploy = %+v, old is %s", moved, e.placementState(t, old.ID))
	}
	sampleMinutes(t, e, 6, 10)
	e.clock.set(meterHour.Add(10*time.Minute + 30*time.Second))
	if err := e.svc.Abandon(ctx, "org_a", moved.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.placement(t, moved.ID); got.State != stateReleased {
		t.Fatalf("the abandoned placement = %+v", got)
	}
	if got := e.placement(t, old.ID); got.State != stateRunning {
		t.Fatalf("the old placement = %+v, want it running", got)
	}
	// Abandon changes nothing of a placement that replaces nothing.
	if err := e.svc.Abandon(ctx, "org_a", old.ID); err != nil || e.placementState(t, old.ID) != stateRunning {
		t.Fatalf("abandon of the current placement: %v, %s", err, e.placementState(t, old.ID))
	}
	sampleMinutes(t, e, 11, 20)
	closeHours(t, e, old.ID, meterHour.Add(2*time.Hour))
	if got, want := m.total(FeatureComputeVCPUHours), 20.0/60; !near(got, want) {
		t.Fatalf("vcpu hours = %v, want %v (20 minutes of the old placement)", got, want)
	}
	for _, id := range m.ids() {
		if strings.Contains(id, moved.ID) {
			t.Fatalf("the replacement that never ran was billed: %v", m.ids())
		}
	}
}

// A redeploy that succeeds bills the old placement up to the minute the new
// one runs, even with the sampler behind, and the new one from the next.
func TestRedeployBillsWithoutAGapOrADoubleMinute(t *testing.T) {
	e, m := meterEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	old := e.running(t, "web") // 1 vCPU, generation 3, since 10:00
	sampleMinutes(t, e, 1, 5)
	e.clock.set(meterHour.Add(5*time.Minute + 10*time.Second))
	moved := e.redeploy(t, "web", 2, 1024)
	// The sampler misses 10:06 and 10:07.
	e.clock.set(meterHour.Add(7*time.Minute + 30*time.Second))
	if err := e.svc.Running(ctx, "org_a", moved.ID, "inst_web", 4); err != nil {
		t.Fatal(err)
	}
	if got := e.placement(t, old.ID); got.State != stateReleased {
		t.Fatalf("the replaced placement = %+v", got)
	}
	if got := e.placement(t, moved.ID); got.State != stateRunning || got.Replaces != "" {
		t.Fatalf("the new placement = %+v", got)
	}
	sampleMinutes(t, e, 8, 10)
	closeHours(t, e, moved.ID, meterHour.Add(2*time.Hour))
	hour := meterHour.Format(time.RFC3339)
	m.mu.Lock()
	oldHours := m.byID["platform:"+old.ID+":3:"+hour+":"+FeatureComputeVCPUHours].Quantity
	newHours := m.byID["platform:"+moved.ID+":4:"+hour+":"+FeatureComputeVCPUHours].Quantity
	m.mu.Unlock()
	if !near(oldHours, 7.0/60) {
		t.Fatalf("old vcpu hours = %v, want 7 minutes at 1 vCPU", oldHours)
	}
	if !near(newHours, 2*3.0/60) {
		t.Fatalf("new vcpu hours = %v, want 3 minutes at 2 vCPUs", newHours)
	}
	if got, want := m.total(FeatureComputeVCPUHours), 13.0/60; !near(got, want) {
		t.Fatalf("vcpu hours = %v, want %v", got, want)
	}
}

// A suspension during a move stops the old VM and ends the move: the old
// placement is suspended and remembered, the new one released and its VM
// removed, and a late report that it runs changes nothing.
func TestSuspensionDuringARedeployStopsBoth(t *testing.T) {
	e, ctl := suspendEnv(t)
	ctx := t.Context()
	old, moved := moveWeb(t, e)
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if got := e.placement(t, old.ID); got.State != stateSuspended || !got.RestoreRunning {
		t.Fatalf("the old placement = %+v", got)
	}
	if got := e.placement(t, moved.ID); got.State != stateReleased || got.InstanceID != "inst_web" {
		t.Fatalf("the replacement = %+v", got)
	}
	if n := countPlacements(t, e.pg, `state IN ('running','reserved')`); n != 0 {
		t.Fatalf("%d placements still reserved or running", n)
	}
	if err := e.svc.Running(ctx, "org_a", moved.ID, "inst_web", 4); !errors.Is(err, ErrSuspended) {
		t.Fatalf("a late report of the replacement = %v", err)
	}
	if got := e.placement(t, moved.ID); got.State != stateReleased {
		t.Fatalf("the late report revived the replacement: %+v", got)
	}
	ctl.mu.Lock()
	stops := len(ctl.stops)
	ctl.mu.Unlock()
	if stops == 0 {
		t.Fatal("nothing was stopped")
	}
	e.age(t, `resource='web'`)
	if err := e.svc.SweepLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.removes) != 1 {
		t.Fatalf("removes = %v, want the replacement's VM", ctl.removes)
	}
	if got := e.placement(t, old.ID); got.State != stateSuspended || got.HostID == nil || got.InstanceID != "inst_web" {
		t.Fatalf("the sweep touched the suspended placement: %+v", got)
	}
}

// While both VMs exist the workspace's limits count only the new size, and
// the host counts both.
func TestRedeployLimitsCountTheNewSizeAndTheHostCountsBoth(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits) // 4 vCPUs
	h := e.readyHost(t, "a", Allotment{VCPUs: 8, MemoryMiB: 8192, DiskMiB: 8192, VMs: 4})
	ctx := t.Context()
	old, err := e.svc.Reserve(ctx, serviceReq("web", 3, 2048))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Running(ctx, "org_a", old.ID, "inst_web", 1); err != nil {
		t.Fatal(err)
	}
	resized := e.redeploy(t, "web", 4, 3072)
	if hostOf(t, resized) != h.ID || resized.Replaces != old.ID {
		t.Fatalf("resized = %+v", resized)
	}
	use, err := e.store.TenantUse(ctx, "org_a")
	if err != nil || use.VCPUs != 4 || use.VMs != 1 {
		t.Fatalf("TenantUse = %+v, %v", use, err)
	}
	hu, err := e.store.HostUse(ctx, h.ID)
	if err != nil || hu.VCPUs != 7 || hu.VMs != 2 {
		t.Fatalf("HostUse = %+v, %v", hu, err)
	}
	if _, err = e.svc.Reserve(ctx, serviceReq("worker", 1, 512)); !errors.Is(err, ErrLimit) {
		t.Fatalf("another service past the limit: %v", err)
	}
	// A host with no room for both keeps the redeploy off it.
	if _, err = e.svc.Reserve(ctx, serviceReq("web", 4, 6200)); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("a redeploy that fits only without the old VM: %v", err)
	}
	if err = e.svc.Abandon(ctx, "org_a", resized.ID); err != nil {
		t.Fatal(err)
	}
	if use, err = e.store.TenantUse(ctx, "org_a"); err != nil || use.VCPUs != 3 || use.VMs != 1 {
		t.Fatalf("TenantUse after the abandon = %+v, %v", use, err)
	}
	if hu, err = e.store.HostUse(ctx, h.ID); err != nil || hu.VCPUs != 3 || hu.VMs != 1 {
		t.Fatalf("HostUse after the abandon = %+v, %v", hu, err)
	}
	if got := e.placement(t, old.ID); got.State != stateRunning {
		t.Fatalf("old = %+v", got)
	}
}

// lockingRemove does what a real VM removal does to the locks: it issues
// maintenance authority, which takes the organization's lock, and a
// projection refresh or an enable may be waiting on that lock for the
// tenant row. It runs both, on connections of their own, before it removes.
type lockingRemove struct {
	*fakeControl
	svc     *Service
	regions []Region
	errs    []error
	claimed bool
}

func (l *lockingRemove) RemoveVM(ctx context.Context, p Placement) error {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var claim *time.Time
	if err := l.svc.store.db.QueryRow(c, `SELECT starting_until FROM loomplane_platform_placements WHERE id=$1`, p.ID).Scan(&claim); err != nil {
		l.errs = append(l.errs, err)
	}
	l.claimed = claim != nil
	l.errs = append(l.errs, l.svc.refreshProjection(c, p.OrgID, l.regions))
	if _, err := l.svc.EnableTenant(c, p.OrgID, nil); err != nil {
		l.errs = append(l.errs, err)
	}
	l.errs = append(l.errs, l.svc.store.withTx(c, func(tx driverTx) error { return cloudprofiles.LockOrganization(c, tx, p.OrgID) }))
	return l.fakeControl.RemoveVM(ctx, p)
}

func TestReclaimDoesNotHoldTheTenantRowWhileRemoving(t *testing.T) {
	e, ctl := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	t0 := time.Now().UTC()
	e.svc.now = func() time.Time { return t0 }
	web := e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	regions, err := e.svc.activeRegions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lr := &lockingRemove{fakeControl: ctl, svc: e.svc, regions: regions}
	e.svc.WithInstances(lr)
	done := make(chan error, 1)
	go func() { done <- e.svc.Reclaim(ctx, t0.Add(8*24*time.Hour)) }()
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the reclaim stalled")
	}
	if err != nil {
		t.Fatal(err)
	}
	if joined := errors.Join(lr.errs...); joined != nil {
		t.Fatalf("what ran during the removal: %v", joined)
	}
	if !lr.claimed {
		t.Fatal("the placement was not claimed while its VM was removed")
	}
	got := e.placement(t, web.ID)
	if got.State != stateSuspended || !got.RestoreRunning || got.HostID != nil || len(ctl.removes) != 1 {
		t.Fatalf("removes = %v, web = %+v", ctl.removes, got)
	}
	var claim *time.Time
	if err = e.pg.QueryRow(ctx, `SELECT starting_until FROM loomplane_platform_placements WHERE id=$1`, web.ID).Scan(&claim); err != nil || claim != nil {
		t.Fatalf("the reclaim's claim was left: %v, %v", claim, err)
	}
}
