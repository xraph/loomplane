package buildrunner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

type admins map[string]bool

func (a admins) IsPlatformAdmin(_ context.Context, user string) (bool, error) { return a[user], nil }

type workspaces map[string]*platformcloud.WorkspaceView

func (w workspaces) Workspace(_ context.Context, org string) (*platformcloud.WorkspaceView, error) {
	if v, ok := w[org]; ok {
		return v, nil
	}
	return &platformcloud.WorkspaceView{}, nil
}

type meter struct{ events []metering.Event }

func (m *meter) Record(_ context.Context, e metering.Event) error {
	m.events = append(m.events, e)
	return nil
}

var staff = security.Principal{UserID: "usr_staff", OrgID: "org_platform"}

func platformRig(t *testing.T) (*rig, *Pool, *meter) {
	t.Helper()
	r := newRig()
	m := &meter{}
	r.svc.Admins = admins{"usr_staff": true}
	r.svc.Workspaces = workspaces{"org_1": {Enabled: true}, "org_suspended": {Enabled: true, SuspendReasons: []platformcloud.SuspendReason{"billing"}}}
	r.svc.Meter = m
	pool, err := r.svc.CreatePlatformPool(t.Context(), staff, PoolInput{Name: "global", Settings: PoolSettings{Default: true}})
	if err != nil {
		t.Fatal(err)
	}
	return r, pool, m
}

func TestOnlyPlatformAdminsMakeGlobalPools(t *testing.T) {
	r, _, _ := platformRig(t)
	if _, err := r.svc.CreatePlatformPool(t.Context(), owner, PoolInput{Name: "mine"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("an organization owner made a global pool: %v", err)
	}
	keyed := staff
	keyed.KeyID = "key_1"
	if _, err := r.svc.CreatePlatformPool(t.Context(), keyed, PoolInput{Name: "keyed"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a platform admin's API key made a global pool: %v", err)
	}
	if pools, _ := r.svc.ListPools(t.Context(), owner); len(pools) != 0 {
		t.Fatalf("a workspace sees the global pools: %+v", pools)
	}
}

func TestGlobalPoolIsTheLastDefault(t *testing.T) {
	r, pool, _ := platformRig(t)
	// A global runner joins the platform admin's organization.
	if conn, err := r.svc.JoinRunner(t.Context(), "org_platform", pool.ID, "node_g1"); err != nil || conn != pool.ID {
		t.Fatalf("join %q %v", conn, err)
	}
	d, err := r.svc.BuildDefault(t.Context(), "org_1", "prj_1")
	if err != nil || d == nil || d.Builder != pool.ID || d.Connection == nil || d.Connection.ID != pool.ID || d.Connection.Cloud != "runner" {
		t.Fatalf("default %+v %v", d, err)
	}
	if !strings.Contains(d.Source, "billed as Loomplane Cloud build minutes") {
		t.Fatalf("the plan doesn't say global builds are billed: %q", d.Source)
	}
	for _, org := range []string{"org_disabled", "org_suspended"} {
		if got, _ := r.svc.BuildDefault(t.Context(), org, "prj_1"); got != nil {
			t.Fatalf("%s got global runners: %+v", org, got)
		}
		if _, derr := r.svc.PoolDetails(t.Context(), org, pool.ID); derr == nil {
			t.Fatalf("%s may build on global runners", org)
		}
	}
	// The workspace's own default wins.
	own, err := r.svc.CreatePool(t.Context(), owner, PoolInput{Name: "own", Scope: ScopeOrganization, Settings: PoolSettings{Default: true}})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ = r.svc.BuildDefault(t.Context(), "org_1", "prj_1"); d == nil || d.Builder != own.ConnectionID {
		t.Fatalf("the workspace's own default lost to the global one: %+v", d)
	}
}

func TestGlobalPoolDetailsCarryPlatformEvidence(t *testing.T) {
	r, pool, _ := platformRig(t)
	if _, err := r.svc.JoinRunner(t.Context(), "org_platform", pool.ID, "node_g1"); err != nil {
		t.Fatal(err)
	}
	r.nodes["node_g1"] = nodesOnline("node_g1", "org_platform")
	caps := capsOf("node_g1", strings.Repeat("a", 64))
	caps.Platform, caps.DeniedPorts = true, []int{25, 465, 587}
	caps.Limits.PerOrg.VMs, caps.Limits.PerOrg.VCPUs, caps.Limits.PerOrg.MemoryMiB = 2, 4, 4096
	r.streams.serve(t, "node_g1", caps)
	r.svc.RunnerOnline(t.Context(), r.nodes["node_g1"])
	d, err := r.svc.PoolDetails(t.Context(), "org_1", pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d["build_isolation"] != "ready" || d["platform"] != "true" || d["smt_active"] != "false" || d["per_org_vms"] != "2" || d["denied_ports"] != "25,465,587" {
		t.Fatalf("details %v", d)
	}
	runners, err := r.svc.BuildRunners(t.Context(), "org_1", pool.ID)
	if err != nil || len(runners) != 1 {
		t.Fatalf("runners %v %v", runners, err)
	}
	if _, err = r.svc.BuildRunners(t.Context(), "org_disabled", pool.ID); err == nil {
		t.Fatal("a workspace that isn't enabled found global runners")
	}
}

func TestGlobalBuildsAreMetered(t *testing.T) {
	r, pool, m := platformRig(t)
	start := time.Now()
	if err := r.svc.MeterBuild(t.Context(), "org_1", pool.ID, "build_1", start, start.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.MeterBuild(t.Context(), "org_1", "conn_own", "build_2", start, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(m.events) != 1 {
		t.Fatalf("events %+v: only a global build is metered", m.events)
	}
	e := m.events[0]
	if e.OrgID != "org_1" || e.Feature != platformcloud.FeatureBuildMinutes || e.Quantity != 2 || !e.PlatformOwned || e.ID != "runner-build:build_1" {
		t.Fatalf("event %+v", e)
	}
}
