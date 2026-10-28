//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

func TestRouteWorkloadUpgradePreservesOlderUnfencedAttempt(t *testing.T) {
	f, _ := routeAttemptFixture(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES('old-direct','org','proj','env','production','applied','review','pool-manifest-proj',1,'{"actions":[{"id":"service:web","name":"web","provider":"server","op":"update"}]}',true,'usr',now()),('new-pooled','org','proj','env','production','applied','review','pool-manifest-proj',1,'{"actions":[{"id":"service:web","name":"web","provider":"server","op":"update"}]}',true,'usr',now())`,
		`INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at,finished_at,steps) VALUES('old-worker','org','proj','env','production','old-direct','reviewed','failed','usr',now()-interval '3 hours',now()-interval '2 hours','[{"action_id":"service:web","status":"failed"}]'),('new-worker','org','proj','env','production','new-pooled','reviewed','failed','usr',now()-interval '1 hour',now(),'[{"action_id":"service:web","status":"failed"}]')`,
		`INSERT INTO loomplane_pool_execution(org_id,plan_id,service,phase,active,generation) VALUES('org','new-pooled','web','reserved',false,1)`,
		`DROP TABLE loomplane_workload_attempts`,
	} {
		if _, err := f.sql.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := federationMigration(t, "20261028000300").Up(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal(err)
	}
	if err := federationMigration(t, "20261028000400").Up(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal(err)
	}
	var legacy bool
	var plan string
	if err := f.sql.QueryRow(ctx, `SELECT legacy_unfenced,plan_id FROM loomplane_workload_attempts WHERE org_id='org' AND environment_id='env' AND service='web'`).Scan(&legacy, &plan); err != nil {
		t.Fatal(err)
	}
	if !legacy {
		t.Fatal("forward correction discarded older ambiguity")
	}
	attempt, err := f.store.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "new-pooled", Deployment: "new-worker"}, nil, false)
	if err == nil {
		t.Fatalf("older failed direct worker was discarded: selected plan=%s legacy_unfenced=%v; takeover accepted generation=%d", plan, legacy, attempt.Generation)
	}
}

func TestRouteWorkloadDirectPreflightAllowsReplan(t *testing.T) {
	for _, priorEffects := range []bool{false, true} {
		t.Run(fmt.Sprint(priorEffects), func(t *testing.T) {
			f := newPoolFixture(t)
			ctx := t.Context()
			if _, err := f.sql.Exec(ctx, `DELETE FROM loomplane_server_pool_members`); err != nil {
				t.Fatal(err)
			}
			directPoolPlan(t, f, "preflight-direct", "runtime-1")
			f.accept(t, "preflight-direct")
			executor := &poolExecutor{}
			runner := poolTestRunner(f, executor)
			dep, _, err := runner.Start(ctx, "org", "preflight-direct")
			if err != nil {
				t.Fatal(err)
			}
			expectedCalls := 0
			if priorEffects {
				executor.fail = true
				if err = runner.Run(ctx, "org", dep, "service:api", nil, func([]byte) error { return nil }); err == nil {
					t.Fatal("ambiguous outcome expected")
				}
				expectedCalls = 1
				executor.fail = false
			}
			if _, err = f.sql.Exec(ctx, `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{architecture}','"arm64"') WHERE id='runtime-1'`); err != nil {
				t.Fatal(err)
			}
			err = runner.Run(ctx, "org", dep, "service:api", nil, func([]byte) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "target settings") || executor.calls != expectedCalls {
				t.Fatalf("expected pre-effect target settings refusal: %v calls=%d", err, executor.calls)
			}
			retryErr := runner.Run(ctx, "org", dep, "service:api", nil, func([]byte) error { return nil })
			if finishErr := runner.Finish(ctx, "org", dep, "preflight refused"); finishErr != nil {
				t.Fatal(finishErr)
			}
			directPoolPlan(t, f, "replacement-direct", "runtime-1")
			if _, err = f.sql.Exec(ctx, `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,target_settings,architecture}','"arm64"') WHERE id='replacement-direct'`); err != nil {
				t.Fatal(err)
			}
			f.accept(t, "replacement-direct")
			newer, _, err := runner.Start(ctx, "org", "replacement-direct")
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(ctx, "org", newer, "service:api", nil, func([]byte) error { return nil })
			if priorEffects {
				if err == nil || !strings.Contains(err.Error(), "recover the prior service attempt") {
					t.Fatal("unknown prior effects were cleared", err)
				}
			} else if err != nil {
				t.Fatalf("no-effect refusal stranded service: old retry=%v; fresh reviewed plan=%v; executor calls=%d", retryErr, err, executor.calls)
			}
		})
	}
}

