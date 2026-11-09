//go:build integration

package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

func TestFirecrackerAuthorityFencesReplayTakeoverAndCancellation(t *testing.T) {
	dsn, template := admissionTemplate(t)
	fixture := admissionFixture(t, dsn, template)
	pg := fixture.db
	attempt, err := cloudinfra.NewPgStore(pg).BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: "plan", Deployment: "deployment"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := attempt.Context(t.Context())
	store := NewPgStore(pg)
	scope := fc.Scope{Org: "org", Connection: "host", Project: "proj", Environment: "env", Resource: "api"}
	digest := strings.Repeat("a", 64)
	first, err := store.IssueFirecracker(ctx, scope, "apply", digest)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.IssueFirecracker(ctx, scope, "apply", digest)
	if err != nil || again.Generation != first.Generation || again.Operation != first.Operation {
		t.Fatal("replay changed authority", again, err)
	}
	if _, err = store.IssueFirecracker(ctx, scope, "apply", strings.Repeat("b", 64)); !errors.Is(err, fc.ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	next, err := store.IssueFirecracker(ctx, scope, "stop", digest)
	if err != nil || next.Generation <= first.Generation {
		t.Fatal("resource generation failed", next, err)
	}
	if _, err = store.IssueFirecracker(ctx, scope, "apply", digest); !errors.Is(err, fc.ErrConflict) {
		t.Fatal("superseded operation revived", err)
	}
	takeover, err := cloudinfra.NewPgStore(pg).BeginWorkloadAttempt(t.Context(), *attempt, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueFirecracker(ctx, scope, "other", digest); err == nil {
		t.Fatal("stale worker renewed authority")
	}
	if _, err = store.IssueFirecracker(takeover.Context(t.Context()), scope, "apply", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(t.Context(), `UPDATE loomplane_deployments SET status='cancelled',finished_at=now() WHERE id='deployment'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueFirecracker(takeover.Context(t.Context()), scope, "after-cancel", digest); err == nil {
		t.Fatal("cancelled worker issued authority")
	}
}

// Loomplane Cloud's own maintenance gets authority only to stop or remove
// one named placement's platform VM on its own host, when that placement is
// released or suspended or its workspace is suspended, never the VM a live
// placement of the same instance runs on that host, and never while a
// deploy in the environment is going.
func TestPlatformMaintenanceAuthorityIsNarrow(t *testing.T) {
	dsn, template := admissionTemplate(t)
	fixture := admissionFixture(t, dsn, template)
	pg := fixture.db
	ctx := context.WithValue(t.Context(), platformMaintenanceKey{}, true)
	store := NewPgStore(pg)
	digest := strings.Repeat("a", 64)
	for _, q := range []string{
		`INSERT INTO loomplane_platform_regions(id,name,display_name,status,revision,created_at,updated_at) VALUES('reg_1','eu','EU','active',1,now(),now())`,
		`INSERT INTO loomplane_platform_hosts(id,region_id,endpoint,host_id,status,revision,created_at,updated_at) VALUES('host_a','reg_1','https://a','hw-a','draining',1,now(),now()),('host_b','reg_1','https://b','hw-b','ready',1,now(),now())`,
		`INSERT INTO loomplane_platform_tenants(org_id,status,suspend_reasons,revision,created_at,updated_at) VALUES('org','enabled','{}',1,now(),now())`,
	} {
		if _, err := pg.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	place := func(id, host, state string) {
		t.Helper()
		if _, err := pg.Exec(t.Context(), `INSERT INTO loomplane_platform_placements(id,org_id,project_id,environment_id,resource,kind,region_id,host_id,vcpus,memory_mib,disk_mib,state,instance_id,created_at,updated_at)
VALUES($1,'org','proj','env','web','service','reg_1',$2,1,128,1024,$3,'inst_web',now(),now())`, id, host, state); err != nil {
			t.Fatal(err)
		}
	}
	// A move: the old placement on a is released, the live one on b runs
	// the same instance.
	place("pl_old", "host_a", "released")
	place("pl_live", "host_b", "running")
	scope := fc.Scope{Org: "org", Connection: fc.PlatformConnection, Project: "proj", Environment: "env", Resource: "inst_web"}
	issue := func(c context.Context, s fc.Scope, op string) error {
		_, err := store.IssueFirecracker(c, s, op, digest)
		return err
	}

	if err := issue(ctx, scope, maintenanceOperation("remove", "pl_old", "host_a")); err != nil {
		t.Fatalf("the leftover a move left: %v", err)
	}
	for name, op := range map[string]string{
		"the running twin":                      maintenanceOperation("remove", "pl_live", "host_b"),
		"the leftover, sent to the twin's host": maintenanceOperation("remove", "pl_old", "host_b"),
		"an apply":                              "apply/" + maintenanceTag + "/pl_old/host_a",
		"no placement named":                    "remove/" + maintenanceTag,
		"the old unbound form":                  "remove/platform-maintenance",
	} {
		if err := issue(ctx, scope, op); !errors.Is(err, fc.ErrConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	own := scope
	own.Connection = "conn_1"
	if err := issue(ctx, own, maintenanceOperation("remove", "pl_old", "host_a")); !errors.Is(err, fc.ErrConflict) {
		t.Errorf("a workspace's own Firecracker VM: %v", err)
	}
	if err := issue(t.Context(), scope, maintenanceOperation("remove", "pl_old", "host_a")); !errors.Is(err, fc.ErrConflict) {
		t.Errorf("no maintenance mark: %v", err)
	}

	// A redeploy on the same host keeps the instance: the released row
	// there names the VM the live placement runs.
	place("pl_same", "host_b", "released")
	if err := issue(ctx, scope, maintenanceOperation("remove", "pl_same", "host_b")); !errors.Is(err, fc.ErrConflict) {
		t.Errorf("a released row on the live VM's host: %v", err)
	}

	// A suspended workspace's running VM may be stopped as itself.
	if _, err := pg.Exec(t.Context(), `UPDATE loomplane_platform_tenants SET suspend_reasons='{past_due}'`); err != nil {
		t.Fatal(err)
	}
	if err := issue(ctx, scope, maintenanceOperation("stop", "pl_live", "host_b")); err != nil {
		t.Errorf("a suspended workspace's running VM: %v", err)
	}

	if _, err := cloudinfra.NewPgStore(pg).BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: "plan", Deployment: "deployment"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := issue(ctx, scope, maintenanceOperation("remove", "pl_old", "host_a")); !errors.Is(err, fc.ErrConflict) {
		t.Errorf("while a deploy is going: %v", err)
	}
}
