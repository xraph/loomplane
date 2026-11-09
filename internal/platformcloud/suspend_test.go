//go:build integration

package platformcloud

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/metering"
)

type fakeControl struct {
	mu      sync.Mutex
	stops   []string
	starts  []string
	removes []string
	cancels int
	failing map[string]bool
	// onStart, when set, is what a start does: a real one reserves and
	// applies through the deploy path.
	onStart func(ctx context.Context, id string) error
}

func (f *fakeControl) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing[id] {
		return errors.New("stop failed")
	}
	f.stops = append(f.stops, id)
	return nil
}

func (f *fakeControl) Start(ctx context.Context, id string) error {
	f.mu.Lock()
	on := f.onStart
	f.mu.Unlock()
	if on != nil {
		if err := on(ctx, id); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing["start:"+id] {
		return errors.New("start failed")
	}
	f.starts = append(f.starts, id)
	return nil
}

func (f *fakeControl) RemoveVM(_ context.Context, p Placement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, p.InstanceID)
	return nil
}

func (f *fakeControl) CancelBuilds(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return nil
}

var suspendLimits = Limits{VCPUs: 16, MemoryMiB: 16384, VMs: 10, ConcurrentBuilds: 4}

func suspendEnv(t *testing.T) (*placerEnv, *fakeControl) {
	t.Helper()
	e := newPlacerEnv(t, suspendLimits)
	fc := &fakeControl{failing: map[string]bool{}}
	e.svc.WithInstances(fc)
	return e, fc
}

