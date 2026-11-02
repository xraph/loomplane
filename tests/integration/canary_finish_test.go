//go:build integration

package integration

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// A failed finish settles its canary and marks the ramping step inside the
// fenced finalization, so replaying it, as a worker that restarted before
// acknowledging does, settles nothing twice: not the settled canary, and
// not the canary a retry reopened and ramps again.
func TestAReplayedFinishDoesntSettleTheCanaryTwice(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','One','one','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env','org','proj','production','production',now(),now())`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`,
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('plan','org','proj','env','production','approved','manifest',1,'usr',now())`,
	} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	store := deployments.NewPgStore(db)
	ramping := func(candidate string) *deployments.Canary {
		return &deployments.Canary{
			Status: deployments.CanaryRamping, Steps: []int{5, 25, 50, 100}, HoldSeconds: 600, Thresholds: releasehealth.DefaultThresholds(),
			Step: 0, Weight: 5, UpdatedAt: time.Now().UTC(),
			Services: []deployments.CanaryService{{ActionID: "service:api", Name: "api", Provider: "cloud-run", BindingID: "bind_api",
				Baseline: "api-00001", Candidate: candidate, Image: "img:new", Refs: map[string]string{"service": "api"}}},
		}
	}
	if _, err := store.CreateDeployment(ctx, &deployments.Deployment{
		ID: "dep", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: "plan",
		Status: deployments.StatusRunning, StartedBy: "usr", StartedAt: time.Now(), Canary: ramping("api-00002"),
		Steps: []deployments.Step{
			{ActionID: "service:api", Status: deployments.StepRamping},
			{ActionID: "service:web", Status: deployments.StepPending},
		},
	}); err != nil {
		t.Fatal(err)
	}
	activity := func() drt.ActivityFunc {
		runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(db), Plans: &finishBookkeepingPlans{}})
		workflows := orchestration.NewDeployments()
		workflows.Bind(nil, runner, nil)
		return workflows.CanaryOptions("test", 1).Activities[orchestration.ActivityDeployFinish]
	}
	invoke := func(info drt.ActivityInfo, reason string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"org_id": "org", "deployment_id": "dep", "reason": reason})
		if _, err := activity()(ctx, info, raw); err != nil {
			t.Fatal(err)
		}
	}
	read := func() *deployments.Deployment {
		t.Helper()
		d, err := store.GetDeployment(ctx, "org", "dep")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	old := drt.ActivityInfo{Key: durable.Key{Namespace: "loomplane", WorkflowID: "plan", RunID: "run"}, CommandID: "finish", Attempt: 1}
	invoke(old, "The canary failed at 5%: api: too slow")
	d := read()
	if d.Status != deployments.StatusFailed || d.Canary.Status != deployments.CanaryRevertFailed || d.Canary.Reason != "The canary failed at 5%: api: too slow" {
		t.Fatalf("first finish: status %s, canary %s %q", d.Status, d.Canary.Status, d.Canary.Reason)
	}
	if api, web := d.Step("service:api"), d.Step("service:web"); api.Status != deployments.StepFailed || api.FinishedAt == nil || web.Status != deployments.StepSkipped {
		t.Fatalf("first finish steps: api %s, web %s", api.Status, web.Status)
	}
	settledAt := d.Canary.UpdatedAt

	// The candidate goes, and the canary settles as rolled back. Replaying
	// the finish leaves that alone.
	d.Canary.Status, d.Canary.Reason, d.Canary.Services[0].Candidate = deployments.CanaryRolledBack, "reverted afterwards", ""
	if err := store.SetCanary(ctx, "org", "dep", d.Canary); err != nil {
		t.Fatal(err)
	}
	old.Attempt++
	invoke(old, "The canary failed at 5%: api: too slow")
	if c := read().Canary; c.Status != deployments.CanaryRolledBack || c.Reason != "reverted afterwards" {
		t.Fatalf("a replayed finish settled the canary again: %s %q", c.Status, c.Reason)
	}

	// A retry reopens the deployment, and its canary ramps a new candidate.
	if ok, err := store.Reopen(ctx, "org", "dep"); err != nil || !ok {
		t.Fatal("reopen", ok, err)
	}
	if err := store.SetCanary(ctx, "org", "dep", ramping("api-00003")); err != nil {
		t.Fatal(err)
	}
	step := *read().Step("service:api")
	step.Status = deployments.StepRamping
	if err := store.UpdateStep(ctx, "org", "dep", step); err != nil {
		t.Fatal(err)
	}
	// The earlier attempt's finish, replayed at once by several workers,
	// settles none of it.
	old.Attempt++
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { invoke(old, "The canary failed at 5%: api: too slow") })
	}
	group.Wait()
	d = read()
	if d.Status != deployments.StatusRunning || d.Retries != 1 || d.Canary.Status != deployments.CanaryRamping || d.Canary.Services[0].Candidate != "api-00003" || d.Canary.Reason != "" {
		t.Fatalf("an old finish settled the retry: status %s, retries %d, canary %+v", d.Status, d.Retries, d.Canary)
	}
	if api, web := d.Step("service:api"), d.Step("service:web"); api.Status != deployments.StepRamping || web.Status != deployments.StepPending {
		t.Fatalf("an old finish marked the retry's steps: api %s, web %s", api.Status, web.Status)
	}

	// The retry's own finish settles its canary, once.
	next := old
	next.Key.WorkflowID = "dep.retry.1"
	invoke(next, "The canary failed at 25%: api: errors")
	d = read()
	if d.Status != deployments.StatusFailed || d.Canary.Status != deployments.CanaryRevertFailed || d.Canary.Reason != "The canary failed at 25%: api: errors" || !d.Canary.UpdatedAt.After(settledAt) {
		t.Fatalf("the retry's finish: status %s, canary %s %q", d.Status, d.Canary.Status, d.Canary.Reason)
	}
	if d.Canary.Services[0].Refs["service"] != "api" {
		t.Fatalf("settling lost the canary's refs: %+v", d.Canary.Services[0])
	}
	var receipts int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM loomplane_deployment_finishes WHERE deployment_id='dep'`).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatal("missing or duplicate receipts", receipts, err)
	}
}
