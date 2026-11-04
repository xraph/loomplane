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

	fc "github.com/xraph/loomplane/internal/firecracker"
)

// age makes every placement of the resource look unchanged for an hour, past
// the sweeps' grace.
func (e *placerEnv) age(t *testing.T, where string) {
	t.Helper()
	if _, err := e.pg.Exec(t.Context(), `UPDATE loomplane_platform_placements SET updated_at=now()-interval '1 hour' WHERE `+where); err != nil {
		t.Fatal(err)
	}
}

// moveWeb runs web on host a, drains a and places web again, on b, as a
// move does. It returns the old placement and the new one.
func moveWeb(t *testing.T, e *placerEnv) (old, moved *Placement) {
	t.Helper()
	ctx := t.Context()
	a := e.readyHost(t, "a", bigAllotment)
	old = e.running(t, "web")
	e.readyHost(t, "b", bigAllotment)
	if _, err := e.svc.DrainHost(ctx, "", a.ID); err != nil {
		t.Fatal(err)
	}
	moved, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.setInstance(ctx, moved.ID, "inst_web"); err != nil {
		t.Fatal(err)
	}
	return e.placement(t, old.ID), e.placement(t, moved.ID)
}

func TestSweepLeftoversWaitsForTheMoveThenRemovesTheOldVM(t *testing.T) {
	e, ctl := suspendEnv(t)
	ctx := t.Context()
	old, moved := moveWeb(t, e)
	if old.State != stateReleased || old.InstanceID == "" {
		t.Fatalf("old placement = %+v", old)
	}

	// Inside the grace nothing is touched.
	if err := e.svc.SweepLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.removes) != 0 {
		t.Fatalf("a fresh leftover was removed: %v", ctl.removes)
	}

	// The move's new placement hasn't run yet: the old VM may be the one
	// serving, so it stays.
	e.age(t, `resource='web'`)
	if err := e.svc.SweepLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.removes) != 0 {
		t.Fatalf("the leftover went while its move was still reserved: %v", ctl.removes)
	}

	if err := e.svc.Running(ctx, "org_a", moved.ID, "inst_web", 4); err != nil {
		t.Fatal(err)
	}
	e.age(t, `resource='web'`)
	if err := e.svc.SweepLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ctl.removes, []string{"inst_web"}) {
		t.Fatalf("removes = %v, want the old VM once", ctl.removes)
	}
	if got := e.placement(t, old.ID); got.InstanceID != "" || got.State != stateReleased {
		t.Fatalf("old placement after the sweep = %+v", got)
	}
	// Run again: nothing left.
	if err := e.svc.SweepLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.removes) != 1 {
		t.Fatalf("a second sweep removed again: %v", ctl.removes)
	}
}

func TestSweepLeftoversOnlyForgetsTheVMItsLivePlacementRuns(t *testing.T) {
	e, ctl := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	first := e.running(t, "web")
	// A redeploy that stays on the host and keeps the instance.
	again, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Running(ctx, "org_a", again.ID, "inst_web", 4); err != nil {
		t.Fatal(err)
	}
	e.age(t, `resource='web'`)
	if err = e.svc.SweepLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.removes) != 0 {
		t.Fatalf("the live VM was removed: %v", ctl.removes)
	}
	if got := e.placement(t, first.ID); got.InstanceID != "" {
		t.Fatalf("the twin was not forgotten: %+v", got)
	}
}

type redeployControl struct {
	*fakeControl
	mu        sync.Mutex
	redeploys []string
}

func (r *redeployControl) Redeploy(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.redeploys = append(r.redeploys, id)
	return nil
}