// running places a service and records its VM as up.
func (e *placerEnv) running(t *testing.T, name string) *Placement {
	t.Helper()
	p, err := e.svc.Reserve(t.Context(), serviceReq(name, 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Running(t.Context(), "org_a", p.ID, "inst_"+name, 3); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *placerEnv) placement(t *testing.T, id string) *Placement {
	t.Helper()
	p, err := scanPlacement(e.pg.QueryRow(t.Context(), `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE id=$1`, id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *placerEnv) byResource(t *testing.T, name string) *Placement {
	t.Helper()
	var id string
	if err := e.pg.QueryRow(t.Context(), `SELECT id FROM loomplane_platform_placements WHERE org_id='org_a' AND resource=$1 AND state<>'released'`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return e.placement(t, id)
}

func TestSuspendStopsRunningAndRemembers(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	web := e.running(t, "web")
	userStopped := e.running(t, "worker")
	if err := e.svc.Stopped(ctx, "org_a", userStopped.ID); err != nil {
		t.Fatal(err)
	}
	build, err := e.svc.Reserve(ctx, buildReq("build-1"))
	if err != nil {
		t.Fatal(err)
	}
	e.setState(t, build.ID, "running")

	if err = e.svc.Suspend(ctx, "org_a", ReasonSpendingLimit); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Suspend(ctx, "org_a", ReasonSpendingLimit); err != nil {
		t.Fatal(err)
	}

	got := e.placement(t, web.ID)
	if got.State != "suspended" || !got.RestoreRunning || got.SuspendedAt == nil {
		t.Fatalf("web = %+v", got)
	}
	if w := e.placement(t, userStopped.ID); w.State != "stopped" || w.RestoreRunning {
		t.Fatalf("user-stopped service = %+v", w)
	}
	if !slices.Equal(fc.stops, []string{"inst_web"}) || fc.cancels != 1 {
		t.Fatalf("stops = %v, cancels = %d; want one stop of inst_web and one cancel", fc.stops, fc.cancels)
	}
	tenant, err := e.store.GetTenant(ctx, "org_a")
	if err != nil || len(tenant.SuspendReasons) != 1 || tenant.SuspendedAt == nil {
		t.Fatalf("tenant = %+v, %v", tenant, err)
	}
	if _, err = e.svc.Reserve(ctx, serviceReq("api", 1, 512)); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Reserve while suspended = %v, want ErrSuspended", err)
	}
}

func TestSuspendReleasesPlacementThatNeverStarted(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	p, err := e.svc.Reserve(t.Context(), serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Suspend(t.Context(), "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if got := e.placement(t, p.ID); got.State != "released" || len(fc.stops) != 0 {
		t.Fatalf("placement = %+v, stops = %v", got, fc.stops)
	}
}

func TestSuspendKeepsStoppingAfterAFailure(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	e.running(t, "a")
	e.running(t, "b")
	fc.failing["inst_a"] = true
	if err := e.svc.Suspend(t.Context(), "org_a", ReasonPastDue); err == nil {
		t.Fatal("Suspend hid a failed stop")
	}
	if e.byResource(t, "a").State != "running" || e.byResource(t, "b").State != "suspended" {
		t.Fatal("a failed stop should leave that service running and stop the other")
	}
	fc.failing["inst_a"] = false
	if err := e.svc.Suspend(t.Context(), "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if e.byResource(t, "a").State != "suspended" || !slices.Equal(fc.stops, []string{"inst_b", "inst_a"}) {
		t.Fatalf("stops = %v", fc.stops)
	}
}

func TestReasonsStack(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	e.running(t, "web")
	e.running(t, "api")
	for _, r := range []SuspendReason{ReasonPastDue, ReasonSpendingLimit} {
		if err := e.svc.Suspend(ctx, "org_a", r); err != nil {
			t.Fatal(err)
		}
	}
	if len(fc.stops) != 2 {
		t.Fatalf("stops = %v", fc.stops)
	}
	if err := e.svc.Restore(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if len(fc.starts) != 0 || e.byResource(t, "web").State != "suspended" {
		t.Fatalf("starts after the first restore = %v", fc.starts)
	}
	if err := e.svc.Restore(ctx, "org_a", ReasonSpendingLimit); err != nil {
		t.Fatal(err)
	}
	slices.Sort(fc.starts)
	if !slices.Equal(fc.starts, []string{"inst_api", "inst_web"}) {
		t.Fatalf("starts = %v, want each service once", fc.starts)
	}
	web := e.byResource(t, "web")
	if web.State != "running" || web.RestoreRunning || web.SuspendedAt != nil {
		t.Fatalf("web = %+v", web)
	}
	tenant, _ := e.store.GetTenant(ctx, "org_a")
	if len(tenant.SuspendReasons) != 0 || tenant.SuspendedAt != nil {
		t.Fatalf("tenant = %+v", tenant)
	}
	if err := e.svc.Restore(ctx, "org_a", ReasonSpendingLimit); err != nil || len(fc.starts) != 2 {
		t.Fatalf("restoring again started %v: %v", fc.starts, err)
	}
	if err := e.svc.RetryRestores(ctx); err != nil || len(fc.starts) != 2 {
		t.Fatalf("RetryRestores started %v: %v", fc.starts, err)
	}
}

func TestReclaimAfterSevenDays(t *testing.T) {
	e, fc := suspendEnv(t)
	h := e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	t0 := time.Now().UTC()
	e.svc.now = func() time.Time { return t0 }
	web := e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reclaim(ctx, t0.Add(6*24*time.Hour+23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(fc.removes) != 0 || e.placement(t, web.ID).HostID == nil {
		t.Fatalf("removed too early: %v", fc.removes)
	}
	for range 2 {
		if err := e.svc.Reclaim(ctx, t0.Add(7*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got := e.placement(t, web.ID)
	if !slices.Equal(fc.removes, []string{"inst_web"}) || got.HostID != nil || got.State != "suspended" || got.InstanceID != "inst_web" {
		t.Fatalf("removes = %v, placement = %+v", fc.removes, got)
	}
	use, err := e.store.TenantUse(ctx, "org_a")
	if err != nil || use.VMs != 1 || use.VCPUs != 1 {
		t.Fatalf("use = %+v, %v", use, err)
	}
	if hu, hErr := e.store.HostUse(ctx, h.ID); hErr != nil || hu.VMs != 0 {
		t.Fatalf("host use = %+v, %v", hu, hErr)
	}
}

func TestReclaimLeavesARestoredWorkspaceAlone(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	t0 := time.Now().UTC()
	e.svc.now = func() time.Time { return t0 }
	e.running(t, "web")
	if err := e.svc.Suspend(t.Context(), "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Restore(t.Context(), "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reclaim(t.Context(), t0.Add(30*24*time.Hour)); err != nil || len(fc.removes) != 0 {
		t.Fatalf("removes = %v, %v", fc.removes, err)
	}
}

// fillHost puts another organization's service on the host, taking its room.
func (e *placerEnv) fillHost(t *testing.T, host *Host, id string) {
	t.Helper()
	if _, err := e.pg.Exec(t.Context(), `INSERT INTO loomplane_platform_placements(id,org_id,project_id,environment_id,resource,kind,region_id,host_id,vcpus,memory_mib,disk_mib,state,created_at,updated_at)
VALUES($1,'org_b','prj_b','env_b','squatter','service','reg_1',$2,1,512,1024,'running',now(),now())`, id, host.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreAfterReclaimWaitsForCapacity(t *testing.T) {
	e, fc := suspendEnv(t)
	ctx := t.Context()
	tiny := Allotment{VCPUs: 64, MemoryMiB: 65536, DiskMiB: 1 << 20, VMs: 1}
	h := e.readyHost(t, "a", tiny)
	t0 := time.Now().UTC()
	e.svc.now = func() time.Time { return t0 }
	e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonSpendingLimit); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reclaim(ctx, t0.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.fillHost(t, h, "plc_squatter")

	if err := e.svc.Restore(ctx, "org_a", ReasonSpendingLimit); err != nil {
		t.Fatal(err)
	}
	web := e.byResource(t, "web")
	if !web.WaitingCapacity || web.State != "suspended" || web.HostID != nil || !web.RestoreRunning || len(fc.starts) != 0 {
		t.Fatalf("web = %+v, starts = %v", web, fc.starts)
	}
	if err := e.svc.RetryRestores(ctx); err != nil || len(fc.starts) != 0 {
		t.Fatalf("starts without room = %v, %v", fc.starts, err)
	}

	if _, err := e.pg.Exec(ctx, `UPDATE loomplane_platform_placements SET state='released' WHERE id='plc_squatter'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.svc.RetryRestores(ctx); err != nil {
			t.Fatal(err)
		}
	}
	web = e.byResource(t, "web")
	if !slices.Equal(fc.starts, []string{"inst_web"}) {
		t.Fatalf("starts = %v, want one start of inst_web", fc.starts)
	}
	if web.State != "running" || web.HostID == nil || web.WaitingCapacity || web.RestoreRunning || web.InstanceID != "inst_web" || web.Generation != 3 {
		t.Fatalf("web = %+v", web)
	}
}

func TestSuspendTouchesOnlyPlatformPlacements(t *testing.T) {
	e, fc := suspendEnv(t)
	h := e.readyHost(t, "a", bigAllotment)
	e.running(t, "web")
	e.fillHost(t, h, "plc_other_org")
	if err := e.svc.Suspend(t.Context(), "org_a", ReasonPlatformOwner); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fc.stops, []string{"inst_web"}) {
		t.Fatalf("stops = %v", fc.stops)
	}
	if other := e.placement(t, "plc_other_org"); other.State != "running" || other.RestoreRunning {
		t.Fatalf("another organization's placement = %+v", other)
	}
}

func TestDisableTenantStopsRunningWork(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	web := e.running(t, "web")
	for range 2 {
		if err := e.svc.DisableTenant(t.Context(), "org_a"); err != nil {
			t.Fatal(err)
		}
	}
	got := e.placement(t, web.ID)
	if got.State != "suspended" || !got.RestoreRunning || !slices.Equal(fc.stops, []string{"inst_web"}) {
		t.Fatalf("web = %+v, stops = %v", got, fc.stops)
	}
	if err := e.svc.Reclaim(t.Context(), time.Now().Add(8*24*time.Hour)); err != nil || len(fc.removes) != 1 {
		t.Fatalf("a disabled tenant's work is reclaimed too: %v, %v", fc.removes, err)
	}
	// Enabling clears the owner's reason; the retry brings the work back.
	if _, err := e.svc.EnableTenant(t.Context(), "org_a", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RetryRestores(t.Context()); err != nil || !slices.Equal(fc.starts, []string{"inst_web"}) {
		t.Fatalf("starts = %v, %v", fc.starts, err)
	}
}

func TestSuspendNeedsAnEnabledTenant(t *testing.T) {
	e, _ := suspendEnv(t)
	if err := e.svc.Suspend(t.Context(), "org_nobody", ReasonPastDue); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("Suspend = %v, want ErrNotEnabled", err)
	}
}

// reclaimed leaves org_a's "web" suspended without a host, then lifts the
// reasons by hand so only the cron has the restore to do.
func reclaimedWeb(t *testing.T, e *placerEnv) {
	t.Helper()
	ctx := t.Context()
	t0 := time.Now().UTC()
	e.svc.now = func() time.Time { return t0 }
	e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonSpendingLimit); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reclaim(ctx, t0.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pg.Exec(ctx, `UPDATE loomplane_platform_tenants SET suspend_reasons='{}',suspended_at=NULL WHERE org_id='org_a'`); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRetryRestoresStartOnce(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	e.readyHost(t, "b", bigAllotment)
	reclaimedWeb(t, e)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.svc.RetryRestores(t.Context())
		}()
	}
	wg.Wait()
	if err := e.svc.RetryRestores(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fc.starts, []string{"inst_web"}) {
		t.Fatalf("starts = %v, want exactly one", fc.starts)
	}
	if n := countPlacements(t, e.pg, `org_id='org_a' AND resource='web' AND state<>'released'`); n != 1 {
		t.Fatalf("%d live rows for web, want 1", n)
	}
	if web := e.byResource(t, "web"); web.State != "running" || web.HostID == nil {
		t.Fatalf("web = %+v", web)
	}
}

func TestPlaceAgainIsOneStepAndSurvivesASuspend(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	reclaimedWeb(t, e)
	ctx := t.Context()
	old := e.byResource(t, "web")
	placed, err := e.svc.placeAgain(ctx, *old)
	if err != nil || placed == nil {
		t.Fatalf("placeAgain = %+v, %v", placed, err)
	}
	// One row for the service, already remembered, with the old release.
	if n := countPlacements(t, e.pg, `org_id='org_a' AND resource='web' AND state<>'released'`); n != 1 {
		t.Fatalf("%d live rows", n)
	}
	got := e.placement(t, placed.ID)
	if got.State != "suspended" || !got.RestoreRunning || got.HostID == nil || got.InstanceID != "inst_web" || got.Generation != 3 || got.SuspendedAt == nil {
		t.Fatalf("new row = %+v", got)
	}
	if e.placement(t, old.ID).State != "released" {
		t.Fatal("old row not released")
	}
	// A suspend landing now leaves it remembered, and a restore starts it.
	if err = e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if got = e.placement(t, placed.ID); got.State != "suspended" || !got.RestoreRunning || len(fc.stops) != 1 {
		t.Fatalf("after suspend = %+v, stops = %v", got, fc.stops)
	}
	if err = e.svc.Restore(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fc.starts, []string{"inst_web"}) || e.placement(t, placed.ID).State != "running" {
		t.Fatalf("starts = %v", fc.starts)
	}
}

func TestPlaceAgainRefusedLeavesTheRememberedRow(t *testing.T) {
	e, fc := suspendEnv(t)
	ctx := t.Context()
	h := e.readyHost(t, "a", Allotment{VCPUs: 64, MemoryMiB: 65536, DiskMiB: 1 << 20, VMs: 1})
	reclaimedWeb(t, e)
	e.fillHost(t, h, "plc_squatter")
	old := e.byResource(t, "web")
	if _, err := e.svc.placeAgain(ctx, *old); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("placeAgain = %v, want ErrNoCapacity", err)
	}
	if got := e.placement(t, old.ID); got.State != "suspended" || !got.RestoreRunning || len(fc.starts) != 0 {
		t.Fatalf("old row = %+v", got)
	}
}

type planGate struct{}

func (planGate) CanRun(context.Context, string, metering.Kind, bool) error {
	return errors.New("over the plan")
}

func TestRestoreRefusedForAnotherReasonWaitsQuietly(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	reclaimedWeb(t, e)
	e.svc.WithGate(planGate{})
	for range 2 {
		if err := e.svc.RetryRestores(t.Context()); err != nil {
			t.Fatalf("RetryRestores = %v, want a quiet wait", err)
		}
	}
	if web := e.byResource(t, "web"); !web.WaitingCapacity || web.State != "suspended" || len(fc.starts) != 0 {
		t.Fatalf("web = %+v", web)
	}
}

func TestLateDeployCannotResurrectSuspendedWork(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	q := e.running(t, "api")
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	fc.stops = nil
	if err := e.svc.Running(ctx, "org_a", q.ID, "inst_api2", 9); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Running while suspended = %v, want ErrSuspended", err)
	}
	got := e.placement(t, q.ID)
	if got.State != "suspended" || !got.RestoreRunning || got.InstanceID != "inst_api2" || !slices.Equal(fc.stops, []string{"inst_api2"}) {
		t.Fatalf("placement = %+v, stops = %v", got, fc.stops)
	}
}

func TestReclaimStopsAgainWhatSuspensionMissed(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	e.running(t, "web")
	fc.failing["inst_web"] = true
	err := e.svc.Suspend(ctx, "org_a", ReasonPastDue)
	if err == nil {
		t.Fatal("Suspend hid a failed stop")
	}
	if strings.Contains(err.Error(), "stop failed") || strings.Contains(err.Error(), "inst_web") {
		t.Fatalf("error leaks details: %v", err)
	}
	fc.failing["inst_web"] = false
	if err = e.svc.Reclaim(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if web := e.byResource(t, "web"); web.State != "suspended" || !web.RestoreRunning || !slices.Equal(fc.stops, []string{"inst_web"}) {
		t.Fatalf("web = %+v, stops = %v", web, fc.stops)
	}
}

func TestSuspendWithoutControlStillRecordsTheReason(t *testing.T) {
	e, _ := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	e.svc.instances = nil
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatalf("nothing to stop, so no error: %v", err)
	}
	if _, err := e.svc.Reserve(ctx, serviceReq("api", 1, 512)); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Reserve = %v", err)
	}
	if err := e.svc.Restore(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	e.running(t, "web")
	if err := e.svc.DisableTenant(ctx, "org_a"); !errors.Is(err, errEffects) {
		t.Fatalf("DisableTenant = %v, want errEffects", err)
	}
	tenant, _ := e.store.GetTenant(ctx, "org_a")
	if tenant.Status != "disabled" || !slices.Contains(tenant.SuspendReasons, ReasonPlatformOwner) || tenant.SuspendedAt == nil {
		t.Fatalf("tenant = %+v", tenant)
	}
}

func TestRestoreRefusesThePlatformOwnerReason(t *testing.T) {
	e, _ := suspendEnv(t)
	if err := e.svc.DisableTenant(t.Context(), "org_a"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Restore(t.Context(), "org_a", ReasonPlatformOwner); !errors.Is(err, ErrConflict) {
		t.Fatalf("Restore = %v, want ErrConflict", err)
	}
	tenant, _ := e.store.GetTenant(t.Context(), "org_a")
	if len(tenant.SuspendReasons) != 1 {
		t.Fatalf("tenant = %+v", tenant)
	}
	if _, err := e.svc.EnableTenant(t.Context(), "org_a", nil); err != nil {
		t.Fatal(err)
	}
	if tenant, _ = e.store.GetTenant(t.Context(), "org_a"); len(tenant.SuspendReasons) != 0 || tenant.SuspendedAt != nil {
		t.Fatalf("tenant after enable = %+v", tenant)
	}
}

func TestSuspendOfAnOrgWithNoPlatformWorkCallsNothing(t *testing.T) {
	e, fc := suspendEnv(t)
	if err := e.svc.Suspend(t.Context(), "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if len(fc.stops)+len(fc.starts)+len(fc.removes)+fc.cancels != 0 {
		t.Fatalf("calls = %+v", fc)
	}
}

// slowRemove blocks RemoveVM until released, so a restore can be tried while
// a reclaim is removing the VM.
type slowRemove struct {
	*fakeControl
	entered chan struct{}
	release chan struct{}
}

func (s *slowRemove) RemoveVM(ctx context.Context, p Placement) error {
	close(s.entered)
	<-s.release
	return s.fakeControl.RemoveVM(ctx, p)
}

// A restore that comes while a reclaim removes the VM doesn't wait for it,
// and leaves the placement to the reclaim's claim; the retry then places it
// again and starts it.
func TestRestoreDuringAReclaimLeavesItTheClaim(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	t0 := time.Now().UTC()
	e.svc.now = func() time.Time { return t0 }
	e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	slow := &slowRemove{fakeControl: fc, entered: make(chan struct{}), release: make(chan struct{})}
	e.svc.WithInstances(slow)
	reclaimed := make(chan error, 1)
	go func() { reclaimed <- e.svc.Reclaim(ctx, t0.Add(8*24*time.Hour)) }()
	<-slow.entered
	restored := make(chan error, 1)
	go func() { restored <- e.svc.Restore(ctx, "org_a", ReasonPastDue) }()
	select {
	case err := <-restored:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(slow.release)
		t.Fatal("the restore waited for the reclaim's VM removal")
	}
	if web := e.byResource(t, "web"); web.State != "suspended" || len(fc.starts) != 0 {
		t.Fatalf("the restore started a placement being reclaimed: starts = %v, web = %+v", fc.starts, web)
	}
	close(slow.release)
	if err := <-reclaimed; err != nil {
		t.Fatal(err)
	}
	web := e.byResource(t, "web")
	if len(fc.removes) != 1 || web.State != "suspended" || !web.RestoreRunning || web.HostID != nil {
		t.Fatalf("after the reclaim: removes = %v, web = %+v", fc.removes, web)
	}
	if err := e.svc.RetryRestores(ctx); err != nil {
		t.Fatal(err)
	}
	web = e.byResource(t, "web")
	if web.State != "running" || web.HostID == nil || !slices.Equal(fc.starts, []string{"inst_web"}) {
		t.Fatalf("after the retry: starts = %v, web = %+v", fc.starts, web)
	}
}

func TestLateDeployOfAReleasedPlacementIsStopped(t *testing.T) {
	e, fc := suspendEnv(t)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	p, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if e.placement(t, p.ID).State != "released" {
		t.Fatal("the reserved row should be released")
	}
	if err = e.svc.Running(ctx, "org_a", p.ID, "inst_late", 1); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Running = %v, want ErrSuspended", err)
	}
	if !slices.Equal(fc.stops, []string{"inst_late"}) {
		t.Fatalf("stops = %v", fc.stops)
	}
}

// A start that fails leaves the placement suspended and remembered, so it is
// neither shown nor billed as running, and the next restore tries again.
func TestFailedStartLeavesTheRestoreWaitingAndUnbilled(t *testing.T) {
	e, fc := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	web := e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	fc.failing["start:inst_web"] = true
	fc.mu.Unlock()
	if err := e.svc.Restore(ctx, "org_a", ReasonPastDue); err == nil {
		t.Fatal("a failed start was reported as done")
	}
	got := e.placement(t, web.ID)
	if got.State != stateSuspended || !got.RestoreRunning {
		t.Fatalf("after a failed start = %+v, want suspended and remembered", got)
	}
	e.svc.WithMeter(newRecordingMeter())
	if err := e.svc.SampleCompute(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var minutes int
	if err := e.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_platform_meter_compute WHERE placement_id=$1`, web.ID).Scan(&minutes); err != nil {
		t.Fatal(err)
	}
	if minutes != 0 {
		t.Fatalf("a placement whose start failed was metered: %d rows", minutes)
	}

	fc.mu.Lock()
	delete(fc.failing, "start:inst_web")
	fc.mu.Unlock()
	if err := e.svc.RetryRestores(ctx); err != nil {
		t.Fatal(err)
	}
	if got = e.placement(t, web.ID); got.State != stateRunning || got.RestoreRunning {
		t.Fatalf("after the retry = %+v", got)
	}
	if !slices.Equal(fc.starts, []string{"inst_web"}) {
		t.Fatalf("starts = %v", fc.starts)
	}
}

// A start reserves through the deploy path before it applies. One whose
// apply fails after that reservation still leaves the service suspended and
// remembered, so the next restore starts it.
func TestStartThatFailsAfterReservingStaysRemembered(t *testing.T) {
	e, fc := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	fc.onStart = func(ctx context.Context, _ string) error {
		if _, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512)); err != nil {
			return err
		}
		return errors.New("image pull failed")
	}
	if err := e.svc.Restore(ctx, "org_a", ReasonPastDue); err == nil {
		t.Fatal("a failed apply was reported as started")
	}
	web := e.byResource(t, "web")
	if web.State != stateSuspended || !web.RestoreRunning || web.InstanceID != "inst_web" {
		t.Fatalf("after a failed apply = %+v, want suspended and remembered", web)
	}
	var claimed bool
	if err := e.pg.QueryRow(ctx, `SELECT starting_until IS NOT NULL FROM loomplane_platform_placements WHERE id=$1`, web.ID).Scan(&claimed); err != nil || claimed {
		t.Fatalf("the start's claim was left behind: %v %v", claimed, err)
	}

	fc.onStart = func(ctx context.Context, id string) error {
		p, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
		if err != nil {
			return err
		}
		return e.svc.Running(ctx, "org_a", p.ID, id, 5)
	}
	if err := e.svc.RetryRestores(ctx); err != nil {
		t.Fatal(err)
	}
	if web = e.byResource(t, "web"); web.State != stateRunning || web.RestoreRunning {
		t.Fatalf("after the retry = %+v", web)
	}
}

// A placement a start has claimed is left alone by a reclaim.
func TestReclaimSkipsAPlacementBeingStarted(t *testing.T) {
	e, fc := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	web := e.running(t, "web")
	if err := e.svc.Suspend(ctx, "org_a", ReasonPastDue); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pg.Exec(ctx, `UPDATE loomplane_platform_placements SET suspended_at=now()-interval '8 days',starting_until=now()+interval '1 hour' WHERE id=$1`, web.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reclaim(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(fc.removes) != 0 {
		t.Fatalf("a claimed placement was reclaimed: %v", fc.removes)
	}
	if _, err := e.pg.Exec(ctx, `UPDATE loomplane_platform_placements SET starting_until=now()-interval '1 minute' WHERE id=$1`, web.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reclaim(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(fc.removes) != 1 {
		t.Fatalf("a lapsed claim still blocked the reclaim: %v", fc.removes)
	}
}
