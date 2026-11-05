//go:build integration

package platformcloud

import (
	"errors"
	"strings"
	"testing"

	cpauth "github.com/xraph/ctrlplane/auth"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

// readyHost is a host the platform has checked and found ready, serving a
// fake agent, with org_a enabled.
func readyHost(t *testing.T) (*hostEnv, *Host) {
	t.Helper()
	e := newHostEnv(t)
	e.serve(t, agentLimits())
	nh, err := e.svc.AddHost(t.Context(), "owner", e.in)
	if err != nil {
		t.Fatal(err)
	}
	e.trust(t, nh)
	h, err := e.svc.CheckHost(t.Context(), "owner", nh.Host.ID)
	if err != nil || h.Status != "ready" {
		t.Fatalf("host not ready: %v %+v", err, h)
	}
	if _, err = e.svc.EnableTenant(t.Context(), "org_a", &defaultTestLimits); err != nil {
		t.Fatal(err)
	}
	return e, h
}

func webRequest() Request {
	return Request{OrgID: "org_a", ProjectID: "prj_1", EnvironmentID: "env_1", Resource: "web", Region: "eu-west", Kind: "service", VCPUs: 1, MemoryMiB: 512, DiskMiB: 4096}
}

func readPlacement(t *testing.T, e *hostEnv, id string) *Placement {
	t.Helper()
	p, err := scanPlacement(e.pg.QueryRow(t.Context(), `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE id=$1`, id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSessionReservesAndOpensThePlacedHost(t *testing.T) {
	e, h := readyHost(t)
	ctx := t.Context()
	c, scope, p, err := e.svc.Session(ctx, "org_a", "prj_1", "env_1", "inst_web", webRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := fc.Scope{Org: "org_a", Connection: fc.PlatformConnection, Project: "prj_1", Environment: "env_1", Resource: "inst_web"}
	if scope != want || p.HostID == nil || *p.HostID != h.ID || p.InstanceID != "inst_web" || p.State != "reserved" {
		t.Fatalf("scope %+v placement %+v", scope, p)
	}
	if c.HostID != e.hostID || c.Details["runtime_identity"] != strings.Repeat("a", 64) {
		t.Fatalf("client %+v", c.Details)
	}
	if got := readPlacement(t, e, p.ID); got.InstanceID != "inst_web" {
		t.Fatalf("stored %+v", got)
	}

	// Attach reaches it by its instance, and only by that.
	ac, ascope, ap, err := e.svc.Attach(ctx, "org_a", "prj_1", "env_1", "inst_web", "web")
	if err != nil || ap.ID != p.ID || ascope != want || ac == nil {
		t.Fatalf("attach: %v %+v %+v", err, ap, ascope)
	}
	ac.Close()
	if _, _, _, err = e.svc.Attach(ctx, "org_a", "prj_1", "env_1", "inst_other", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach to another instance: %v", err)
	}
	if _, _, _, err = e.svc.Attach(ctx, "org_a", "prj_other", "env_1", "inst_web", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach from another project: %v", err)
	}

	if err = e.svc.Running(ctx, "org_a", p.ID, "inst_web", 7); err != nil {
		t.Fatal(err)
	}
	if got := readPlacement(t, e, p.ID); got.State != "running" || got.Generation != 7 || got.InstanceID != "inst_web" {
		t.Fatalf("running %+v", got)
	}
}

// A redeploy on the same host replaces the placement row; the released one
// keeps the instance until something forgets it.
func TestSessionAgainLeavesTheOldRowForTheSweep(t *testing.T) {
	e, h := readyHost(t)
	ctx := t.Context()
	c, _, first, err := e.svc.Session(ctx, "org_a", "prj_1", "env_1", "inst_web", webRequest())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, _, second, err := e.svc.Session(ctx, "org_a", "prj_1", "env_1", "inst_web", webRequest())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if second.ID == first.ID || *second.HostID != h.ID {
		t.Fatalf("second %+v", second)
	}
	left, err := e.svc.Leftovers(ctx, "org_a", "env_1", "web")
	if err != nil || len(left) != 1 || left[0].ID != first.ID || left[0].InstanceID != "inst_web" || *left[0].HostID != h.ID {
		t.Fatalf("leftovers %v %+v", err, left)
	}
	oc, scope, err := e.svc.Open(ctx, &left[0])
	if err != nil || oc == nil || scope.Resource != "inst_web" || scope.Connection != fc.PlatformConnection {
		t.Fatalf("open: %v %+v", err, scope)
	}
	oc.Close()
	if err = e.svc.Removed(ctx, "org_a", first.ID); err != nil {
		t.Fatal(err)
	}
	if left, err = e.svc.Leftovers(ctx, "org_a", "env_1", "web"); err != nil || len(left) != 0 {
		t.Fatalf("leftovers after removal %v %+v", err, left)
	}
	if err = e.svc.Removed(ctx, "org_a", second.ID); err != nil {
		t.Fatal(err)
	}
	if got := readPlacement(t, e, second.ID); got.State != "released" || got.InstanceID != "" || got.HostID == nil {
		t.Fatalf("removed %+v", got)
	}
}

func TestStoppedIsNotRestored(t *testing.T) {
	e, _ := readyHost(t)
	ctx := t.Context()
	c, _, p, err := e.svc.Session(ctx, "org_a", "prj_1", "env_1", "inst_web", webRequest())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err = e.pg.Exec(ctx, `UPDATE loomplane_platform_placements SET state='running',restore_running=true WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Stopped(ctx, "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	if got := readPlacement(t, e, p.ID); got.State != "stopped" || got.RestoreRunning {
		t.Fatalf("stopped %+v", got)
	}
}

// A host whose runtime changed since its last check takes no new work, and
// the refusal names nothing about it.
func TestSessionRefusesAChangedHostWithoutNamingIt(t *testing.T) {
	e, h := readyHost(t)
	e.driver.caps.RuntimeIdentity = strings.Repeat("b", 64)
	_, _, _, err := e.svc.Session(t.Context(), "org_a", "prj_1", "env_1", "inst_web", webRequest())
	if err == nil {
		t.Fatal("placed on a changed host")
	}
	for _, leak := range []string{h.ID, e.hostID, e.server.URL, strings.TrimPrefix(e.server.URL, "https://"), "127.0.0.1"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("%q names %q", err, leak)
		}
	}
}

func TestSessionRefusesAnotherTenant(t *testing.T) {
	e, _ := readyHost(t)
	ctx := cpauth.WithClaims(t.Context(), &cpauth.Claims{TenantID: "org_b", SubjectID: "user"})
	if _, _, _, err := e.svc.Session(ctx, "org_a", "prj_1", "env_1", "inst_web", webRequest()); err == nil {
		t.Fatal("session for another tenant")
	}
	if _, _, _, err := e.svc.Session(t.Context(), "org_a", "prj_1", "env_other", "inst_web", webRequest()); err == nil {
		t.Fatal("session for a request outside its environment")
	}
	if _, err := e.svc.Leftovers(ctx, "org_a", "env_1", "web"); err == nil {
		t.Fatal("leftovers for another tenant")
	}
}

// The setters only touch the organization's own placement.
func TestPlacementSettersAreOrgScoped(t *testing.T) {
	e, _ := readyHost(t)
	ctx := t.Context()
	c, _, p, err := e.svc.Session(ctx, "org_a", "prj_1", "env_1", "inst_web", webRequest())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err = e.svc.Running(ctx, "org_b", p.ID, "inst_web", 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("running for another org: %v", err)
	}
	if err = e.svc.Stopped(ctx, "org_b", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stopped for another org: %v", err)
	}
	if err = e.svc.Removed(ctx, "org_b", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed for another org: %v", err)
	}
	if got := readPlacement(t, e, p.ID); got.State != "reserved" || got.InstanceID != "inst_web" {
		t.Fatalf("another org changed %+v", got)
	}
	if err = e.svc.Removed(ctx, "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Running(ctx, "org_a", p.ID, "inst_web", 3); !errors.Is(err, ErrConflict) {
		t.Fatalf("running a released placement: %v", err)
	}
}