func TestRepairMissingVMsRedeploysOnlyWhatIsGone(t *testing.T) {
	e, ctl := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	e.running(t, "web")
	e.running(t, "api")
	// A deploy that failed: reserved, with an instance and no VM, and no
	// older VM a move left. Its deployment reports it; the sweep leaves it.
	failed, err := e.svc.Reserve(ctx, serviceReq("new", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.setInstance(ctx, failed.ID, "inst_new"); err != nil {
		t.Fatal(err)
	}
	rc := &redeployControl{fakeControl: ctl}
	e.svc.WithInstances(rc)
	e.svc.inspect = func(_ context.Context, _ string, scope fc.Scope) (fc.Record, error) {
		switch scope.Resource {
		case "inst_web", "inst_new":
			return fc.Record{}, fc.ErrMissing
		case "inst_api":
			return fc.Record{Scope: scope, State: "running", Current: &fc.VM{ID: "vm"}}, nil
		}
		return fc.Record{}, errors.New("unexpected scope " + scope.Resource)
	}
	// Fresh placements are left to the deploy still writing them.
	if err := e.svc.RepairMissingVMs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rc.redeploys) != 0 {
		t.Fatalf("redeployed inside the grace: %v", rc.redeploys)
	}
	e.age(t, `true`)
	if err := e.svc.RepairMissingVMs(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rc.redeploys, []string{"inst_web"}) {
		t.Fatalf("redeploys = %v, want only the missing VM", rc.redeploys)
	}

	// A host that can't be reached says nothing about the VM.
	rc.redeploys = nil
	e.svc.inspect = func(context.Context, string, fc.Scope) (fc.Record, error) { return fc.Record{}, fc.ErrUnavailable }
	if err := e.svc.RepairMissingVMs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rc.redeploys) != 0 {
		t.Fatalf("redeployed past an unreachable host: %v", rc.redeploys)
	}
}

