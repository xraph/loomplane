//go:build integration

package platformcloud

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/metering"
)

type placerEnv struct {
	svc   *Service
	store *PgStore
	pg    *pgdriver.PgDB
	hosts []*Host
	clock *testClock
}

func newPlacerEnv(t *testing.T, limits Limits) *placerEnv {
	t.Helper()
	svc, s, pg := newTenantService(t)
	ctx := t.Context()
	for _, r := range []*Region{
		{ID: "reg_1", Name: "eu-west", DisplayName: "EU West", Status: "active"},
		{ID: "reg_closed", Name: "ap-south", DisplayName: "AP South", Status: "closed"},
	} {
		if err := s.CreateRegion(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.EnableTenant(ctx, "org_a", &limits); err != nil {
		t.Fatal(err)
	}
	return &placerEnv{svc: svc, store: s, pg: pg}
}

var bigAllotment = Allotment{VCPUs: 64, MemoryMiB: 65536, DiskMiB: 1 << 20, VMs: 100}

func (e *placerEnv) addHost(t *testing.T, name, status string, checked time.Time, a Allotment) *Host {
	t.Helper()
	h := &Host{ID: "host_" + name, RegionID: "reg_1", Endpoint: "https://" + name + ".hosts.internal:9443", HostID: "hw-" + name,
		Status: status, Evidence: Evidence{CheckedAt: checked, Architecture: "amd64"}, Allotment: a}
	if err := e.store.CreateHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	e.hosts = append(e.hosts, h)
	return h
}

func (e *placerEnv) readyHost(t *testing.T, name string, a Allotment) *Host {
	t.Helper()
	return e.addHost(t, name, "ready", time.Now(), a)
}

func serviceReq(resource string, vcpus, mem int) Request {
	return Request{OrgID: "org_a", ProjectID: "prj_1", EnvironmentID: "env_1", Resource: resource, Region: "eu-west",
		Kind: "service", Architecture: "amd64", VCPUs: vcpus, MemoryMiB: mem, DiskMiB: 1024}
}

func buildReq(resource string) Request {
	r := serviceReq(resource, 2, 2048)
	r.Kind = "build"
	return r
}

func countPlacements(t *testing.T, pg *pgdriver.PgDB, where string) int {
	t.Helper()
	var n int
	if err := pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_platform_placements WHERE `+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hostOf(t *testing.T, p *Placement) string {
	t.Helper()
	if p == nil || p.HostID == nil {
		t.Fatalf("placement has no host: %+v", p)
	}
	return *p.HostID
}

var defaultTestLimits = Limits{VCPUs: 4, MemoryMiB: 8192, VMs: 10, ConcurrentBuilds: 1}

func TestReserveConcurrentRespectsLimit(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, res := range []string{"web", "worker"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.svc.Reserve(ctx, serviceReq(res, 3, 1024))
		}()
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrLimit):
			limited++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 || limited != 1 {
		t.Fatalf("ok=%d limited=%d, want one of each (%v)", ok, limited, errs)
	}
	if n := countPlacements(t, e.pg, `state<>'released'`); n != 1 {
		t.Fatalf("placements = %d, want 1", n)
	}
}

func TestBuildsUseTheirOwnAllowance(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	if _, err := e.svc.Reserve(ctx, serviceReq("web", 4, 1024)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reserve(ctx, serviceReq("worker", 1, 256)); !errors.Is(err, ErrLimit) {
		t.Fatalf("service over the vCPU limit: %v", err)
	}
	b, err := e.svc.Reserve(ctx, buildReq("build_1"))
	if err != nil {
		t.Fatalf("build with every service vCPU used: %v", err)
	}
	if b.Kind != "build" || b.State != "reserved" {
		t.Fatalf("build placement = %+v", b)
	}
	_, err = e.svc.Reserve(ctx, buildReq("build_2"))
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "build") {
		t.Fatalf("second concurrent build: %v", err)
	}
	if err = e.svc.Release(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.Reserve(ctx, buildReq("build_2")); err != nil {
		t.Fatalf("build after the first was released: %v", err)
	}
}

func TestReserveSpreadsAWorkspace(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	a := e.readyHost(t, "a", bigAllotment)
	b := e.readyHost(t, "b", bigAllotment)
	// Another workspace takes memory on b, so b has less free memory than a.
	addPlacement(t, e.pg, "other", "org_b", "service", "running", &b.ID, 1, 4096, 10)
	ctx := t.Context()

	first, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if hostOf(t, first) != a.ID {
		t.Fatalf("first placement on %s, want the host with most free memory", hostOf(t, first))
	}
	second, err := e.svc.Reserve(ctx, serviceReq("worker", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if hostOf(t, second) != b.ID {
		t.Fatalf("second placement on %s, want the host with none of the workspace's placements", hostOf(t, second))
	}
}

func TestReserveSkipsDrainingAndStaleHosts(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	e.addHost(t, "draining", "draining", time.Now(), bigAllotment)
	e.addHost(t, "stale", "ready", time.Now().Add(-2*EvidenceMaxAge), bigAllotment)
	e.addHost(t, "pending", "pending", time.Now(), bigAllotment)
	arm := e.readyHost(t, "arm", bigAllotment)
	arm.Evidence.Architecture = "arm64"
	if err := e.store.UpdateHost(t.Context(), arm); err != nil {
		t.Fatal(err)
	}
	ok := e.readyHost(t, "ok", Allotment{VCPUs: 2, MemoryMiB: 2048, DiskMiB: 4096, VMs: 2})
	ctx := t.Context()

	p, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if hostOf(t, p) != ok.ID {
		t.Fatalf("placed on %s, want the only ready, fresh amd64 host", hostOf(t, p))
	}
	_, err = e.svc.Reserve(ctx, serviceReq("worker", 2, 512))
	if !errors.Is(err, ErrNoCapacity) || !strings.Contains(err.Error(), "EU West") {
		t.Fatalf("no ready host fits: %v", err)
	}
	if _, err = e.svc.Preview(ctx, serviceReq("worker", 2, 512)); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("preview with no ready host that fits: %v", err)
	}
}

func TestReserveKeepsHostOnRedeploy(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	a := e.readyHost(t, "a", bigAllotment)
	b := e.readyHost(t, "b", bigAllotment)
	ctx := t.Context()

	first, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if hostOf(t, first) != a.ID {
		t.Fatalf("first placement on %s", hostOf(t, first))
	}
	if err = e.svc.SetState(ctx, first.ID, "running"); err != nil {
		t.Fatal(err)
	}
	// Left to itself the placer would now pick b, which has more free memory.
	addPlacement(t, e.pg, "other", "org_b", "service", "running", &a.ID, 1, 8192, 10)

	again, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if hostOf(t, again) != a.ID {
		t.Fatalf("redeploy moved to %s, want it kept on %s", hostOf(t, again), a.ID)
	}
	if n := countPlacements(t, e.pg, `resource='web' AND state='released'`); n != 1 {
		t.Fatalf("released placements for web = %d, want the old one", n)
	}
	if n := countPlacements(t, e.pg, `resource='web' AND state<>'released'`); n != 1 {
		t.Fatalf("live placements for web = %d", n)
	}

	if _, err = e.svc.DrainHost(ctx, "", a.ID); err != nil {
		t.Fatal(err)
	}
	moved, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if hostOf(t, moved) != b.ID {
		t.Fatalf("redeploy off a draining host went to %s, want %s", hostOf(t, moved), b.ID)
	}
}

func TestResizeReleasesOldSizeFirst(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	h := e.readyHost(t, "a", Allotment{VCPUs: 4, MemoryMiB: 4096, DiskMiB: 8192, VMs: 4})
	ctx := t.Context()

	old, err := e.svc.Reserve(ctx, serviceReq("web", 3, 3072))
	if err != nil {
		t.Fatal(err)
	}
	// 4 more vCPUs fit neither the workspace limit nor the host unless the
	// old 3 are given back in the same transaction.
	resized, err := e.svc.Reserve(ctx, serviceReq("web", 4, 4096))
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if hostOf(t, resized) != h.ID || resized.VCPUs != 4 || resized.MemoryMiB != 4096 || resized.ID == old.ID {
		t.Fatalf("resized = %+v", resized)
	}
	use, err := e.store.TenantUse(ctx, "org_a")
	if err != nil || use.VCPUs != 4 || use.VMs != 1 {
		t.Fatalf("TenantUse = %+v, %v", use, err)
	}
	hu, err := e.store.HostUse(ctx, h.ID)
	if err != nil || hu.VCPUs != 4 || hu.VMs != 1 {
		t.Fatalf("HostUse = %+v, %v", hu, err)
	}
	if _, err = e.svc.Reserve(ctx, serviceReq("web", 5, 4096)); !errors.Is(err, ErrLimit) {
		t.Fatalf("resize past the limit: %v", err)
	}
	if n := countPlacements(t, e.pg, `resource='web' AND state<>'released' AND vcpus=4`); n != 1 {
		t.Fatalf("a refused resize changed the live placement")
	}
}

type refuseGate struct{ err error }

func (g refuseGate) CanRun(context.Context, string, metering.Kind, bool) error { return g.err }

var errBilling = errors.New("billing: the workspace has reached its spending limit")

func TestGateRefusalBlocksReserve(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	e.readyHost(t, "a", bigAllotment)
	e.svc.WithGate(refuseGate{err: errBilling})
	ctx := t.Context()
	if _, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512)); !errors.Is(err, errBilling) {
		t.Fatalf("Reserve = %v, want the gate's refusal", err)
	}
	if _, err := e.svc.Preview(ctx, serviceReq("web", 1, 512)); !errors.Is(err, errBilling) {
		t.Fatalf("Preview = %v, want the gate's refusal", err)
	}
	if n := countPlacements(t, e.pg, `true`); n != 0 {
		t.Fatalf("placements = %d after a gate refusal", n)
	}
}

func TestSuspendedTenantCannotReserve(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	limits := defaultTestLimits
	if err := e.store.UpsertTenant(ctx, &Tenant{OrgID: "org_a", Status: "enabled", Limits: &limits, SuspendReasons: []SuspendReason{ReasonSpendingLimit}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512)); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Reserve = %v", err)
	}
	if _, err := e.svc.Preview(ctx, serviceReq("web", 1, 512)); !errors.Is(err, ErrSuspended) {
		t.Fatalf("Preview = %v", err)
	}
	other := serviceReq("web", 1, 512)
	other.OrgID = "org_none"
	if _, err := e.svc.Reserve(ctx, other); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("Reserve without a tenant = %v", err)
	}
	if n := countPlacements(t, e.pg, `true`); n != 0 {
		t.Fatalf("placements = %d", n)
	}
}

func TestPreviewChecksWithoutWriting(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	e.readyHost(t, "a", Allotment{VCPUs: 2, MemoryMiB: 4096, DiskMiB: 8192, VMs: 4})
	ctx := t.Context()
	ref, err := e.svc.Preview(ctx, serviceReq("web", 2, 1024))
	if err != nil {
		t.Fatal(err)
	}
	if *ref != (Ref{Region: "eu-west", VCPUs: 2, MemoryMiB: 1024, DiskMiB: 1024}) {
		t.Fatalf("ref = %+v", ref)
	}
	if _, err = e.svc.Preview(ctx, serviceReq("web", 5, 1024)); !errors.Is(err, ErrLimit) {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err = e.svc.Preview(ctx, serviceReq("web", 3, 1024)); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("bigger than any host: %v", err)
	}
	closed := serviceReq("web", 1, 512)
	closed.Region = "ap-south"
	if _, err = e.svc.Preview(ctx, closed); err == nil {
		t.Fatal("a closed region took a placement")
	}
	if n := countPlacements(t, e.pg, `true`); n != 0 {
		t.Fatalf("Preview wrote %d placements", n)
	}
}

func TestErrorsNeverNameAHost(t *testing.T) {
	e := newPlacerEnv(t, Limits{VCPUs: 4, MemoryMiB: 4096, VMs: 2, ConcurrentBuilds: 1})
	e.readyHost(t, "secret-a", Allotment{VCPUs: 4, MemoryMiB: 4096, DiskMiB: 8192, VMs: 8})
	e.addHost(t, "secret-draining", "draining", time.Now(), bigAllotment)
	e.addHost(t, "secret-stale", "ready", time.Now().Add(-time.Hour), bigAllotment)
	ctx := t.Context()

	type refusal struct {
		name string
		err  error
	}
	var got []refusal
	try := func(name string, req Request) {
		t.Helper()
		_, rerr := e.svc.Reserve(ctx, req)
		_, perr := e.svc.Preview(ctx, req)
		if rerr == nil || perr == nil {
			t.Fatalf("%s: Reserve %v, Preview %v, want both refused", name, rerr, perr)
		}
		got = append(got, refusal{name + " reserve", rerr}, refusal{name + " preview", perr})
	}

	if _, err := e.svc.Reserve(ctx, buildReq("build_1")); err != nil {
		t.Fatal(err)
	}
	try("vcpu limit", serviceReq("web", 5, 512))
	try("memory limit", serviceReq("web", 1, 8192))
	try("build limit", buildReq("build_2"))
	try("no capacity", serviceReq("web", 4, 4096))
	big := serviceReq("web", 1, 512)
	big.DiskMiB = 1 << 30
	try("no capacity disk", big)
	closed := serviceReq("web", 1, 512)
	closed.Region = "ap-south"
	try("closed region", closed)
	missing := serviceReq("web", 1, 512)
	missing.Region = "nowhere"
	try("unknown region", missing)
	none := serviceReq("web", 1, 512)
	none.OrgID = "org_none"
	try("not enabled", none)

	e.svc.WithGate(refuseGate{err: errBilling})
	try("gate", serviceReq("web", 1, 512))
	e.svc.WithGate(nil)

	// A raw database error that names the host must not reach the workspace.
	if _, err := e.pg.Exec(ctx, `CREATE FUNCTION placer_test_refuse() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'refused on % at %', NEW.host_id, (SELECT endpoint || ' ' || host_id FROM loomplane_platform_hosts WHERE id=NEW.host_id);
END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pg.Exec(ctx, `CREATE TRIGGER placer_test_refuse BEFORE INSERT ON loomplane_platform_placements FOR EACH ROW EXECUTE FUNCTION placer_test_refuse()`); err != nil {
		t.Fatal(err)
	}
	_, rawErr := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if rawErr == nil {
		t.Fatal("the refusing trigger let a placement in")
	}
	if rawErr.Error() != "Loomplane Cloud couldn't place this right now." {
		t.Fatalf("raw error reached the workspace: %v", rawErr)
	}
	got = append(got, refusal{"raw driver error", rawErr})

	for _, r := range got {
		for _, h := range e.hosts {
			for _, secret := range []string{h.ID, h.HostID, h.Endpoint, "secret"} {
				if strings.Contains(r.err.Error(), secret) {
					t.Errorf("%s: %q names a host (%s)", r.name, r.err, secret)
				}
			}
		}
	}
}

func TestSetStateAndRelease(t *testing.T) {
	e := newPlacerEnv(t, defaultTestLimits)
	e.readyHost(t, "a", bigAllotment)
	ctx := t.Context()
	p, err := e.svc.Reserve(ctx, serviceReq("web", 1, 512))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.SetState(ctx, p.ID, "running"); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.SetState(ctx, p.ID, "bogus"); err == nil {
		t.Fatal("SetState took an unknown state")
	}
	if err = e.svc.Release(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Release(ctx, p.ID); err != nil {
		t.Fatalf("second release: %v", err)
	}
	if err = e.svc.SetState(ctx, p.ID, "running"); !errors.Is(err, ErrConflict) {
		t.Fatalf("SetState on a released placement = %v", err)
	}
	if err = e.svc.Release(ctx, "plc_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Release missing = %v", err)
	}
}
