//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/xraph/ctrlplane/app"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	cppg "github.com/xraph/ctrlplane/store/postgres"
	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/migrate"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/ops"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestManagedInstalledApplicationPending(t *testing.T) {
	for _, mode := range []string{"guaranteed", "best_effort"} {
		t.Run(mode, func(t *testing.T) { managedCleanupEffectsRealAPI(t, false, "application", mode) })
	}
}

// The admission policy has known supply. This kind namespace tests the real
// Kubernetes execution and readiness path independently of provider node supply.
func installedApplication(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, f *poolFixture, creds *connections.Service, infra *cloudinfra.Service, svc *planning.Service, p *planning.Plan, res *planning.Result, mode string) {
	t.Helper()
	missingMetrics := mode == "hpa-missing"
	testHPA := mode == "hpa" || missingMetrics
	if testHPA {
		mode = "guaranteed"
	}
	a := &res.Actions[0]
	cfg := a.Service
	c, _, err := creds.Credential(ctx, p.OrgID, cfg.Target)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TargetSettings = planning.ReviewedConnectionSettings(c.Cloud, c.Details)
	assignment, err := f.clouds.Resolve(ctx, p.OrgID, p.ProjectID, p.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	targetPin, err := f.clouds.GetTarget(ctx, p.OrgID, p.ProjectID, cfg.CloudPlacement.CloudID, cfg.CloudPlacement.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CloudPlacement.CloudRevision = assignment.CloudRevision
	cfg.CloudPlacement.AssignmentRevision = assignment.Revision
	cfg.CloudPlacement.TargetName, cfg.CloudPlacement.TargetRevision = targetPin.Name, targetPin.Revision
	cfg.CloudPlacement.AccountIdentity = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	cfg.CloudPlacement.Region, cfg.Region, a.Region = targetPin.Region, targetPin.Region, targetPin.Region

	a.Engine = "ctrlplane"
	cfg.Workload = provider.Worker
	cfg.CapacityMode = mode
	cfg.Scale = &manifest.Scale{Min: 1, Max: 1}
	if testHPA {
		cfg.Scale = &manifest.Scale{Min: 2, Max: 3}
	}
	cfg.BuildTarget = &planning.BuildTarget{Image: "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a"}
	cfg.Env = []planning.EnvLink{{Subject: planning.ResourceSubject, Field: "consumer:url", From: "db"}}
	var blocked *cloudinfra.CapacityAdmissionError
	if checkErr := svc.PrepareClusterWorkloadDemands(ctx, p, res); !errors.As(checkErr, &blocked) || blocked.Block.Reason != "managed_consumer_cleanup_unqualified" {
		t.Fatal("accepted unqualified consumer", checkErr)
	}
	cfg.Env = nil
	if err = svc.PrepareClusterWorkloadDemands(ctx, p, res); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, p, res)
	ds := deployments.NewPgStore(e.pg)
	db, err := grove.Open(e.pg)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := app.New(app.WithStore(cppg.New(db)))
	if err != nil {
		t.Fatal(err)
	}
	proofKey := make([]byte, 32)
	if _, keyErr := rand.Read(proofKey); keyErr != nil {
		t.Fatal(keyErr)
	}
	verifier := kube.NewControllerStore(e.pg).WithReplicaProof(proofKey)
	infra.ReplicaVerifier = verifier
	target := &deployments.OwnedTarget{Cloud: connections.Kubernetes, Ctrl: deployments.CtrlPlane{Providers: cp.Providers(), Instances: cp.Instances, Deploys: cp.Deploys}, Credentials: creds, Secrets: syncSecrets{}, Controllers: verifier.WithClusterCapacity(true)}
	reviewer := managedCleanupReviewer(t)
	store := planning.NewManagedCleanupStore(e.pg, reviewer, nil).WithReplicaProof(proofKey)
	ce := &deployments.ManagedCleanupExecution{Store: store, Credentials: creds, Target: target, Routes: cloudinfra.NewService(cloudinfra.Deps{Store: e.store})}
	svc.BindManagedRuntime(ce)
	deps := deployments.RunnerDeps{Store: ds, Plans: f.plans, ManagedCleanup: ce, Capacity: svc, Credentials: creds, Variables: poolVars{}, Providers: providers.Registry(), CloudProfiles: e.clouds, Executors: map[string]deployments.Executor{"kubernetes": target}}
	runner := deployments.NewRunner(deps)
	rs := orchestration.NewPgRunStore(e.pg)
	saveInstalledDeploymentRoute(t, rs, p, orchestration.LegacyCleanupBuild)
	dep := &deployments.Deployment{ID: "installed-release", OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, Environment: "production", PlanID: p.ID, StartedBy: "usr", Status: deployments.StatusRunning, StartedAt: time.Now(), Steps: []deployments.Step{{ActionID: a.ID, Status: deployments.StepPending}}}
	if _, err = ds.CreateDeployment(ctx, dep); err != nil {
		t.Fatal(err)
	}
	if mode == "guaranteed" {
		svc.BindManagedRuntime(nil)
		var unqualified *cloudinfra.CapacityAdmissionError
		if gateErr := svc.ResolveCapacityIntents(orchestration.ManagedWorkloadContext(ctx), p.OrgID, p.ID); !errors.As(gateErr, &unqualified) || unqualified.Block.Reason != "managed_workload_execution_unqualified" {
			t.Fatal("missing executor", gateErr)
		}
		for _, missing := range []string{"vault", "credential resolver", "managed credentials", "ctrlplane deployments", "journal", "registered backend", "cleanup authorizer"} {
			partial := *target
			partial.Ctrl.Providers = cpprovider.NewRegistry()
			switch missing {
			case "vault":
				partial.Secrets = nil
			case "credential resolver":
				partial.Credentials = nil
			case "ctrlplane deployments":
				partial.Ctrl.Deploys = nil
			case "journal":
				partial.Controllers = kube.NewControllerStore(e.pg)
			case "registered backend":
				partial.Backend = func(string, string) owned.Backend { return &kube.Runtime{} }
			}
			incomplete := *ce
			incomplete.Target = &partial
			if missing == "managed credentials" {
				incomplete.Credentials = &connections.Service{}
				partial.Credentials = incomplete.Credentials
			}
			if missing == "cleanup authorizer" {
				incomplete.Store = planning.NewManagedCleanupStore(e.pg, nil, nil)
			}
			svc.BindManagedRuntime(&incomplete)
			if runErr := runner.Run(orchestration.ManagedWorkloadContext(ctx), p.OrgID, dep.ID, a.ID, nil, func([]byte) error { return nil }); !errors.Is(runErr, planning.ErrManagedCleanupUnavailable) && !errors.Is(runErr, kube.ErrManagedAuthority) {
				t.Fatal("partial executor", missing, runErr)
			}
		}
		svc.BindManagedRuntime(ce)
		var started bool
		if checkErr := e.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cluster_workload_holds WHERE plan_id=$1) OR EXISTS(SELECT 1 FROM loomplane_workload_attempts WHERE plan_id=$1) OR EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE scope->>'Plan'=$1)`, p.ID).Scan(&started); checkErr != nil || started {
			t.Fatal("partial executor reserved capacity or opened work", started, checkErr)
		}
	}
	ns := e.attachment().Namespace
	if missingMetrics {
		_, discoveryErr := e.bootstrap.Kube.Discovery().ServerResourcesForGroupVersion("metrics.k8s.io/v1beta1")
		if discoveryErr == nil {
			t.Skip("this case requires absent metrics")
		}
		runErr := runner.Run(orchestration.ManagedWorkloadContext(ctx), p.OrgID, dep.ID, a.ID, nil, func([]byte) error { return nil })
		if runErr == nil || !strings.Contains(runErr.Error(), "HPA permissions and the metrics API") {
			t.Fatal("missing metrics permitted elastic workload", runErr)
		}
		var issued bool
		if checkErr := e.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE scope->>'Plan'=$1)`, p.ID).Scan(&issued); checkErr != nil || issued {
			t.Fatal("issued writes without metrics", issued, checkErr)
		}
		return
	}
	var releasePressure func()
	if mode == "guaranteed" {
		if cfg.ClusterDemand.Input.Native == nil || cfg.ClusterDemand.Input.Native.MaximumNodes <= 1 {
			t.Fatal("missing reviewed future node supply")
		}
		releasePressure = reserveKindCPU(ctx, t, e.bootstrap.Kube, ns, cfg.BuildTarget.Image)
	}
	quotas := e.bootstrap.Kube.CoreV1().ResourceQuotas(ns)
	quota, err := quotas.Create(ctx, &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "fixture-readiness"}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourcePods: apiresource.MustParse("0")}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(call context.Context) (bool, error) {
		q, rerr := quotas.Get(call, quota.Name, metav1.GetOptions{})
		if rerr != nil {
			return false, rerr
		}
		_, ok := q.Status.Hard[corev1.ResourcePods]
		return ok, nil
	}); err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(orchestration.ManagedWorkloadContext(ctx), 150*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(call, p.OrgID, dep.ID, a.ID, nil, func([]byte) error { return nil }) }()
	var receipt kube.ControllerReceipt
	var pending *appsv1.Deployment
	if err = wait.PollUntilContextTimeout(call, 150*time.Millisecond, 60*time.Second, true, func(c context.Context) (bool, error) {
		select {
		case runErr := <-done:
			if runErr != nil {
				return false, runErr
			}
			return false, context.Canceled
		default:
		}
		var raw []byte
		if qerr := e.sql.QueryRow(c, `SELECT coalesce((SELECT body FROM loomplane_workload_controllers WHERE org_id=$1 AND body->>'plan_id'=$2 LIMIT 1),'null'::jsonb)`, p.OrgID, p.ID).Scan(&raw); qerr != nil {
			return false, qerr
		}
		if string(raw) == "null" {
			return false, nil
		}
		if qerr := json.Unmarshal(raw, &receipt); qerr != nil {
			return false, qerr
		}
		list, qerr := e.bootstrap.Kube.AppsV1().Deployments(ns).List(c, metav1.ListOptions{})
		if qerr != nil {
			return false, qerr
		}
		for i := range list.Items {
			if string(list.Items[i].UID) == receipt.DeploymentUID {
				pending = &list.Items[i]
				break
			}
		}
		return pending != nil, nil
	}); err != nil {
		t.Fatal("pending receipt", err)
	}
	if !receipt.Valid() || pending.Status.AvailableReplicas != 0 {
		t.Fatal("reported ready before namespace admitted pods")
	}
	var bound, holds, credited int
	if err = e.sql.QueryRow(ctx, `SELECT (SELECT count(*) FROM loomplane_bindings WHERE org_id=$1 AND environment_id=$2 AND subject='service' AND name=$3),(SELECT count(*) FROM loomplane_cluster_workload_holds WHERE plan_id=$4 AND NOT released),(SELECT count(*) FROM loomplane_cluster_workload_holds WHERE plan_id=$4 AND coalesce(body->>'binding_id','')<>'')`, p.OrgID, p.EnvironmentID, a.Name, p.ID).Scan(&bound, &holds, &credited); err != nil || bound != 0 || credited != 0 || mode == "guaranteed" && holds != 1 || mode == "best_effort" && holds != 0 {
		t.Fatalf("pending binding/holds/credit: %d/%d/%d %v", bound, holds, credited, err)
	}
	quota, err = quotas.Get(ctx, quota.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	quota.Spec.Hard[corev1.ResourcePods] = apiresource.MustParse("10")
	if _, err = quotas.Update(ctx, quota, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if releasePressure != nil {
		requireSchedulerShortage(call, t, e.bootstrap.Kube, pending, receipt.ReleaseID)
		releasePressure()
	}
	select {
	case err = <-done:
	case <-call.Done():
		err = call.Err()
	}
	if err != nil {
		t.Fatal("ready runner", err)
	}
	live, err := e.bootstrap.Kube.AppsV1().Deployments(ns).Get(ctx, pending.Name, metav1.GetOptions{})
	if err != nil || live.Status.AvailableReplicas < 1 {
		t.Fatal("workload is not ready", err)
	}
	if err = e.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_bindings WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND plan_id=$4 AND name=$5 AND external_id=$6`, p.OrgID, p.ProjectID, p.EnvironmentID, p.ID, a.Name, receipt.InstanceID).Scan(&bound); err != nil || bound != 1 {
		t.Fatal("ready serving binding", bound, err)
	}
	if mode == "guaranteed" {
		if live.Spec.Template.Spec.NodeSelector[corev1.LabelArchStable] != cfg.ClusterDemand.Profile.Architecture {
			t.Fatal("running template lacks reviewed architecture")
		}
		if err = runner.Finish(call, p.OrgID, dep.ID, ""); err != nil {
			t.Fatal("finish original release", err)
		}
		if testHPA {
			checkInstalledHPA(ctx, t, e, infra, f, receipt, live, cfg, kube.NewControllerStore(e.pg).WithReplicaProof(proofKey))
			deletePlan := &planning.Plan{ID: "installed-hpa-remove", OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, CreatedBy: "reviewer"}
			removal := &planning.Result{Executable: true, Connections: res.Connections, Actions: []planning.Action{{ID: a.ID, Name: a.Name, Subject: planning.ServiceSubject, Op: planning.Delete, Provider: "kubernetes"}}}
			if checkErr := svc.PrepareManagedCleanupTargets(ctx, deletePlan, removal); checkErr != nil || len(removal.Actions[0].CleanupTargets) != 1 {
				t.Fatal("review progressed controller", checkErr)
			}
			saveCleanupReview(t, f, deletePlan, removal)
			actor := security.WithPrincipal(ctx, security.Principal{OrgID: p.OrgID, UserID: "reviewer"})
			if ok, checkErr := f.plans.Decide(actor, p.OrgID, deletePlan.ID, "approved", "reviewer", "", time.Now()); checkErr != nil || !ok {
				t.Fatal("approve progressed HPA cleanup", checkErr)
			}
			saveInstalledDeploymentRoute(t, rs, deletePlan, orchestration.ManagedCleanupBuild)
			removing := &deployments.Deployment{ID: "hpa-remove", OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, Environment: "production", PlanID: deletePlan.ID, StartedBy: "reviewer", Status: deployments.StatusRunning, StartedAt: time.Now(), Steps: []deployments.Step{{ActionID: a.ID, Status: deployments.StepPending}}}
			if _, checkErr := ds.CreateDeployment(ctx, removing); checkErr != nil {
				t.Fatal(checkErr)
			}
			if checkErr := runner.Run(orchestration.ManagedCleanupContext(actor), p.OrgID, removing.ID, a.ID, nil, func([]byte) error { return nil }); checkErr != nil {
				t.Fatal("remove progressed HPA deployment", checkErr)
			}
			var released bool
			if checkErr := e.sql.QueryRow(ctx, `SELECT released FROM loomplane_cluster_workload_holds WHERE org_id=$1 AND plan_id=$2`, p.OrgID, p.ID).Scan(&released); checkErr != nil || !released {
				t.Fatal("unreleased HPA maximum", released, checkErr)
			}
			return
		}
		at := e.attachment()
		op, upgradeErr := infra.PreviewCluster(ctx, e.principal, "cloud", accessInput(e, at))
		if upgradeErr != nil {
			t.Fatal(upgradeErr)
		}
		if _, upgradeErr = infra.ApplyCluster(ctx, e.principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); upgradeErr != nil {
			t.Fatal(upgradeErr)
		}
		if upgradeErr = infra.ReconcileRuntimeAccessOperation(ctx, p.OrgID, op.ID, nil); upgradeErr != nil {
			t.Fatal(upgradeErr)
		}
		svc = planning.NewService(planning.Deps{Store: f.plans, Authz: reviewer, Capacity: managedDemandAuthority{}})
		svc.BindManagedRuntime(ce)
		deps.Capacity = svc
		runner = deployments.NewRunner(deps)
		update := &planning.Plan{ID: "installed-update", OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, CreatedBy: "reviewer"}
		next := *cfg
		next.CapacityMode = "best_effort"
		next.ClusterDemand = nil
		action := *a
		action.Op = planning.Update
		action.Service = &next
		review := &planning.Result{Executable: true, Connections: res.Connections, Actions: []planning.Action{action}}
		if err = svc.PrepareClusterWorkloadDemands(ctx, update, review); err != nil {
			t.Fatal("update demand", err)
		}
		if err = svc.PrepareManagedCleanupTargets(ctx, update, review); err != nil {
			t.Fatal("update targets", err)
		}
		if len(review.Actions[0].CleanupTargets) != 1 || review.Actions[0].CleanupTargets[0].Kind != planning.ManagedCleanupRetireDependents {
			t.Fatal("unexpected update authority")
		}
		saveCleanupReview(t, f, update, review)
		actor := security.WithPrincipal(ctx, security.Principal{OrgID: p.OrgID, UserID: "reviewer"})
		if ok, approveErr := f.plans.Decide(actor, p.OrgID, update.ID, "approved", "reviewer", "", time.Now()); approveErr != nil || !ok {
			t.Fatal("update approval", approveErr)
		}
		saveInstalledDeploymentRoute(t, rs, update, orchestration.ManagedCleanupBuild)
		nextDep := &deployments.Deployment{ID: "installed-update-deploy", OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, PlanID: update.ID, Environment: "production", StartedBy: "reviewer", Status: deployments.StatusRunning, StartedAt: time.Now(), Steps: []deployments.Step{{ActionID: a.ID, Status: deployments.StepPending}}}
		if _, err = ds.CreateDeployment(ctx, nextDep); err != nil {
			t.Fatal(err)
		}
		nextCtx, stop := context.WithTimeout(orchestration.ManagedCleanupContext(actor), 120*time.Second)
		defer stop()
		if err = runner.Run(nextCtx, p.OrgID, nextDep.ID, a.ID, nil, func([]byte) error { return nil }); err != nil {
			t.Fatal("installed shared Update", err)
		}
		serving, readErr := e.bootstrap.Kube.AppsV1().Deployments(ns).Get(ctx, pending.Name, metav1.GetOptions{})
		if readErr != nil || serving.UID != live.UID || serving.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation] == receipt.ReleaseID || serving.Status.AvailableReplicas < 1 {
			t.Fatal("new serving instance not preserved", readErr)
		}
		var updated kube.ControllerReceipt
		var body []byte
		if queryErr := e.sql.QueryRow(ctx, `SELECT body FROM loomplane_workload_controllers WHERE org_id=$1 AND connection_id=$2 AND instance_id=$3 AND body->>'plan_id'=$4`, p.OrgID, receipt.ConnectionID, receipt.InstanceID, update.ID).Scan(&body); queryErr != nil {
			t.Fatal(queryErr)
		}
		if decodeErr := json.Unmarshal(body, &updated); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		receiptStore := kube.NewControllerStore(e.pg)
		if checkErr := receiptStore.CheckServingUpdate(ctx, p.OrgID, receipt.InstanceID, update.ID, receipt.ConnectionID, updated.RuntimeRevision); checkErr != nil {
			t.Fatal("verified serving update", checkErr)
		}
		for _, revision := range []string{updated.RuntimeRevision + "x", receipt.RuntimeRevision} {
			if checkErr := receiptStore.CheckServingUpdate(ctx, p.OrgID, receipt.InstanceID, update.ID, receipt.ConnectionID, revision); checkErr == nil {
				t.Fatal("unverified revision was serving", revision)
			}
		}
		tx, beginErr := e.pg.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer func() { _ = tx.Rollback() }()
		if _, insertErr := tx.Exec(ctx, `INSERT INTO cp_releases(id,tenant_id,instance_id,version,services,active) SELECT 'newer-fixture',tenant_id,instance_id,max(version)+1,'[]',true FROM cp_releases WHERE tenant_id=$1 AND instance_id=$2 GROUP BY tenant_id,instance_id`, p.OrgID, receipt.InstanceID); insertErr != nil {
			t.Fatal(insertErr)
		}
		if checkErr := updated.CheckServingUpdate(ctx, tx); checkErr == nil {
			t.Fatal("a superseded success still authorized cleanup")
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		var released, retired bool
		if err = e.sql.QueryRow(ctx, `SELECT h.released,EXISTS(SELECT 1 FROM loomplane_managed_retired_instances WHERE org_id=$1 AND instance_id=$3) FROM loomplane_cluster_workload_holds h WHERE h.org_id=$1 AND h.plan_id=$2`, p.OrgID, p.ID, receipt.InstanceID).Scan(&released, &retired); err != nil || !released || retired {
			t.Fatal("shared cleanup accounting", released, retired, err)
		}
		if err = runner.Finish(nextCtx, p.OrgID, nextDep.ID, ""); err != nil {
			t.Fatal(err)
		}
		// A stored binding from an older placement names the prior region.
		// The new reviewed target uses today's actual connection and region.
		prev, readErr := f.plans.ListBindings(ctx, p.OrgID, p.EnvironmentID)
		if readErr != nil || len(prev) != 1 {
			t.Fatal("current Update binding", readErr)
		}
		prev[0].Region = "previous-placement"
		if err = f.plans.PutBinding(ctx, prev[0]); err != nil {
			t.Fatal(err)
		}
		replace := *update
		replace.ID = "installed-replace"
		nextAction := action
		nextCfg := next
		nextCfg.ClusterDemand = nil
		nextAction.Service = &nextCfg
		nextAction.Op = planning.Replace
		nextReview := &planning.Result{Executable: true, Connections: res.Connections, Actions: []planning.Action{nextAction}}
		if err = svc.PrepareClusterWorkloadDemands(ctx, &replace, nextReview); err != nil {
			t.Fatal(err)
		}
		if err = svc.PrepareManagedCleanupTargets(ctx, &replace, nextReview); err != nil {
			t.Fatal(err)
		}
		saveCleanupReview(t, f, &replace, nextReview)
		if ok, approveErr := f.plans.Decide(actor, p.OrgID, replace.ID, "approved", "reviewer", "", time.Now()); approveErr != nil || !ok {
			t.Fatal("replace approval", approveErr)
		}
		if migrateErr := ops.Migrate(ctx, e.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); migrateErr != nil {
			t.Fatal(migrateErr)
		}
		wf := orchestration.NewDeployments()
		dispatchStore := dispatchpg.New(db)
		dispatcher, engineErr := dispatch.New(dispatch.WithStore(dispatchStore))
		if engineErr != nil {
			t.Fatal(engineErr)
		}
		eng, engineErr := engine.Build(dispatcher, engine.WithDurableWorkflows(wf.Options("legacy", 1)))
		if engineErr != nil {
			t.Fatal(engineErr)
		}
		wf.Bind(svc, runner, func() *engine.Engine { return eng })
		wf.BindRunStore(rs)
		worker, workerErr := drt.NewWorker(dispatchStore, wf.ManagedCleanupOptions("current", 1))
		if workerErr != nil {
			t.Fatal(workerErr)
		}
		wf.BindWorker(orchestration.ManagedCleanupBuild, worker)
		// Replay the saved approval through the public start and resume paths.
		if err = wf.Start(ctx, p.OrgID, replace.ID, time.Hour); err != nil {
			t.Fatal("start approved Replace", err)
		}
		pumpWorkflow(t, worker)
		firstRoute, routeErr := rs.ForPlan(ctx, replace.ID)
		if routeErr != nil || firstRoute.Request.BuildID != orchestration.ManagedCleanupBuild {
			t.Fatal("replacement build", routeErr)
		}
		if err = wf.ResumeRelease(ctx, p.OrgID, replace.ID); err != nil {
			t.Fatal(err)
		}
		pumpWorkflow(t, worker)
		replacement, readErr := ds.ForPlan(ctx, p.OrgID, replace.ID)
		if readErr != nil {
			t.Fatal("installed Replace", readErr)
		}
		if replacement.Status != deployments.StatusSucceeded {
			t.Fatal("installed Replace", replacement.Status, replacement.Error, replacement.Steps)
		}
		rows, readErr := f.plans.ListBindings(ctx, p.OrgID, p.EnvironmentID)
		if readErr != nil || len(rows) != 1 || rows[0].ExternalID == receipt.InstanceID || rows[0].PlanID != replace.ID || len(rows[0].Replaced) != 0 {
			t.Fatal("replacement binding", readErr)
		}
		if checkErr := e.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_retired_instances WHERE org_id=$1 AND instance_id=$2) AND NOT EXISTS(SELECT 1 FROM cp_instances WHERE tenant_id=$1 AND id=$2) AND EXISTS(SELECT 1 FROM cp_instances WHERE tenant_id=$1 AND id=$3 AND state='running')`, p.OrgID, receipt.InstanceID, rows[0].ExternalID).Scan(&retired); checkErr != nil || !retired {
			t.Fatal("replacement metadata", retired, checkErr)
		}
		if _, readErr = e.bootstrap.Kube.AppsV1().Deployments(ns).Get(ctx, pending.Name, metav1.GetOptions{}); !apierrors.IsNotFound(readErr) {
			t.Fatal("retired Deployment remains", readErr)
		}
		if err = e.sql.QueryRow(ctx, `SELECT body FROM loomplane_workload_controllers WHERE org_id=$1 AND instance_id=$2`, p.OrgID, rows[0].ExternalID).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(body, &receipt); err != nil {
			t.Fatal(err)
		}
	}
	adapters := cloudinfra.NewAdapters()
	if err = adapters.RegisterCluster("digitalocean", clusterCapacityKindReader{e.provider}, provider.ClusterDiscovery, provider.ClusterBootstrapAccess, provider.ClusterCapacityObservation); err != nil {
		t.Fatal(err)
	}
	infra.Adapters = adapters
	observed, err := infra.ObserveClusterCapacity(ctx, e.principal, "cloud", "cluster")
	if err != nil {
		t.Fatal("current Kubernetes observation", err)
	}
	if observed.Complete {
		t.Fatal("kind is not qualified DigitalOcean supply")
	}
	var found bool
	for _, w := range observed.Workloads {
		if w.DeploymentUID == receipt.DeploymentUID && w.ReplicaSetUID != "" && w.PodUID != "" && w.ReleaseID != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("ready workload missing from native ownership observation")
	}
}