func TestSweepHostnamesDeletesOnlyOrphans(t *testing.T) {
	e, _ := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	e.running(t, "web")
	for _, r := range []string{"web", "gone"} {
		if _, err := e.pg.Exec(ctx, `INSERT INTO loomplane_platform_hostnames(org_id,environment_id,resource,project_id,hostname,created_at,updated_at)
VALUES('org_a','env_1',$1,'prj_1',$1||'-x1.apps.example',now()-interval '1 hour',now()-interval '1 hour')`, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.SweepHostnames(ctx); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := e.pg.Query(ctx, `SELECT resource FROM loomplane_platform_hostnames ORDER BY resource`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r string
		if err = rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		left = append(left, r)
	}
	rows.Close()
	if !slices.Equal(left, []string{"web"}) {
		t.Fatalf("hostnames left = %v, want only the live service's", left)
	}
}

func TestCleanupGoneTenantsTakesEverythingOff(t *testing.T) {
	e, ctl := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	web := e.running(t, "web")
	orgs := []string{"org_a"}
	e.svc.WithOrgs(func(context.Context) ([]string, error) { return orgs, nil })
	// Without a per-organization check nothing is cleaned up at all.
	orgs = []string{"org_other"}
	if err := e.svc.CleanupGoneTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if countPlacements(t, e.pg, `state<>'released'`) != 1 {
		t.Fatal("cleaned up with no way to check the organization is gone")
	}
	// The list says gone, but the organization is back by the time its
	// turn comes: it is left alone.
	exists := true
	e.svc.WithOrgExists(func(context.Context, string) (bool, error) { return exists, nil })
	if err := e.svc.CleanupGoneTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if countPlacements(t, e.pg, `state<>'released'`) != 1 {
		t.Fatal("an organization the re-check found was cleaned up")
	}
	exists = false
	orgs = []string{"org_a"}

	// The organization still exists: nothing happens.
	if err := e.svc.CleanupGoneTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if countPlacements(t, e.pg, `state<>'released'`) != 1 {
		t.Fatal("a live organization's work was released")
	}
	// An empty list is not believed.
	orgs = nil
	if err := e.svc.CleanupGoneTenants(ctx); err == nil {
		t.Fatal("an empty organization list cleaned up")
	}
	if countPlacements(t, e.pg, `state<>'released'`) != 1 {
		t.Fatal("an empty organization list released work")
	}

	orgs = []string{"org_other"}
	if err := e.svc.CleanupGoneTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ctl.removes, []string{"inst_web"}) {
		t.Fatalf("removes = %v", ctl.removes)
	}
	if got := e.placement(t, web.ID); got.State != stateReleased || got.InstanceID != "" {
		t.Fatalf("placement after cleanup = %+v", got)
	}
	if _, err := e.store.GetTenant(ctx, "org_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant after cleanup: %v", err)
	}
	var clouds int
	if err := e.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_profiles WHERE org_id='org_a'`).Scan(&clouds); err != nil {
		t.Fatal(err)
	}
	if clouds != 0 {
		t.Fatalf("projection left: %d Clouds", clouds)
	}
}

func TestRecheckHostsChecksOnlyAgingHosts(t *testing.T) {
	e, _ := suspendEnv(t)
	ctx := t.Context()
	e.addHost(t, "fresh", "ready", time.Now(), bigAllotment)
	e.addHost(t, "aging", "ready", time.Now().Add(-EvidenceMaxAge/2-time.Minute), bigAllotment)
	e.addHost(t, "retired", "retired", time.Now().Add(-time.Hour), bigAllotment)
	// No secret store: a check fails before it reaches the host, which is
	// enough to see which hosts were checked.
	err := e.svc.RecheckHosts(ctx)
	if err == nil {
		t.Fatal("the aging host wasn't checked")
	}
	msg := err.Error()
	if !strings.Contains(msg, "host_aging") || strings.Contains(msg, "host_fresh") || strings.Contains(msg, "host_retired") {
		t.Fatalf("re-check error = %q, want only the aging host", msg)
	}
}

func TestWorkspaceViewHasNoHostAndCountsOnlyServices(t *testing.T) {
	e, _ := suspendEnv(t)
	ctx := t.Context()
	e.readyHost(t, "a", bigAllotment)
	e.running(t, "web")
	if _, err := e.svc.Reserve(ctx, buildReq("op_1")); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Workspace(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Enabled || len(v.Placements) != 1 || v.Placements[0].Service != "web" || v.Placements[0].Region != "eu-west" {
		t.Fatalf("workspace view = %+v", v)
	}
	none, err := e.svc.Workspace(ctx, "org_nobody")
	if err != nil {
		t.Fatal(err)
	}
	if none.Enabled || len(none.Placements) != 0 || none.Limits != defaultTestLimits {
		t.Fatalf("view of an organization with no tenant = %+v", none)
	}
}

func TestSetTenantDisablesAndKeepsNewLimits(t *testing.T) {
	e, _ := suspendEnv(t)
	ctx := t.Context()
	limits := Limits{VCPUs: 2, MemoryMiB: 1024, VMs: 2}
	tn, err := e.svc.SetTenant(ctx, "org_a", "disabled", &limits)
	if err != nil {
		t.Fatal(err)
	}
	if tn.Status != tenantDisabled || tn.Limits == nil || *tn.Limits != limits || !slices.Contains(tn.SuspendReasons, ReasonPlatformOwner) {
		t.Fatalf("tenant = %+v", tn)
	}
	tn, err = e.svc.SetTenant(ctx, "org_a", "enabled", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tn.Status != tenantEnabled || len(tn.SuspendReasons) != 0 || *tn.Limits != limits {
		t.Fatalf("re-enabled tenant = %+v", tn)
	}
	if _, err = e.svc.SetTenant(ctx, "org_a", "paused", nil); err == nil {
		t.Fatal("an unknown status was taken")
	}
}

// A move whose apply failed leaves the new placement reserved with no VM,
// and the old VM still serving. The repair redeploys it; the leftover sweep
// keeps the old VM meanwhile.
func TestRepairRedeploysAMoveThatNeverRan(t *testing.T) {
	e, ctl := suspendEnv(t)
	ctx := t.Context()
	_, moved := moveWeb(t, e)
	rc := &redeployControl{fakeControl: ctl}
	e.svc.WithInstances(rc)
	e.svc.inspect = func(_ context.Context, hostID string, _ fc.Scope) (fc.Record, error) {
		if hostID == *moved.HostID {
			return fc.Record{}, fc.ErrMissing
		}
		return fc.Record{State: "running"}, nil
	}
	e.age(t, `true`)
	if err := e.svc.RepairMissingVMs(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rc.redeploys, []string{"inst_web"}) {
		t.Fatalf("redeploys = %v", rc.redeploys)
	}
	if err := e.svc.SweepLeftovers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.removes) != 0 {
		t.Fatalf("the serving VM was removed while its move was unfinished: %v", ctl.removes)
	}
}

func TestHasRoomElsewhereAndPuttingAMoveBack(t *testing.T) {
	e, _ := suspendEnv(t)
	ctx := t.Context()
	a := e.readyHost(t, "a", bigAllotment)
	web := e.running(t, "web")
	if _, err := e.svc.DrainHost(ctx, "", a.ID); err != nil {
		t.Fatal(err)
	}
	room, err := e.svc.HasRoomElsewhere(ctx, *e.placement(t, web.ID))
	if err != nil || room {
		t.Fatalf("room with no other host = %v, %v", room, err)
	}
	e.readyHost(t, "tiny", Allotment{VCPUs: 1, MemoryMiB: 128, DiskMiB: 128, VMs: 1})
	if room, err = e.svc.HasRoomElsewhere(ctx, *e.placement(t, web.ID)); err != nil || room {
		t.Fatalf("room on a host too small = %v, %v", room, err)
	}
	b := e.readyHost(t, "b", bigAllotment)
	if room, err = e.svc.HasRoomElsewhere(ctx, *e.placement(t, web.ID)); err != nil || !room {
		t.Fatalf("room with a free host = %v, %v", room, err)
	}

	// A move that failed on b goes back to the draining host it came from.
	moved, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil || hostOf(t, moved) != b.ID {
		t.Fatalf("move = %+v, %v", moved, err)
	}
	back, err := e.svc.Reserve(WithKeepHost(ctx, a.ID), serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if hostOf(t, back) != a.ID {
		t.Fatalf("put back on %s, want %s", hostOf(t, back), a.ID)
	}
}

// A tenant whose projection is still referenced is marked blocked, shown so
// in the tenants list and left alone for an hour.
func TestCleanupBlockedByAReferenceBacksOff(t *testing.T) {
	e, _ := suspendEnv(t)
	ctx := t.Context()
	e.svc.WithOrgs(func(context.Context) ([]string, error) { return []string{"org_other"}, nil }).
		WithOrgExists(func(context.Context, string) (bool, error) { return false, nil })
	for _, q := range []string{
		`CREATE FUNCTION test_still_assigned() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'still assigned' USING ERRCODE = '23503'; END $$`,
		`CREATE TRIGGER zz_test_still_assigned BEFORE DELETE ON loomplane_cloud_profiles FOR EACH ROW EXECUTE FUNCTION test_still_assigned()`,
	} {
		if _, err := e.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.CleanupGoneTenants(ctx); err == nil {
		t.Fatal("a blocked cleanup reported success")
	}
	views, err := e.svc.Tenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(views, func(v TenantView) bool { return v.OrgID == "org_a" })
	if i < 0 || views[i].CleanupBlocked == nil || views[i].CleanupBlocked.Reason == "" || !views[i].CleanupBlocked.Until.After(time.Now()) {
		t.Fatalf("tenant view = %+v", views)
	}
	// Inside the back-off the next run leaves it alone, without an error.
	if err = e.svc.CleanupGoneTenants(ctx); err != nil {
		t.Fatalf("a backed-off tenant was tried again: %v", err)
	}
}
