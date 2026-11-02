//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/store/memory"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/internal/ops"

	"github.com/xraph/loomplane/database/migrations"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

func TestManagedCleanupHistoricalCutover(t *testing.T) {
	f, _, plan, result, scope := cleanupTargetFixture(t)
	ctx := t.Context()
	steps := migrations.Group().Migrations()
	move := func(name string, up bool) {
		t.Helper()
		for _, m := range steps {
			if m.Name == name {
				var err error
				if up {
					err = m.Up(ctx, pgmigrate.New(f.db))
				} else {
					err = m.Down(ctx, pgmigrate.New(f.db))
				}
				if err != nil {
					t.Fatal(name, err)
				}
				return
			}
		}
		t.Fatal("missing migration", name)
	}
	move("managed_cleanup_cutover", false)
	move("managed_cleanup_execution_protocol", false)
	result.RequiredRuntimeProtocol = orchestration.LegacyCleanupProtocol
	saveCleanupReview(t, f, plan, result)
	p := security.Principal{OrgID: "org", UserID: "reviewer"}
	if ok, err := f.plans.Decide(security.WithPrincipal(ctx, p), "org", plan.ID, "approved", "reviewer", "", time.Now()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	app := kube.NewControllerStore(f.db).WithClusterCapacity(true).WithManagedDemandGuard(managedFixtureDemandGuard(f.db))
	id, err := app.Issue(ctx, scope, kube.ManagedRequest{Method: "PUT", Version: "v1", Resource: "services", Name: "old", Namespace: scope.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	if _, finishErr := f.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='succeeded',finished_at=now() WHERE id=$1`, scope.Deployment); finishErr != nil {
		t.Fatal(finishErr)
	}
	if _, insertErr := f.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('historic','org','proj','env','production',$1,'old','running','reviewer',now())`, plan.ID); insertErr != nil {
		t.Fatal(insertErr)
	}
	raw, _ := json.Marshal(orchestration.DeploymentInput{OrgID: "org", PlanID: plan.ID, Window: time.Minute})
	req := durable.StartRequest{Key: durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: plan.ID, RunID: "run"}, RequestID: "start:" + plan.ID, WorkflowType: orchestration.WorkflowDeployment, BuildID: orchestration.LegacyCleanupBuild, Queue: orchestration.DurableQueue, Input: raw}
	runs := orchestration.NewPgRunStore(f.db)
	if _, saveErr := runs.Save(ctx, orchestration.DeploymentRun{OrgID: "org", PlanID: plan.ID, Request: req}); saveErr != nil {
		t.Fatal(saveErr)
	}
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	if err = ops.Migrate(ctx, f.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	st := dispatchpg.New(db)
	dispatcher, err := dispatch.New(dispatch.WithStore(st))
	if err != nil {
		t.Fatal(err)
	}
	d := orchestration.NewDeployments()
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(d.Options("historical-main", 1)))
	if err != nil {
		t.Fatal(err)
	}
	d.Bind(workflowPlanner{}, nil, func() *engine.Engine { return eng })
	d.BindRunStore(runs)
	opts := d.LegacyCleanupOptions("compatible-replay", 1)
	w, err := drt.NewWorker(st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.StartExecution(ctx, req); err != nil {
		t.Fatal(err)
	}
	move("managed_cleanup_execution_protocol", true)
	move("managed_cleanup_cutover", true)
	var status string
	var blocked, other bool
	if err = f.sql.QueryRow(ctx, `SELECT status,loomplane_legacy_cleanup_blocked('org','proj','env','api',''),loomplane_legacy_cleanup_blocked('org','proj','env','other','') FROM loomplane_deployments WHERE id='historic'`).Scan(&status, &blocked, &other); err != nil || status != "paused" || !blocked || other {
		t.Fatal(status, blocked, other, err)
	}
	if err = f.plans.DeleteBinding(ctx, "org", "cleanup-binding"); err == nil || !strings.Contains(err.Error(), "exact reviewed binding") {
		t.Fatal("legacy bare removal", err)
	}

	if err = d.ReconcileHistoricalCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 8; n++ {
		if _, err = w.RunOnce(ctx, durable.TaskWorkflow); err != nil {
			t.Fatal(err)
		}
		ex, readErr := st.GetExecution(ctx, req.Key)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if ex.State != durable.StateRunning {
			break
		}
	}
	if err = d.ReconcileHistoricalCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.sql.QueryRow(ctx, `SELECT state FROM loomplane_legacy_cleanup_runs WHERE org_id='org' AND plan_id=$1`, plan.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatal("cancel ack cleared unknown own write", status, err)
	}
	if err = app.Resolve(ctx, id, scope, kube.ManagedOutcome{State: "rejected", StatusCode: 409}); err != nil {
		t.Fatal("historical Resolve", err)
	}
	if err = d.ReconcileHistoricalCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.sql.QueryRow(ctx, `SELECT state FROM loomplane_legacy_cleanup_runs WHERE org_id='org' AND plan_id=$1`, plan.ID).Scan(&status); err != nil || status != "drained" {
		t.Fatal(status, err)
	}
	got, err := runs.ForPlan(ctx, plan.ID)
	if err != nil || got.Request.BuildID != orchestration.LegacyCleanupBuild || string(got.Request.Input) != string(req.Input) {
		t.Fatal("rewritten historical route", err)
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_deployments SET status='running' WHERE id='historic'`); err == nil {
		t.Fatal("reactivated old approval")
	}
}

func TestManagedCleanupCandidateRouting(t *testing.T) {
	f, _, _, draft, _ := cleanupTargetFixture(t)
	ctx := t.Context()
	b, err := f.plans.GetBinding(ctx, "org", "cleanup-binding")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.plans.DeleteBinding(ctx, "org", b.ID); err != nil {
		t.Fatal(err)
	}
	engStore := memory.New()
	dispatcher, err := dispatch.New(dispatch.WithStore(engStore))
	if err != nil {
		t.Fatal(err)
	}
	d := orchestration.NewDeployments()
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(d.Options("default", 1)))
	if err != nil {
		t.Fatal(err)
	}
	d.Bind(workflowPlanner{}, nil, func() *engine.Engine { return eng })
	runs := orchestration.NewPgRunStore(f.db)
	d.BindRunStore(runs)
	old, err := drt.NewWorker(engStore, d.LegacyCleanupOptions("old", 1))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := drt.NewWorker(engStore, d.ManagedCleanupOptions("new", 1))
	if err != nil {
		t.Fatal(err)
	}
	d.BindWorker(orchestration.LegacyCleanupBuild, old)
	d.BindWorker(orchestration.ManagedCleanupBuild, newer)
	start := func(planID, want string) {
		t.Helper()
		if _, e := f.sql.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES($1,'org','proj','env','production','planning','pool-manifest-proj',1,'usr',now())`, planID); e != nil {
			t.Fatal(e)
		}
		if e := d.Start(ctx, "org", planID, time.Minute); e != nil {
			t.Fatal(e)
		}
		got, e := runs.ForPlan(ctx, planID)
		if e != nil || got.Request.BuildID != want {
			t.Fatalf("route %s: %s %v", planID, got.Request.BuildID, e)
		}
	}
	start("before-evidence", orchestration.LegacyCleanupBuild)
	if err = f.plans.PutBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err = d.Start(ctx, "org", "before-evidence", time.Minute); err != nil {
		t.Fatal("durable retry", err)
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_plans SET result=$2 WHERE id=$1`, "before-evidence", raw); err == nil || !strings.Contains(err.Error(), "compatible workflow") {
		t.Fatal("late managed evidence used the older route", err)
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_plans SET status='failed' WHERE id='before-evidence'`); err != nil {
		t.Fatal(err)
	}
	start("potential-cleanup", orchestration.ManagedCleanupBuild)
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_plans SET result='{"actions":[{"id":"service:api","subject":"service","name":"api","op":"keep"}]}' WHERE id='potential-cleanup'`); err != nil {
		t.Fatal(err)
	}
	var intents int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_managed_cleanup_intents WHERE plan_id='potential-cleanup'`).Scan(&intents); err != nil || intents != 0 {
		t.Fatal("route capability granted cleanup", intents, err)
	}
}