func TestRouteWorkloadEffectBoundaryFencesSettlement(t *testing.T) {
	f, _ := routeAttemptFixture(t)
	ctx := t.Context()
	approvedWorkloadPlan(t, f, "plan")
	in := cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "plan", Deployment: "deployment"}
	first, err := f.store.BeginWorkloadAttempt(ctx, in, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.store.BeginWorkloadAttempt(ctx, in, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.MarkWorkloadEffects(ctx, *first); err == nil {
		t.Fatal("stale attempt entered provider effects")
	}
	if err = f.store.FinishWorkloadAttempt(ctx, *first, false); err == nil {
		t.Fatal("stale no-effect callback settled successor")
	}
	if err = f.store.MarkWorkloadEffects(ctx, *next); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.store.BeginWorkloadAttempt(ctx, in, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishWorkloadAttempt(ctx, *resumed, false); err != nil {
		t.Fatal(err)
	}
	in.Plan = "different"
	in.Deployment = "different"
	if _, err = f.store.BeginWorkloadAttempt(ctx, in, nil, false); err == nil {
		t.Fatal("preflight-only resumed generation erased prior uncertainty")
	}
}

func TestRouteWorkloadTTLUpdateRevisesAcceptedAuthority(t *testing.T) {
	f, op := routeAttemptFixture(t)
	ctx := t.Context()
	if err := f.store.AcceptRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE loomplane_domain_routes SET status='ready',body=jsonb_set(body,'{status}','"ready"')`,
		`UPDATE loomplane_route_operations SET status='succeeded',body=jsonb_set(body,'{status}','"succeeded"')`,
	} {
		if _, err := f.sql.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	before, err := f.store.Route(ctx, "org", "proj", op.Route.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Authz: domainAuth{}})
	p := security.Principal{OrgID: "org", UserID: "manager"}
	in := cloudinfra.RouteTTLInput{Revision: before.Revision, ExternalTTLSeconds: 3600}
	after, err := svc.UpdateRouteTTL(ctx, p, "proj", before.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if after.Input.ExternalTTLSeconds != 3600 || after.Revision != before.Revision+1 {
		t.Fatal(after)
	}
	if _, err = svc.UpdateRouteTTL(ctx, p, "proj", before.ID, in); err == nil {
		t.Fatal("stale revision changed TTL")
	}
	attempt := cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "new", Deployment: "new"}
	if _, err = f.store.BeginWorkloadAttempt(ctx, attempt, []cloudinfra.Route{*before}, false); err == nil {
		t.Fatal("old reviewed snapshot survived TTL update")
	}
	active, err := f.store.BeginWorkloadAttempt(ctx, attempt, []cloudinfra.Route{*after}, false)
	if err != nil {
		t.Fatal(err)
	}
	in.Revision = after.Revision
	if _, err = svc.UpdateRouteTTL(ctx, p, "proj", before.ID, in); err == nil {
		t.Fatal("TTL changed under active release")
	}
	if err = f.store.FinishWorkloadAttempt(ctx, *active, false); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.UpdateRouteTTL(ctx, p, "proj", before.ID, in); err != nil {
		t.Fatal("no-effect attempt blocked TTL update", err)
	}
}