func saveInstalledDeploymentRoute(t *testing.T, store *orchestration.PgRunStore, p *planning.Plan, build string) {
	t.Helper()
	raw, err := json.Marshal(orchestration.DeploymentInput{OrgID: p.OrgID, PlanID: p.ID, Window: time.Hour, ReconcileInterval: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	req := durable.StartRequest{Key: durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: p.ID, RunID: "1"}, RequestID: "start:" + p.ID, WorkflowType: orchestration.WorkflowDeployment, BuildID: build, Queue: orchestration.DurableQueue, Input: raw}
	if _, err = store.Save(t.Context(), orchestration.DeploymentRun{OrgID: p.OrgID, PlanID: p.ID, Request: req}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedInstalledLegacyApplication(t *testing.T) {
	managedCleanupEffectsRealAPI(t, false, "legacy")
}

func installedLegacyApplication(t *testing.T, e *enrollmentServiceFixture, f *poolFixture, creds *connections.Service) {
	t.Helper()
	ctx := t.Context()
	at := e.attachment()
	connection, _, err := creds.Credential(ctx, "org", at.RuntimeConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	settings := planning.ReviewedConnectionSettings(connection.Cloud, connection.Details)
	a := planning.Action{ID: "service:api", Name: "api", Op: planning.Create, ConnectionID: connection.ID, Provider: "kubernetes", Engine: "ctrlplane", Subject: planning.ServiceSubject, Service: &planning.ServiceConfig{Target: connection.ID, Provider: "kubernetes", TargetSettings: settings}}
	result := &planning.Result{Executable: true, Connections: map[string]string{"kubernetes": connection.ID}, Actions: []planning.Action{a}}
	p := &planning.Plan{ID: "legacy-installed", OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}
	svc := planning.NewService(planning.Deps{Store: f.plans, Capacity: managedDemandAuthority{disabled: true}})
	if err = svc.PrepareClusterWorkloadDemands(ctx, p, result); err != nil || result.Actions[0].Service.ClusterDemand != nil {
		t.Fatal("legacy demand", err)
	}
	saveCleanupReview(t, f, p, result)
	acceptFuture(t, f, p.ID)
	key := connection.ID
	registry := cpprovider.NewRegistry()
	store := kube.NewControllerStore(e.pg)
	target := &deployments.OwnedTarget{Cloud: connections.Kubernetes, Credentials: creds, Controllers: store, Ctrl: deployments.CtrlPlane{Providers: registry}}
	registered, err := registry.Get(target.Register("org", key))
	if err != nil {
		t.Fatal(err)
	}
	runtime := registered.(*owned.Provider).Backend.(*kube.Runtime)
	runtime.Values = func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{}, nil
	}
	cfg, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: "env", owned.LabelService: "api", owned.LabelPlan: p.ID, owned.LabelConnection: key, owned.LabelAction: a.ID, owned.LabelRuntime: "org/env/api/runtime_legacy", owned.LabelRevision: "legacy-deployment/service:api", owned.LabelSettings: string(cfg), owned.LabelReplicas: "1"}
	iid := id.New(id.PrefixInstance)
	identity := kube.ManagedRuntimeIdentity{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: p.ID, Connection: key, Instance: iid.String(), RuntimeRef: labels[owned.LabelRuntime], RuntimeRevision: labels[owned.LabelRevision], Namespace: at.Namespace, NamespaceUID: at.NamespaceUID, ClusterCA: at.TrustFingerprint}
	profile, profileErr := store.ManagedProfileFor(ctx, identity)
	if profileErr != nil || profile != kube.ManagedProfileLegacy {
		t.Fatal("legacy stored profile", profile, profileErr)
	}
	if checkErr := store.CheckLegacyManagedWrite(ctx, identity); checkErr != nil {
		t.Fatal("legacy write eligibility", checkErr)
	}
	spec := cpprovider.ServiceSpec{Name: "main", Image: "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a", Resources: cpprovider.ResourceSpec{CPUMillis: 100, MemoryMB: 128}}
	if _, err = runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal("legacy apply", err)
	}
	deps := e.bootstrap.Kube.AppsV1().Deployments(at.Namespace)
	for _, replicas := range []int{0, 1, 2} {
		if replicas == 2 {
			err = runtime.Scale(ctx, iid, labels, cpprovider.ResourceSpec{Replicas: replicas})
		} else {
			err = runtime.SetRunning(ctx, iid, labels, replicas != 0)
		}
		if err != nil {
			t.Fatal("legacy replicas", replicas, err)
		}
		current, readErr := deps.Get(ctx, kube.WorkloadName(iid), metav1.GetOptions{})
		if readErr != nil || int(*current.Spec.Replicas) != replicas {
			t.Fatal("stored replica count", replicas, readErr)
		}
	}
	receipt, err := store.GetController(ctx, "org", key, iid.String(), labels[owned.LabelRuntime])
	if err != nil || receipt.ReleaseID != "" {
		t.Fatal("legacy receipt", err)
	}
	if err = runtime.Remove(ctx, iid, labels); err != nil {
		t.Fatal("legacy Remove", err)
	}
	if err = wait.PollUntilContextTimeout(ctx, 150*time.Millisecond, time.Minute, true, func(c context.Context) (bool, error) {
		_, e := deps.Get(c, kube.WorkloadName(iid), metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return true, nil
		}
		return false, e
	}); err != nil {
		t.Fatal("legacy removal absent", err)
	}
	var effects int
	if err = e.sql.QueryRow(ctx, `SELECT (SELECT count(*) FROM loomplane_managed_workload_issuances)+(SELECT count(*) FROM loomplane_cluster_workload_holds)`).Scan(&effects); err != nil || effects != 0 {
		t.Fatal("legacy acquired managed capacity", effects, err)
	}
}
