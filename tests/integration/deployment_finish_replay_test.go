//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/xraph/loomplane/internal/planning"
)

func TestDeploymentFinishReplayCannotSettleReopenedAttempt(t *testing.T) {
	for _, capacity := range []bool{false, true} {
		name := "legacy"
		if capacity {
			name = "capacity"
		}
		t.Run(name, func(t *testing.T) { testDeploymentFinishReplay(t, capacity) })
	}
}

func testDeploymentFinishReplay(t *testing.T, capacity bool) {
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
	_, createErr := store.CreateDeployment(ctx, &deployments.Deployment{ID: "dep", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: "plan", Status: deployments.StatusRunning, StartedBy: "usr", StartedAt: time.Now(), Steps: []deployments.Step{{ActionID: "resource:database", Status: deployments.StepPending}}})
	if createErr != nil {
		t.Fatal(createErr)
	}
	plans := &finishBookkeepingPlans{}
	var selectedStore deployments.Store
	activity := func() drt.ActivityFunc {
		currentStore := selectedStore
		if currentStore == nil {
			currentStore = deployments.NewPgStore(db)
		}
		runner := deployments.NewRunner(deployments.RunnerDeps{Store: currentStore, Plans: plans})
		workflows := orchestration.NewDeployments()
		workflows.Bind(nil, runner, nil)
		if capacity {
			return workflows.CapacityOptions("test", 1).Activities[orchestration.ActivityDeployFinish]
		}
		return workflows.Options("test", 1).Activities[orchestration.ActivityDeployFinish]
	}
	old := drt.ActivityInfo{Key: durable.Key{Namespace: "loomplane", WorkflowID: "plan", RunID: "run"}, CommandID: "finish", Attempt: 1}
	invoke := func(fn drt.ActivityFunc, info drt.ActivityInfo, reason string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"org_id": "org", "deployment_id": "dep", "reason": reason})
		if _, err := fn(ctx, info, raw); err != nil {
			t.Fatal(err)
		}
	}

	// Refuse the receipt insert to prove status and skipped steps roll back together.
	for _, stmt := range []string{
		`CREATE FUNCTION reject_finish_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt unavailable'; END $$`,
		`CREATE TRIGGER reject_finish_receipt BEFORE INSERT ON loomplane_deployment_finishes FOR EACH ROW EXECUTE FUNCTION reject_finish_receipt()`,
	} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]string{"org_id": "org", "deployment_id": "dep", "reason": "original failure"})
	if _, err := activity()(ctx, old, raw); err == nil {
		t.Fatal("receipt failure was ignored")
	}
	before, beforeErr := store.GetDeployment(ctx, "org", "dep")
	if beforeErr != nil || before.Status != deployments.StatusRunning || before.Steps[0].Status != deployments.StepPending {
		t.Fatal("receipt failure left partial finalization", before, beforeErr)
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER reject_finish_receipt ON loomplane_deployment_finishes`); err != nil {
		t.Fatal(err)
	}
	invoke(activity(), old, "original failure")
	if ok, err := store.Reopen(ctx, "org", "dep"); err != nil || !ok {
		t.Fatal("reopen", ok, err)
	}
	// Recreate the runner and adapter, as after a restart before acknowledgement.
	old.Attempt++

	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() { _, err := activity()(ctx, old, raw); failures <- err })
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	d, readErr := store.GetDeployment(ctx, "org", "dep")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if d.Status != deployments.StatusRunning || d.Error != "" || d.Retries != 1 || d.Steps[0].Status != deployments.StepPending {
		t.Fatalf("old finish settled the new attempt: status=%s error=%s retries=%d step=%s", d.Status, d.Error, d.Retries, d.Steps[0].Status)
	}
	next := old
	next.Key.WorkflowID = "dep.retry.1"
	invoke(activity(), next, "new failure")
	d, readErr = store.GetDeployment(ctx, "org", "dep")
	if readErr != nil || d.Status != deployments.StatusFailed || d.Error != "new failure" {
		t.Fatal("new finish did not settle its own attempt", d, readErr)
	}
	if _, found, err := store.FinishedActivity(ctx, "other-org", "dep", old.IdempotencyKey()); err != nil || found {
		t.Fatal("finish receipt escaped organization scope", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM loomplane_deployment_finishes WHERE deployment_id='dep'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("missing or duplicate receipts", count, err)
	}
	if ok, err := store.Reopen(ctx, "org", "dep"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	next.Key.WorkflowID = "dep.retry.2"
	plans.fail = true
	success, _ := json.Marshal(map[string]string{"org_id": "org", "deployment_id": "dep"})
	if _, err := activity()(ctx, next, success); err == nil {
		t.Fatal("bookkeeping failure was swallowed")
	}
	settled, settleErr := store.GetDeployment(ctx, "org", "dep")
	if settleErr != nil || settled.Status != deployments.StatusSucceeded {
		t.Fatal("success was not durably settled", settled, settleErr)
	}
	invoke(activity(), next, "")
	if plans.calls != 2 || len(plans.records) != 1 || !plans.records[0].DeployedAt.Equal(*settled.FinishedAt) {
		t.Fatal("successful replay did not finish original bookkeeping", plans.calls, plans.records)
	}

	// A finish committed before receipt support still must not affect a new retry.
	if _, err := db.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('upgrade-plan','org','proj','env','production','approved','manifest',1,'usr',now())`); err != nil {
		t.Fatal(err)
	}
	_, upgradeErr := store.CreateDeployment(ctx, &deployments.Deployment{ID: "upgrade", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: "upgrade-plan", Status: deployments.StatusRunning, StartedBy: "usr", StartedAt: time.Now(), Steps: []deployments.Step{{ActionID: "resource:database", Status: deployments.StepPending}}})
	if upgradeErr != nil {
		t.Fatal(upgradeErr)
	}
	if err := store.Finish(ctx, "org", "upgrade", deployments.StatusFailed, "legacy failure", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Reopen(ctx, "org", "upgrade"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	legacy := old
	legacy.Key.WorkflowID = "upgrade-plan"
	body, _ := json.Marshal(map[string]string{"org_id": "org", "deployment_id": "upgrade", "reason": "legacy failure"})
	if _, err := activity()(ctx, legacy, body); err != nil {
		t.Fatal(err)
	}
	upgraded, upgradeReadErr := store.GetDeployment(ctx, "org", "upgrade")
	if upgradeReadErr != nil || upgraded.Status != deployments.StatusRunning || upgraded.Steps[0].Status != deployments.StepPending {
		t.Fatal("pre-migration finish settled new retry", upgraded, upgradeReadErr)
	}

	// Advance the generation after the runner reads it, before the store commits.
	selectedStore = &finishReopenStore{PgStore: store}
	legacy.Key.WorkflowID = "upgrade.retry.1"
	if _, err := activity()(ctx, legacy, body); err != nil {
		t.Fatal(err)
	}
	upgraded, upgradeReadErr = store.GetDeployment(ctx, "org", "upgrade")
	if upgradeReadErr != nil || upgraded.Retries != 2 || upgraded.Status != deployments.StatusRunning || upgraded.Steps[0].Status != deployments.StepPending {
		t.Fatal("finish crossed the atomic retry fence", upgraded, upgradeReadErr)
	}
	selectedStore = nil
	legacy.Key.WorkflowID = "upgrade.retry.3"
	if _, err := activity()(ctx, legacy, body); err == nil {
		t.Fatal("unadmitted future retry settled deployment")
	}
}

type finishBookkeepingPlans struct {
	*planning.PgStore
	fail    bool
	calls   int
	records []planning.DeployedSource
}

func (p *finishBookkeepingPlans) GetPlan(context.Context, string, string) (*planning.Plan, error) {
	return &planning.Plan{Sources: []planning.PlanSource{{SourceID: "source", InScope: true}}}, nil
}
func (p *finishBookkeepingPlans) RecordRelease(_ context.Context, _ string, release *planning.DeployedRelease) error {
	p.calls++
	if p.fail {
		p.fail = false
		return errors.New("bookkeeping unavailable")
	}
	for _, value := range release.Sources {
		p.records = append(p.records, *value)
	}
	return nil
}

type finishReopenStore struct {
	*deployments.PgStore
	once sync.Once
}

func (s *finishReopenStore) FinishedActivity(ctx context.Context, org, id, key string) (int, bool, error) {
	var failed error
	s.once.Do(func() {
		if failed = s.Finish(context.Background(), org, id, deployments.StatusFailed, "interleaved failure", time.Now()); failed != nil {
			return
		}
		_, failed = s.Reopen(ctx, org, id)
	})
	if failed != nil {
		return 0, false, failed
	}
	return s.PgStore.FinishedActivity(ctx, org, id, key)
}
