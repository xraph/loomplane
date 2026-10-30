//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestManagedCleanupClosureSharedRuntimeFailure(t *testing.T) {
	path := os.Getenv("LOOMPLANE_TEST_MANAGED_KUBECONFIG")
	if path == "" {
		t.Skip("requires disposable managed kind cluster")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "closure-" + uuid.NewString()[:8]
	cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := admin.Kube.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = admin.Kube.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
	}()
	ca := cfg.Details["cluster_ca"]
	f, svc, plan, result, _ := managedDemandFixture(t, map[string]string{"namespace": namespace, "namespace_uid": string(ns.UID), "cluster_ca": ca})
	result.Actions[0].Service.CapacityMode = "best_effort"
	if err = svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, plan, result)
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('closure-runtime','org','proj','env','production',$1,'old','running','usr',now())`, plan.ID); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: "closure-runtime"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	iid := id.New(id.PrefixInstance)
	scope := kube.ManagedScope{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: attempt.Deployment, Action: "service:api", Connection: "cluster-runtime", Instance: iid.String(), RuntimeRef: "org/env/api/runtime_closure", RuntimeRevision: attempt.Deployment + "/service:api", Namespace: namespace, NamespaceUID: string(ns.UID), ClusterCA: ca, Epoch: attempt.Generation}
	scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
	journal := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if err = journal.OpenManagedScope(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	apply := func(journal *kube.ControllerStore, current kube.ManagedScope, image string, wait time.Duration) error {
		currentConfig := cfg
		currentConfig.ManagedWorkload, currentConfig.ManagedJournal, currentConfig.ManagedScope = true, journal, current
		currentConfig.Details["namespace_uid"] = current.NamespaceUID
		client, clientErr := kube.New(currentConfig)
		if clientErr != nil {
			return clientErr
		}
		runtime := &kube.Runtime{Session: func(context.Context, map[string]string) (*kube.Client, error) { return client, nil }, RecordController: journal.RecordController, Wait: wait}
		labels := map[string]string{owned.LabelOrg: current.Org, owned.LabelProject: current.Project, owned.LabelEnvironment: current.Environment, owned.LabelService: current.Service, owned.LabelPlan: current.Plan, owned.LabelConnection: current.Connection, owned.LabelAction: current.Action, owned.LabelRuntime: current.RuntimeRef, owned.LabelRevision: current.RuntimeRevision}
		_, applyErr := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "api", Image: image, Resources: cpprovider.ResourceSpec{CPUMillis: 100, MemoryMB: 32}}, labels)
		return applyErr
	}
	// The disposable kind node already carries this pause image.
	if err = apply(journal, scope, "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a", 45*time.Second); err != nil {
		t.Fatal(err)
	}
	first, err := journal.GetController(t.Context(), scope.Org, scope.Connection, scope.Instance, scope.RuntimeRef)
	if err != nil {
		t.Fatal(err)
	}
	binding := &planning.Binding{ID: "closure-binding", OrgID: scope.Org, ProjectID: scope.Project, EnvironmentID: scope.Environment, Subject: planning.ServiceSubject, Name: scope.Service, Provider: "kubernetes", ExternalID: scope.Instance, PlanID: scope.Plan, ConnectionID: scope.Connection, Revision: "ready-A", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = f.plans.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	peer := unpublishedCleanupRelease(t, f, svc, scope, func(journal *kube.ControllerStore, current kube.ManagedScope) {
		applyErr := apply(journal, current, "registry.invalid/api@sha256:"+strings.Repeat("a", 64), time.Nanosecond)
		if applyErr == nil {
			t.Fatal("unpullable update unexpectedly ready")
		}
		t.Logf("unpublished update readiness: %v", applyErr)
	})
	second, err := journal.GetController(t.Context(), peer.Org, peer.Connection, peer.Instance, peer.RuntimeRef)
	if err != nil {
		t.Fatal(err)
	}
	if first.DeploymentUID != second.DeploymentUID || second.ReleaseID != peer.Release || second.DeploymentGeneration <= first.DeploymentGeneration {
		t.Fatalf("shared receipt mismatch: %+v %+v", first, second)
	}
	saved, err := f.plans.GetBinding(t.Context(), "org", binding.ID)
	if err != nil || saved.PlanID != scope.Plan {
		t.Fatalf("binding A changed: %+v %v", saved, err)
	}
	removal := &planning.Plan{ID: "closure-remove", OrgID: scope.Org, ProjectID: scope.Project, EnvironmentID: scope.Environment}
	reviewed := &planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: planning.Delete, Provider: "kubernetes"}}}
	if err = svc.PrepareManagedCleanupTargets(t.Context(), removal, reviewed); err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Actions[0].CleanupTargets) != 2 {
		t.Fatal("review omitted unpublished runtime receipt")
	}
	saveCleanupReview(t, f, removal, reviewed)
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
	if ok, approvalErr := f.plans.Decide(ctx, "org", removal.ID, "approved", "reviewer", "", time.Now()); approvalErr != nil || !ok {
		t.Fatalf("review approval: %v %v", ok, approvalErr)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('closure-removal','org','proj','env','production',$1,'cleanup','running','reviewer',now())`, removal.ID); err != nil {
		t.Fatal(err)
	}
	roles := memory.New()
	engine, err := warden.NewEngine(warden.WithStore(roles))
	if err != nil {
		t.Fatal(err)
	}
	auth := security.NewAuthorizer(engine, roles, func(_ context.Context, org, user string) (string, bool, error) {
		return security.MemberOwner, org == "org" && user == "reviewer", nil
	})
	if err = auth.Assign(t.Context(), "org", "reviewer", security.RoleOwner, "test"); err != nil {
		t.Fatal(err)
	}
	cleanup := planning.NewManagedCleanupStore(f.db, auth, nil)
	var grant *planning.ManagedCleanupGrant
	for index := range reviewed.Actions[0].CleanupTargets {
		g, openErr := cleanup.Open(ctx, planning.ManagedCleanupSelection{Org: "org", Plan: removal.ID, Action: "service:api", Deployment: "closure-removal", TargetIndex: index})
		if openErr != nil {
			t.Fatal(openErr)
		}
		if g.Target.Scope.Release == scope.Release {
			grant = g
		}
	}
	if grant == nil {
		t.Fatal("missing old grant")
	}
	request := kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: namespace, Name: kube.WorkloadName(iid), UID: first.DeploymentUID, Method: "DELETE"}
	if _, err = cleanup.Journal("org", grant.ID).Issue(ctx, scope, request); err != nil {
		t.Fatalf("shared runtime receipt issuance: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_workload_controllers SET body=jsonb_set(body,'{namespace_uid}','"foreign"') WHERE runtime_ref=$1`, peer.RuntimeRef); err != nil {
		t.Fatal(err)
	}
	if _, err = cleanup.Journal("org", grant.ID).Issue(ctx, scope, request); err == nil {
		t.Fatal("mismatched peer receipt authorized")
	}
	if _, err = admin.Kube.AppsV1().Deployments(namespace).Get(t.Context(), kube.WorkloadName(iid), metav1.GetOptions{}); err != nil {
		t.Fatal("review removed shared deployment", err)
	}
}
