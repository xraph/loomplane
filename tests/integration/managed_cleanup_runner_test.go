//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/app"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cppg "github.com/xraph/ctrlplane/store/postgres"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/internal/ops"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestManagedOrdinaryCleanupRunner(t *testing.T) {
	for _, mode := range []string{"ordinary", "rebound", "restart", "consumer"} {
		t.Run(mode, func(t *testing.T) { managedCleanupEffectsRealAPI(t, false, mode) })
	}
}

func TestManagedAbandonedCleanupRunner(t *testing.T) {
	for _, mode := range []string{"failed", "cancelled"} {
		t.Run(mode, func(t *testing.T) { managedCleanupEffectsRealAPI(t, false, mode) })
	}
}

type pausedManagedDelete struct {
	instance.Service
	afterDelete error
	entered     chan struct{}
	resume      chan struct{}
	once        sync.Once
}

func (p *pausedManagedDelete) Delete(ctx context.Context, iid id.ID) error {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.resume:
	}
	if err := p.Service.Delete(ctx, iid); err != nil {
		return err
	}
	return p.afterDelete
}

func runReviewedCleanup(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, creds *connections.Service, store *planning.ManagedCleanupStore, scope kube.ManagedScope, sel planning.ManagedCleanupSelection, mode string) {
	t.Helper()
	rebound := mode == "rebound"
	abandoned := mode == "failed" || mode == "cancelled"
	db, err := grove.Open(e.pg)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := app.New(app.WithStore(cppg.New(db)))
	if err != nil {
		t.Fatal(err)
	}
	pause := &pausedManagedDelete{Service: cp.Instances, entered: make(chan struct{}), resume: make(chan struct{})}
	secrets := syncSecrets{scope.RuntimeRef: []byte("fixture")}
	target := &deployments.OwnedTarget{Cloud: connections.Kubernetes, Ctrl: deployments.CtrlPlane{Providers: cp.Providers(), Instances: pause, Deploys: cp.Deploys}, Credentials: creds, Secrets: secrets, Controllers: kube.NewControllerStore(e.pg).WithClusterCapacity(!abandoned)}
	routes := cloudinfra.NewService(cloudinfra.Deps{Store: e.store})
	engine := &deployments.ManagedCleanupExecution{Store: store, Credentials: creds, Target: target, Routes: routes}
	plans := planning.NewPgStore(e.pg)
	runner := deployments.NewRunner(deployments.RunnerDeps{Authz: managedCleanupReviewer(t), ManagedCleanup: engine, Store: deployments.NewPgStore(e.pg), Plans: plans})
	labels, err := json.Marshal(map[string]string{owned.LabelOrg: scope.Org, owned.LabelProject: scope.Project, owned.LabelEnvironment: scope.Environment, owned.LabelService: scope.Service, owned.LabelPlan: scope.Plan, owned.LabelConnection: scope.Connection, owned.LabelRuntime: scope.RuntimeRef, owned.LabelRevision: scope.RuntimeRevision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.sql.Exec(ctx, `INSERT INTO cp_instances(id,tenant_id,slug,name,state,provider_name,kind,services,labels) VALUES($1,$2,'old','old','running',$3,'deployment','[]',$4)`, scope.Instance, scope.Org, "kubernetes."+scope.Connection, labels); err != nil {
		t.Fatal(err)
	}
	if !abandoned {
		if _, saveErr := orchestration.NewPgRunStore(e.pg).Save(ctx, orchestration.DeploymentRun{OrgID: sel.Org, PlanID: sel.Plan, Request: durable.StartRequest{Key: durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: sel.Plan, RunID: "run"}, RequestID: "start:" + sel.Plan, WorkflowType: orchestration.WorkflowDeployment, BuildID: orchestration.ManagedCleanupBuild, Queue: orchestration.DurableQueue, Input: []byte("{}")}}); saveErr != nil {
			t.Fatal(saveErr)
		}
		if _, legacyErr := e.sql.Exec(ctx, `DELETE FROM cp_instances WHERE tenant_id=$1 AND id=$2`, scope.Org, scope.Instance); legacyErr == nil {
			t.Fatal("old worker removed instance metadata before reviewed effects")
		}
	}
	if abandoned {
		close(pause.resume)
		if _, err = e.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='succeeded',finished_at=now() WHERE id=$1`, sel.Deployment); err != nil {
			t.Fatal(err)
		}
		b, readErr := plans.GetBinding(ctx, scope.Org, "closure-binding")
		if readErr != nil {
			t.Fatal(readErr)
		}
		b.ExternalID, b.PlanID, b.Revision = "fresh", sel.Plan, "fresh"
		if err = plans.PutBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
		actor := security.Principal{OrgID: scope.Org, UserID: "reviewer"}
		if mode == "cancelled" {
			if _, err = e.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='running',finished_at=NULL WHERE id=$1`, scope.Deployment); err != nil {
				t.Fatal(err)
			}
			ds := deployments.NewPgStore(e.pg)
			paused, pauseErr := ds.SetCapacityWait(ctx, scope.Org, scope.Deployment, 0, []cloudinfra.CapacityBlock{{Reason: "fixture_pause_after_effect"}}, time.Now())
			if pauseErr != nil {
				t.Fatal(pauseErr)
			}
			service := deployments.NewService(ds, managedCleanupReviewer(t), nil, nil)
			cancelled, cancelErr := service.CancelCapacityWait(ctx, actor, scope.Deployment, paused.CapacityRevision)
			if cancelErr != nil || cancelled.Status != deployments.StatusCancelled {
				t.Fatal("post-effect cancellation", cancelErr)
			}
			var held, removed bool
			if checkErr := e.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cluster_workload_holds WHERE org_id=$1 AND plan_id=$2 AND NOT released),EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND release_id=$3 AND request->>'Method'='DELETE')`, scope.Org, scope.Plan, scope.Release).Scan(&held, &removed); checkErr != nil || !held || removed {
				t.Fatal("cancellation changed reviewed workload", held, removed, checkErr)
			}
		} else if _, err = e.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='failed',finished_at=now() WHERE id=$1`, scope.Deployment); err != nil {
			t.Fatal(err)
		}
		if migrateErr := ops.Migrate(ctx, e.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); migrateErr != nil {
			t.Fatal(migrateErr)
		}
		workflows := orchestration.NewDeployments()
		workflows.BindRunStore(orchestration.NewPgRunStore(e.pg))
		failures := deployments.NewFailedCleanups(runner, deployments.NewPgStore(e.pg), workflows).WithManagedCleanup(store, engine)
		workflows.BindFailedCleanups(failures)
		worker, workerErr := drt.NewWorker(dispatchpg.New(db), workflows.ManagedCleanupOptions("cleanup-integration", 1))
		if workerErr != nil {
			t.Fatal(workerErr)
		}
		workflows.BindWorker(orchestration.ManagedCleanupBuild, worker)
		preview, reviewErr := store.PreviewFailedCleanup(ctx, actor, scope.Deployment)
		if reviewErr != nil {
			t.Fatal("failed preview", reviewErr)
		}
		if len(preview.Blockers) == 0 {
			t.Fatal("failed cleanup did not retain newer serving binding")
		}
		if _, denyErr := store.AcceptFailedCleanup(ctx, actor, scope.Deployment, preview.Digest); denyErr == nil {
			t.Fatal("accepted cleanup of a serving dependency")
		}
		if err = plans.DeleteBinding(ctx, scope.Org, b.ID); err != nil {
			t.Fatal(err)
		}
		b.ID, b.Name = "independent-binding", "other"
		if err = plans.PutBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
		preview, reviewErr = store.PreviewFailedCleanup(ctx, actor, scope.Deployment)
		if reviewErr != nil {
			t.Fatal(reviewErr)
		}
		review, reviewErr := failures.Apply(ctx, actor, scope.Deployment, preview.Digest)
		if reviewErr != nil {
			t.Fatal("failed approval", reviewErr, preview.Blockers)
		}
		if review.ReviewID == "" {
			t.Fatal("accepted cleanup has no immutable review")
		}
		pumpWorkflow(t, worker)
		saved, reviewErr := store.CurrentFailedCleanup(ctx, scope.Org, scope.Deployment)
		if reviewErr != nil || saved.Status != "succeeded" {
			t.Fatal(saved, reviewErr)
		}
		var oldRows int
		if err = e.sql.QueryRow(ctx, `SELECT count(*) FROM cp_instances WHERE tenant_id=$1 AND id=$2`, scope.Org, scope.Instance).Scan(&oldRows); err != nil || oldRows != 0 {
			t.Fatal("failed instance remains", oldRows, err)
		}
		current, readErr := plans.GetBinding(ctx, scope.Org, b.ID)
		if readErr != nil || current.ExternalID != "fresh" {
			t.Fatal("failed worker removed newer binding", readErr)
		}
		if _, ok := secrets[scope.RuntimeRef]; ok {
			t.Fatal("failed runtime ref remains")
		}
		return
	}
	if mode == "restart" {
		pause.afterDelete = errors.New("lost ctrlplane response")
	}
	step := []deployments.Step{{ActionID: sel.Action, Status: deployments.StepPending}}
	raw, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.sql.Exec(ctx, `UPDATE loomplane_deployments SET steps=$2 WHERE id=$1`, sel.Deployment, raw); err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(orchestration.ManagedCleanupContext(ctx), 90*time.Second)
	defer cancel()
	if mode == "consumer" {
		b, readErr := plans.GetBinding(ctx, scope.Org, "closure-binding")
		if readErr != nil {
			t.Fatal(readErr)
		}
		b.Config = json.RawMessage(`{"env":[{"subject":"resource","from":"db","field":"consumer:url"}]}`)
		if err = plans.PutBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
		runErr := runner.Run(call, sel.Org, sel.Deployment, sel.Action, nil, func([]byte) error { return nil })
		paused, readErr := deployments.NewPgStore(e.pg).GetDeployment(ctx, sel.Org, sel.Deployment)
		if !errors.Is(runErr, deployments.ErrCapacityPaused) || readErr != nil || paused.CapacityWait == nil || len(paused.CapacityWait.Blocks) != 1 || paused.CapacityWait.Blocks[0].Reason != "managed_consumer_cleanup_unqualified" {
			t.Fatal("unsupported credential cleanup", runErr, readErr)
		}
		var unchanged bool
		if queryErr := e.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cp_instances WHERE tenant_id=$1 AND id=$2) AND EXISTS(SELECT 1 FROM loomplane_bindings WHERE org_id=$1 AND id=$3) AND EXISTS(SELECT 1 FROM loomplane_cluster_workload_holds WHERE org_id=$1 AND plan_id=$4 AND NOT released) AND NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND release_id=$5 AND request->>'Method'='DELETE')`, scope.Org, scope.Instance, b.ID, scope.Plan, scope.Release).Scan(&unchanged); queryErr != nil || !unchanged {
			t.Fatal("preflight lost reviewed source", unchanged, queryErr)
		}
		return
	}
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(call, sel.Org, sel.Deployment, sel.Action, nil, func([]byte) error { return nil })
	}()
	select {
	case <-pause.entered:
	case runErr := <-done:
		t.Fatal("runner failed before ctrlplane removal", runErr)
	case <-call.Done():
		t.Fatal(call.Err())
	}
	if rebound {
		b, readErr := plans.GetBinding(ctx, scope.Org, "closure-binding")
		if readErr != nil {
			t.Fatal(readErr)
		}
		b.ExternalID, b.Revision = "fresh", "fresh"
		b.Config = json.RawMessage(`{"env":[{"subject":"resource","from":"db","field":"consumer:url"}]}`)
		secrets["org/env/api/runtime_fresh"] = []byte("fresh credential")
		if _, err = e.sql.Exec(ctx, `INSERT INTO cp_instances(id,tenant_id,slug,name,state,provider_name) VALUES('fresh','org','fresh','fresh','running','kubernetes.fresh')`); err != nil {
			t.Fatal(err)
		}
		if err = plans.PutBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if bareErr := plans.DeleteBinding(ctx, scope.Org, "closure-binding"); bareErr == nil {
		t.Fatal("the old unqualified worker removed the serving binding")
	}
	close(pause.resume)
	err = <-done
	if mode == "restart" {
		if !errors.Is(err, pause.afterDelete) {
			t.Fatal("missing lost response", err)
		}
		pause.afterDelete = nil
		runner = deployments.NewRunner(deployments.RunnerDeps{ManagedCleanup: engine, Store: deployments.NewPgStore(e.pg), Plans: plans})
		err = runner.Run(call, sel.Org, sel.Deployment, sel.Action, nil, func([]byte) error { return nil })
	}
	if rebound && !errors.Is(err, planning.ErrManagedCleanupBindingChanged) || !rebound && err != nil {
		t.Fatal("runner outcome", err)
	}
	var count int
	if err = e.sql.QueryRow(ctx, `SELECT count(*) FROM cp_instances WHERE tenant_id=$1 AND id=$2`, scope.Org, scope.Instance).Scan(&count); err != nil || count != 0 {
		t.Fatal("old ctrlplane row remains", count, err)
	}
	if _, ok := secrets[scope.RuntimeRef]; ok {
		t.Fatal("old runtime ref retained")
	}
	b, readErr := plans.GetBinding(ctx, scope.Org, "closure-binding")
	if rebound {
		if readErr != nil || b.ExternalID != "fresh" {
			t.Fatal("rebound service removed", readErr)
		}
		if string(secrets["org/env/api/runtime_fresh"]) != "fresh credential" {
			t.Fatal("rebound credential removed")
		}
	} else if !errors.Is(readErr, planning.ErrNotFound) {
		t.Fatal("ordinary binding retained", b, readErr)
	}
}

func TestManagedCleanupMixedDeleteReview(t *testing.T) {
	f, svc, plan, result, scope := cleanupTargetFixture(t)
	ctx := t.Context()
	old, err := f.plans.GetBinding(ctx, scope.Org, "cleanup-binding")
	if err != nil {
		t.Fatal(err)
	}
	current := *old
	current.Provider, current.ConnectionID, current.ExternalID, current.PlanID = "server", "server-connection", "current-server", "server-plan"
	current.Config = json.RawMessage(`{"public":false}`)
	current.Replaced = []planning.Binding{*old}
	if err = f.plans.PutBinding(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if err = svc.PrepareManagedCleanupTargets(ctx, plan, result); err != nil {
		t.Fatal(err)
	}
	targets := result.Actions[0].CleanupTargets
	if len(targets) != 1 || targets[0].Scope != scope || targets[0].Origin != "retained" {
		t.Fatal("mixed Delete did not isolate the retained release")
	}
	parent := targets[0].MixedCurrentBinding
	if parent == nil || parent.ConfigHash == "" || parent.ExternalID != current.ExternalID || parent.ID != current.ID || parent.Provider != current.Provider {
		t.Fatal("mixed Delete is missing the current parent")
	}
	saveCleanupReview(t, f, plan, result)
	current.Config = json.RawMessage(`{"public":true}`)
	if err = f.plans.PutBinding(ctx, &current); err != nil {
		t.Fatal(err)
	}
	actor := security.WithPrincipal(ctx, security.Principal{OrgID: scope.Org, UserID: "reviewer"})
	if ok, decideErr := f.plans.Decide(actor, scope.Org, plan.ID, "approved", "reviewer", "", time.Now()); decideErr == nil || ok {
		t.Fatal("a changed parent was approved")
	}
	current.Replaced = nil
	if err = f.plans.PutBinding(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if err = svc.PrepareManagedCleanupTargets(ctx, plan, result); err != nil || len(result.Actions[0].CleanupTargets) != 0 {
		t.Fatal("a static binding requires no managed cleanup targets", err)
	}
}
