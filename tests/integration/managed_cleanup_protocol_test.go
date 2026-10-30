//go:build integration

package integration

import (
	"encoding/json"

	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/store/memory"

	"github.com/xraph/loomplane/internal/security"

	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"

	"github.com/xraph/loomplane/internal/orchestration"
)

func TestManagedCleanupProtocolPersistence(t *testing.T) {
	f, _, plan, result, _ := cleanupTargetFixture(t)
	if result.RequiredRuntimeProtocol != orchestration.ManagedCleanupProtocol {
		t.Fatal("cleanup without new demand has no protocol")
	}
	saveCleanupReview(t, f, plan, result)
	for _, build := range []string{"deployment-v2", "deployment-v3", "deployment-v4", "deployment-v5"} {
		if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org',$1,$2,jsonb_build_object('build_id',$2::text))`, plan.ID, build); err == nil {
			t.Fatal("accepted old build", build)
		}
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org',$1,'cleanup-route','{"build_id":"deployment-v6"}')`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_participants(org_id,plan_id,workflow_id) VALUES('org',$1,'cleanup-route')`, plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE loomplane_plans SET result=result-'required_runtime_protocol' WHERE id='cleanup-new-plan'`,
		`UPDATE loomplane_plans SET result=jsonb_set(result,'{required_runtime_protocol}','"managed-workload-journal-v1"') WHERE id='cleanup-new-plan'`,
		`UPDATE loomplane_plans SET required_runtime_protocol='' WHERE id='cleanup-new-plan'`,
		`UPDATE loomplane_deployment_workflow_runs SET plan_id='unmanaged' WHERE workflow_id='cleanup-route'`,
		`UPDATE loomplane_deployment_workflow_runs SET request='{"build_id":"deployment-v5"}' WHERE workflow_id='cleanup-route'`,
		`UPDATE loomplane_deployment_workflow_participants SET plan_id='unmanaged' WHERE workflow_id='cleanup-route'`,
		`DELETE FROM loomplane_deployment_workflow_participants WHERE workflow_id='cleanup-route'`,
	} {
		_, err := f.sql.Exec(t.Context(), query)
		if err == nil {
			// Derived columns always restore the authoritative JSON value.
			if query == `UPDATE loomplane_plans SET required_runtime_protocol='' WHERE id='cleanup-new-plan'` {
				var got string
				if err = f.sql.QueryRow(t.Context(), `SELECT required_runtime_protocol FROM loomplane_plans WHERE id='cleanup-new-plan'`).Scan(&got); err == nil && got == orchestration.ManagedCleanupProtocol {
					continue
				}
			}
			t.Fatal("accepted protocol tampering", query, err)
		}
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org','unmanaged','old-route','{"build_id":"deployment-v5"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_participants(org_id,plan_id,workflow_id) VALUES('org',$1,'old-route')`, plan.ID); err == nil {
		t.Fatal("old participant route accepted cleanup")
	}
}

func TestManagedCleanupProtocolApplicationProfile(t *testing.T) {
	f, svc, plan, result, _ := managedDemandFixture(t, map[string]string{"namespace": "apps", "namespace_uid": "namespace-uid", "cluster_ca": strings.Repeat("a", 64)})
	result.Actions[0].Service.CapacityMode = "best_effort"
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	result.RequiredRuntimeProtocol = orchestration.ManagedCleanupProtocol
	saveManagedDemandPlan(t, f, plan, result)
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('cleanup-protocol-deployment','org','proj','env','production',$1,'old','running','usr',now())`, plan.ID); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: "cleanup-protocol-deployment"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scope := kube.ManagedScope{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: attempt.Deployment, Action: "service:api", Connection: "cluster-runtime", Instance: "fresh-instance", RuntimeRef: "org/env/api/runtime_cleanup_protocol", RuntimeRevision: attempt.Deployment + "/service:api", Namespace: "apps", NamespaceUID: "namespace-uid", ClusterCA: strings.Repeat("a", 64), Epoch: attempt.Generation}
	scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
	journal := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	identity := kube.ManagedRuntimeIdentity{Org: scope.Org, Project: scope.Project, Environment: scope.Environment, Service: scope.Service, Plan: scope.Plan, Connection: scope.Connection, Instance: scope.Instance, RuntimeRef: scope.RuntimeRef, RuntimeRevision: scope.RuntimeRevision, Namespace: scope.Namespace, NamespaceUID: scope.NamespaceUID, ClusterCA: scope.ClusterCA}
	profile, err := journal.WithClusterCapacity(false).ManagedProfileFor(t.Context(), identity)
	if err != nil || profile != kube.ManagedProfileJournal {
		t.Fatalf("fresh release downgraded: %s %v", profile, err)
	}
	if err = journal.OpenManagedScope(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	req := kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: scope.Namespace, Name: scope.Instance, Method: "POST"}
	issuance, err := journal.Issue(t.Context(), scope, req)
	if err != nil {
		t.Fatal(err)
	}
	disabled := journal.WithClusterCapacity(false)
	if _, err = disabled.Issue(t.Context(), scope, req); !errors.Is(err, kube.ErrManagedCapacityDisabled) {
		t.Fatalf("disabled effects: %v", err)
	}
	if err = disabled.Resolve(t.Context(), issuance, scope, kube.ManagedOutcome{State: "accepted", UID: "fresh-uid", StatusCode: 201}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCleanupCloudSyncProtocolRoute(t *testing.T) {
	f, svc, preview, _, _ := cleanupSyncFixture(t)
	accepted, err := svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"}, "proj", preview.ID, preview.Revision, "protocol")
	if err != nil {
		t.Fatal(err)
	}
	planID := accepted.Environments[0].PlanID
	saved, err := f.plans.GetPlan(t.Context(), "org", planID)
	if err != nil || saved.RequiredRuntimeProtocol != orchestration.ManagedCleanupProtocol {
		t.Fatalf("accepted protocol lost: %+v %v", saved, err)
	}
	store := memory.New()
	dispatcher, err := dispatch.New(dispatch.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	workflow := orchestration.NewDeployments()
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(workflow.Options("legacy", 1)))
	if err != nil {
		t.Fatal(err)
	}
	workflow.Bind(svc, nil, func() *engine.Engine { return eng })
	workflow.BindCloudSync(svc)
	routes := orchestration.NewPgRunStore(f.db)
	workflow.BindRunStore(routes)
	worker, err := drt.NewWorker(store, workflow.ManagedCleanupOptions("cleanup", 1))
	if err != nil {
		t.Fatal(err)
	}
	workflow.BindWorker(orchestration.ManagedCleanupBuild, worker)
	if err = workflow.StartCloudSync(t.Context(), "org", "proj", accepted.ID, accepted.Attempt); err != nil {
		t.Fatal(err)
	}
	route, err := routes.ForPlan(t.Context(), accepted.ID)
	if err != nil || route.Request.BuildID != orchestration.ManagedCleanupBuild {
		t.Fatalf("cleanup-only sync routed incorrectly: %+v %v", route, err)
	}
	if err = routes.AttachPlan(t.Context(), "org", planID, route.Request.WorkflowID); err != nil {
		t.Fatal(err)
	}
	if worked, claimErr := worker.RunOnce(t.Context(), durable.TaskWorkflow); claimErr != nil || !worked {
		t.Fatalf("actual sync task claim: %v %v", worked, claimErr)
	}
}

func TestManagedCleanupCloudSyncProtocolTampering(t *testing.T) {
	for _, protocol := range []string{"", "managed-workload-journal-v1"} {
		t.Run(protocol, func(t *testing.T) {
			f, _, preview, snapshot, sync := cleanupSyncFixture(t)
			var result map[string]any
			if err := json.Unmarshal(snapshot.Environments[0].AcceptedPlan.Result, &result); err != nil {
				t.Fatal(err)
			}
			if protocol == "" {
				delete(result, "required_runtime_protocol")
			} else {
				result["required_runtime_protocol"] = protocol
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Environments[0].AcceptedPlan.Result = raw
			if _, err = f.clouds.Accept(security.WithPrincipal(t.Context(), sync.Actor), preview, snapshot, sync, "tamper"); err == nil {
				t.Fatal("accepted stripped or mismatched cleanup protocol")
			}
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_syncs`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial acceptance: %d %v", count, err)
			}
		})
	}
}
