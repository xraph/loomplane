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

// Loomplane Cloud's own maintenance gets authority only to stop or remove a
// platform VM whose placement is released or suspended, or whose workspace
// is suspended, and never while a deploy in the environment is going.
func TestPlatformMaintenanceAuthorityIsNarrow(t *testing.T) {
	dsn, template := admissionTemplate(t)
	fixture := admissionFixture(t, dsn, template)
	pg := fixture.db
	ctx := context.WithValue(t.Context(), platformMaintenanceKey{}, true)
	store := NewPgStore(pg)
	digest := strings.Repeat("a", 64)
	place := func(id, instance, state string) {
		t.Helper()
		if _, err := pg.Exec(t.Context(), `INSERT INTO loomplane_platform_regions(id,name,display_name,status,revision,created_at,updated_at) VALUES('reg_1','eu','EU','active',1,now(),now()) ON CONFLICT DO NOTHING`); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.Exec(t.Context(), `INSERT INTO loomplane_platform_placements(id,org_id,project_id,environment_id,resource,kind,region_id,vcpus,memory_mib,disk_mib,state,instance_id,created_at,updated_at)
VALUES($1,'org','proj','env',$1,'service','reg_1',1,128,1024,$3,$2,now(),now())`, id, instance, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pg.Exec(t.Context(), `INSERT INTO loomplane_platform_tenants(org_id,status,suspend_reasons,revision,created_at,updated_at) VALUES('org','enabled','{}',1,now(),now())`); err != nil {
		t.Fatal(err)
	}
	place("pl_old", "inst_old", "released")
	place("pl_live", "inst_live", "running")
	scope := func(instance string) fc.Scope {
		return fc.Scope{Org: "org", Connection: fc.PlatformConnection, Project: "proj", Environment: "env", Resource: instance}
	}

	if _, err := store.IssueFirecracker(ctx, scope("inst_old"), "remove/platform-maintenance", digest); err != nil {
		t.Fatalf("a released placement's VM: %v", err)
	}
	if _, err := store.IssueFirecracker(ctx, scope("inst_live"), "remove/platform-maintenance", digest); !errors.Is(err, fc.ErrConflict) {
		t.Fatalf("a running placement of a workspace in good standing: %v", err)
	}
	if _, err := store.IssueFirecracker(ctx, scope("inst_old"), "apply/platform-maintenance", digest); !errors.Is(err, fc.ErrConflict) {
		t.Fatalf("an apply: %v", err)
	}
	own := scope("inst_old")
	own.Connection = "conn_1"
	if _, err := store.IssueFirecracker(ctx, own, "remove/platform-maintenance", digest); !errors.Is(err, fc.ErrConflict) {
		t.Fatalf("a workspace's own Firecracker VM: %v", err)
	}
	if _, err := store.IssueFirecracker(t.Context(), scope("inst_old"), "remove/platform-maintenance", digest); !errors.Is(err, fc.ErrConflict) {
		t.Fatalf("no maintenance mark: %v", err)
	}

	if _, err := pg.Exec(t.Context(), `UPDATE loomplane_platform_tenants SET suspend_reasons='{past_due}'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IssueFirecracker(ctx, scope("inst_live"), "stop/platform-maintenance", digest); err != nil {
		t.Fatalf("a suspended workspace's running VM: %v", err)
	}

	attempt, err := cloudinfra.NewPgStore(pg).BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: "plan", Deployment: "deployment"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = attempt
	if _, err := store.IssueFirecracker(ctx, scope("inst_old"), "remove/platform-maintenance", digest); !errors.Is(err, fc.ErrConflict) {
		t.Fatalf("while a deploy is going: %v", err)
	}
}
