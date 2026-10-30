//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// unpublishedCleanupRelease uses the saved application producer and journal.
// Its accepted shared UID remains unpublished when readiness fails later.
func unpublishedCleanupRelease(t *testing.T, f *poolFixture, svc *planning.Service, old kube.ManagedScope, write ...func(*kube.ControllerStore, kube.ManagedScope)) kube.ManagedScope {
	t.Helper()
	if err := f.store.FinishWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: old.Org, Project: old.Project, Environment: old.Environment, Service: old.Service, Plan: old.Plan, Deployment: old.Deployment, Generation: old.Epoch}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='succeeded' WHERE id=$1`, old.Deployment); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := f.sql.QueryRow(t.Context(), `SELECT result FROM loomplane_plans WHERE id=$1`, old.Plan).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var result planning.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	plan := &planning.Plan{ID: old.Plan + "-unpublished", OrgID: old.Org, ProjectID: old.Project, EnvironmentID: old.Environment, CreatedBy: "usr"}
	result.Actions[0].Op = planning.Update
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, &result); err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareManagedCleanupTargets(t.Context(), plan, &result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, plan, &result)
	deployment := old.Deployment + "-unpublished"
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES($1,'org','proj','env','production',$2,'update','running','usr',now())`, deployment, plan.ID); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: old.Org, Project: old.Project, Environment: old.Environment, Service: old.Service, Plan: plan.ID, Deployment: deployment}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scope := old
	scope.Plan, scope.Deployment, scope.Epoch = plan.ID, deployment, attempt.Generation
	scope.RuntimeRef += "-unpublished"
	scope.RuntimeRevision = deployment + "/service:api"
	scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
	journal := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if err = journal.OpenManagedScope(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if len(write) > 0 {
		write[0](journal, scope)
	} else {
		id, issueErr := journal.Issue(t.Context(), scope, kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: scope.Namespace, Name: scope.Instance, Method: "PUT"})
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		if err = journal.Resolve(t.Context(), id, scope, kube.ManagedOutcome{State: "accepted", UID: "deployment-uid", ResourceVersion: "2", Generation: 2, StatusCode: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='failed',finished_at=now() WHERE id=$1`, deployment); err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestManagedCleanupClosureApproval(t *testing.T) {
	for _, change := range []string{"complete", "omitted", "namespace", "uid", "foreign", "late-owner", "legacy-review"} {
		t.Run(change, func(t *testing.T) {
			f, svc, plan, result, scope := cleanupTargetFixture(t)
			original := result.Actions[0].CleanupTargets
			peer := unpublishedCleanupRelease(t, f, svc, scope)
			if err := svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
				t.Fatal(err)
			}
			if len(result.Actions[0].CleanupTargets) != 2 {
				t.Fatalf("missing peer: %+v", result.Actions[0].CleanupTargets)
			}
			for _, target := range result.Actions[0].CleanupTargets {
				if target.Origin != "current" || target.Binding.PlanID != scope.Plan || len(target.BindingRefs) != 1 {
					t.Fatal("release changed binding-reference origin")
				}
			}
			switch change {
			case "omitted":
				result.Actions[0].CleanupTargets = result.Actions[0].CleanupTargets[:1]
			case "namespace":
				result.Actions[0].CleanupTargets[0].Scope.NamespaceUID = "foreign"
			case "uid":
				result.Actions[0].CleanupTargets[0].Accepted[0].Outcome.UID = "foreign"
			case "foreign":
				result.Actions[0].CleanupTargets[0].Scope.Project = "foreign"
			case "legacy-review":
				result.Actions[0].CleanupTargets = original
				result.Actions[0].CleanupTargets[0].BindingRefs = nil
			case "late-owner":
				if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) SELECT org_id,'late-owner',epoch,body FROM loomplane_managed_workload_authority WHERE release_id=$1`, peer.Release); err != nil {
					t.Fatal(err)
				}
			}
			saveCleanupReview(t, f, plan, result)
			ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
			ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now())
			if change == "complete" {
				if err != nil || !ok {
					t.Fatalf("approval: %v %v", ok, err)
				}
			} else if err == nil || ok {
				t.Fatal("incomplete or mismatched review approved")
			}
		})
	}
}

func TestManagedCleanupClosureIssuer(t *testing.T) {
	for _, change := range []string{"complete", "late-owner", "foreign-connection-owner", "revoked-peer", "unknown-peer", "wrong-namespace", "wrong-uid", "update"} {
		t.Run(change, func(t *testing.T) {
			var peer kube.ManagedScope
			f := prepareCleanupIssuerFixture(t, change == "update", func(f *poolFixture, svc *planning.Service, _ *planning.Plan, _ *planning.Result, scope kube.ManagedScope) {
				peer = unpublishedCleanupRelease(t, f, svc, scope)
			})
			var raw []byte
			if err := f.sql.QueryRow(t.Context(), `SELECT body FROM loomplane_managed_cleanup_intents WHERE plan_id=$1`, f.selection.Plan).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var targets []planning.ManagedCleanupTarget
			if err := json.Unmarshal(raw, &targets); err != nil {
				t.Fatal(err)
			}
			if len(targets) != 2 {
				t.Fatal("missing peer")
			}
			grants := make([]*planning.ManagedCleanupGrant, 0, len(targets))
			for index := range targets {
				sel := f.selection
				sel.TargetIndex = index
				g, err := f.store.Open(f.ctx, sel)
				if err != nil {
					t.Fatal(err)
				}
				grants = append(grants, g)
				if index == 0 {
					if _, err = f.store.Journal("org", g.ID).Issue(f.ctx, g.Target.Scope, f.request); err == nil {
						t.Fatal("issued before peer revocation")
					}
				}
			}
			for _, g := range grants {
				again, err := f.store.Open(f.ctx, g.Selection)
				if err != nil || again.ID != g.ID {
					t.Fatalf("recovery: %+v %v", again, err)
				}
			}
			g := grants[0]
			switch change {
			case "late-owner":
				if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) SELECT org_id,'later-C',epoch,body FROM loomplane_managed_workload_authority WHERE release_id=$1`, peer.Release); err != nil {
					t.Fatal(err)
				}
			case "foreign-connection-owner":
				if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) SELECT org_id,'foreign-connection-owner',epoch,jsonb_set(body,'{Connection}','"other-connection"') FROM loomplane_managed_workload_authority WHERE release_id=$1`, peer.Release); err != nil {
					t.Fatal(err)
				}
			case "revoked-peer":
				if err := f.store.Revoke(f.ctx, "org", grants[1].ID); err != nil {
					t.Fatal(err)
				}
			case "unknown-peer":
				if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request) SELECT 'unknown-peer',org_id,release_id,epoch,body,'{}'::jsonb FROM loomplane_managed_workload_authority WHERE release_id=$1`, peer.Release); err != nil {
					t.Fatal(err)
				}
			case "wrong-namespace":
				f.request.Namespace = "foreign"
			case "wrong-uid":
				f.request.UID = "foreign"
			}
			id, err := f.store.Journal("org", g.ID).Issue(f.ctx, g.Target.Scope, f.request)
			if change == "complete" {
				if err != nil || id == "" {
					t.Fatalf("complete issuance: %s %v", id, err)
				}
				if _, err = f.store.Journal("org", grants[1].ID).Issue(f.ctx, grants[1].Target.Scope, f.request); err != nil {
					t.Fatalf("exact peer retry: %v", err)
				}
				var unknown int
				if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_managed_workload_issuances WHERE cleanup_grant_id IS NOT NULL AND outcome IS NULL`).Scan(&unknown); err != nil || unknown != 2 {
					t.Fatalf("unknown history: %d %v", unknown, err)
				}
			} else if err == nil {
				t.Fatal("unsafe coauthorization succeeded")
			}
		})
	}
}

func TestManagedCleanupClosureRepeatedBinding(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "update"}[retire], func(t *testing.T) {
			f := prepareCleanupIssuerFixture(t, retire, func(f *poolFixture, _ *planning.Service, _ *planning.Plan, _ *planning.Result, _ kube.ManagedScope) {
				b, err := f.plans.GetBinding(t.Context(), "org", "cleanup-binding")
				if err != nil {
					t.Fatal(err)
				}
				if err = f.plans.SetReplaced(t.Context(), "org", b.ID, []planning.Binding{*b, *b}); err != nil {
					t.Fatal(err)
				}
			})
			g := f.open(t)
			if len(g.Target.BindingRefs) != 2 {
				t.Fatalf("references: %+v", g.Target.BindingRefs)
			}
			if retire && g.Target.Kind != planning.ManagedCleanupRetireDependents || !retire && g.Target.Kind != planning.ManagedCleanupRemoveRelease {
				t.Fatal("alias changed purpose")
			}
			again, err := f.store.Open(f.ctx, f.selection)
			if err != nil || again.ID != g.ID {
				t.Fatalf("duplicate recovery: %v", err)
			}
			_, err = f.store.Journal("org", g.ID).Issue(f.ctx, g.Target.Scope, f.request)
			if retire && err == nil || !retire && err != nil {
				t.Fatalf("purpose: %v", err)
			}
		})
	}
}

func TestManagedCleanupClosureHistoricalApproval(t *testing.T) {
	f, svc, plan, result, scope := cleanupTargetFixture(t)
	var closure *migrate.Migration
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "managed_cleanup_target_closure" {
			closure = m
		}
	}
	if closure == nil {
		t.Fatal("missing migration")
	}
	executor := pgmigrate.New(f.db)
	var functionOID uint32
	if err := f.sql.QueryRow(t.Context(), `SELECT 'loomplane_managed_cleanup_targets(text,text,text,text,text)'::regprocedure::oid`).Scan(&functionOID); err != nil {
		t.Fatal(err)
	}
	if err := closure.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	_ = unpublishedCleanupRelease(t, f, svc, scope)
	result.Actions[0].CleanupTargets = nil
	if err := svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	if len(result.Actions[0].CleanupTargets) != 1 || result.Actions[0].CleanupTargets[0].BindingRefs != nil {
		t.Fatal("historical producer changed")
	}
	saveCleanupReview(t, f, plan, result)
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
	if ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); err != nil || !ok {
		t.Fatalf("historical approval: %v %v", ok, err)
	}
	var before, after []byte
	if err := f.sql.QueryRow(t.Context(), `SELECT result FROM loomplane_plans WHERE id=$1`, plan.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := closure.Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	var afterOID uint32
	if err := f.sql.QueryRow(t.Context(), `SELECT 'loomplane_managed_cleanup_targets(text,text,text,text,text)'::regprocedure::oid`).Scan(&afterOID); err != nil || afterOID != functionOID {
		t.Fatalf("function identity changed: %d %d %v", functionOID, afterOID, err)
	}
	if err := f.sql.QueryRow(t.Context(), `SELECT result FROM loomplane_plans WHERE id=$1`, plan.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("migration rewrote historical approval")
	}
	if err := svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	if len(result.Actions[0].CleanupTargets) != 2 {
		t.Fatal("fresh review still omits unpublished owner")
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=$2 WHERE id=$1`, plan.ID, after); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCleanupClosureTakeover(t *testing.T) {
	for _, change := range []string{"reviewed-history", "different-runtime", "future-epoch", "post-review-outcome"} {
		t.Run(change, func(t *testing.T) {
			f := prepareCleanupIssuerFixture(t, false, func(f *poolFixture, _ *planning.Service, _ *planning.Plan, _ *planning.Result, scope kube.ManagedScope) {
				attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: scope.Org, Project: scope.Project, Environment: scope.Environment, Service: scope.Service, Plan: scope.Plan, Deployment: scope.Deployment}, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				scope.Epoch = attempt.Generation
				if err = kube.NewControllerStore(f.db).WithClusterCapacity(true).OpenManagedScope(t.Context(), scope); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "different-runtime":
					if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_managed_workload_issuances SET scope=jsonb_set(scope,'{RuntimeRef}','"different-runtime"') WHERE request->>'Resource'='deployments'`); err != nil {
						t.Fatal(err)
					}
				case "future-epoch":
					if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_managed_workload_issuances SET epoch=epoch+10,scope=jsonb_set(scope,'{Epoch}',to_jsonb(epoch+10)) WHERE request->>'Resource'='deployments'`); err != nil {
						t.Fatal(err)
					}
				}
			})
			if change == "post-review-outcome" {
				if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request,outcome,resolved_at) SELECT 'unreviewed-accepted',org_id,release_id,epoch,scope,request,outcome,resolved_at FROM loomplane_managed_workload_issuances WHERE request->>'Resource'='deployments'`); err != nil {
					t.Fatal(err)
				}
			}
			g := f.open(t)
			if change == "reviewed-history" && g.Target.Accepted[0].Epoch >= g.Target.Scope.Epoch {
				t.Fatal("fixture did not retain earlier epoch")
			}
			_, err := f.store.Journal("org", g.ID).Issue(f.ctx, g.Target.Scope, f.request)
			if change == "reviewed-history" {
				if err != nil {
					t.Fatalf("reviewed takeover history blocked: %v", err)
				}
			} else if err == nil {
				t.Fatal("unreviewed takeover evidence authorized")
			}
		})
	}
}
