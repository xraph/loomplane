//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

func capacityDeployment(t *testing.T, st *deployments.PgStore, plan *planning.Plan) *deployments.Deployment {
	t.Helper()
	d, err := st.CreateDeployment(t.Context(), &deployments.Deployment{ID: "deployment-" + plan.ID, OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Environment: "production", PlanID: plan.ID, Status: deployments.StatusRunning, StartedAt: time.Now(), StartedBy: "usr", Retries: 2, Steps: []deployments.Step{{ActionID: "done", Status: deployments.StepSucceeded, Checkpoint: []byte("receipt")}, {ActionID: "pending", Status: deployments.StepPending}}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestDeploymentCapacityWaitPostgres(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "wait-plan", "env")
	acceptFuture(t, f, plan.ID)
	st := deployments.NewPgStore(f.db)
	d := capacityDeployment(t, st, plan)
	blocks := []cloudinfra.CapacityBlock{{Reason: "hard_limit", Resource: "cpu"}, {Reason: "provisioning", Resource: "memory", Retryable: true}}
	d, err := st.SetCapacityWait(t.Context(), "org", d.ID, 0, blocks, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != deployments.StatusPaused || d.FinishedAt != nil {
		t.Fatalf("bad wait: %+v", d)
	}
	// SQL CHECK must reject missing JSON fields, not accept a NULL result.
	if _, e := f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET capacity_wait='{}' WHERE org_id='org' AND id=$1`, d.ID); e == nil {
		t.Fatal("malformed wait passed the database constraint")
	}
	if err = st.UpdateStep(t.Context(), "org", d.ID, deployments.Step{ActionID: "pending", Status: deployments.StepSkipped}); err != nil {
		t.Fatal(err)
	}
	guarded, err := st.GetDeployment(t.Context(), "org", d.ID)
	if err != nil || guarded.Step("pending").Status != deployments.StepPending {
		t.Fatalf("late finish skipped paused work: %+v %v", guarded, err)
	}
	restarted := deployments.NewPgStore(f.db)
	d, err = restarted.SetCapacityWait(t.Context(), "org", d.ID, 1, []cloudinfra.CapacityBlock{blocks[1], blocks[0]}, time.Now())
	if err != nil || d.CapacityWait.Revision != 1 {
		t.Fatalf("repeat: %+v %v", d, err)
	}
	if _, err = st.SetCapacityWait(t.Context(), "other", d.ID, 1, blocks, time.Now()); !errors.Is(err, deployments.ErrNotFound) {
		t.Fatalf("org scope: %v", err)
	}
	blocks[0].Limit = 2
	d, err = st.SetCapacityWait(t.Context(), "org", d.ID, 1, blocks, time.Now())
	if err != nil || d.CapacityWait.Revision != 2 {
		t.Fatalf("change: %+v %v", d, err)
	}
	// A failed outbox insert must roll back the state transition too.
	if _, err = f.sql.Exec(t.Context(), `ALTER TABLE loomplane_deployment_capacity_events ADD CONSTRAINT fail_delivery CHECK(body->>'status'<>'running')`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.TransitionCapacityWait(t.Context(), "org", d.ID, 2, deployments.StatusRunning, time.Now()); err == nil {
		t.Fatal("expected event insert failure")
	}
	still, err := st.GetDeployment(t.Context(), "org", d.ID)
	if err != nil || still.Status != deployments.StatusPaused || still.CapacityWait.Revision != 2 {
		t.Fatalf("non-atomic transition: %+v %v", still, err)
	}
	if _, err = f.sql.Exec(t.Context(), `ALTER TABLE loomplane_deployment_capacity_events DROP CONSTRAINT fail_delivery`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, status := range []string{deployments.StatusRunning, deployments.StatusCancelled} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := st.TransitionCapacityWait(t.Context(), "org", d.ID, 2, status, time.Now())
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		} else if !errors.Is(e, deployments.ErrCapacityWaitConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatalf("race winners: %d", wins)
	}
	d, err = restarted.GetDeployment(t.Context(), "org", d.ID)
	if err != nil || d.Retries != 2 || d.Steps[0].Status != deployments.StepSucceeded || string(d.Steps[0].Checkpoint) != "receipt" {
		t.Fatalf("receipts: %+v %v", d, err)
	}
	if (d.Status == deployments.StatusCancelled) != (d.FinishedAt != nil) {
		t.Fatalf("finish timestamp: %+v", d)
	}
	events, err := restarted.ListCapacityTransitions(t.Context(), "org", deployments.CapacityConsumerWorkflow, 100)
	if err != nil || len(events) != 3 {
		t.Fatalf("outbox: %+v %v", events, err)
	}
	for _, e := range events {
		if err = st.AcknowledgeCapacityTransition(t.Context(), "org", e.DeploymentID, e.Revision, deployments.CapacityConsumerWorkflow); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := st.ListCapacityTransitions(t.Context(), "org", deployments.CapacityConsumerWorkflow, 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ack: %+v %v", pending, err)
	}
	pending, err = st.ListCapacityTransitions(t.Context(), "org", deployments.CapacityConsumerNotification, 100)
	if err != nil || len(pending) != 3 {
		t.Fatalf("notification event lost: %+v %v", pending, err)
	}
}

type heldCapacityCandidate struct {
	placement        *cloudinfra.Placement
	started, release chan struct{}
}

func (c *heldCapacityCandidate) EnsureCapacity(ctx context.Context, _ cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	close(c.started)
	select {
	case <-c.release:
		return &cloudinfra.CapacityDecision{Placement: c.placement}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestDeploymentCapacityWaitLateAdmission(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "late-plan", "env")
	acceptFuture(t, f, plan.ID)
	st := deployments.NewPgStore(f.db)
	d := capacityDeployment(t, st, plan)
	d, err := st.SetCapacityWait(t.Context(), "org", d.ID, 0, []cloudinfra.CapacityBlock{{Reason: "provisioning"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := f.request("env", "api")
	req.PlanID = plan.ID
	req.CPUMillis = 2000
	placement, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	candidate := &heldCapacityCandidate{placement: placement, started: make(chan struct{}), release: make(chan struct{})}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: candidate})
	result := make(chan error, 1)
	go func() { result <- svc.ResolveCapacityIntents(t.Context(), "org", plan.ID) }()
	<-candidate.started
	if _, err = st.TransitionCapacityWait(t.Context(), "org", d.ID, d.CapacityWait.Revision, deployments.StatusCancelled, time.Now()); err != nil {
		t.Fatal(err)
	}
	close(candidate.release)
	var admission *cloudinfra.CapacityAdmissionError
	if err = <-result; !errors.As(err, &admission) || admission.Block.Reason != "review_required" {
		t.Fatalf("late admission: %v", err)
	}
	pins, err := f.plans.ResolvedCapacityPlacements(t.Context(), "org", plan.ID)
	if err != nil || len(pins) != 0 {
		t.Fatalf("cancelled admitted: %+v %v", pins, err)
	}
}

type heldDemandAuthority struct {
	d                cloudinfra.CapacityDemand
	calls            int
	started, release chan struct{}
}

func (a *heldDemandAuthority) ResolveApprovedCapacityDemand(ctx context.Context, _, _, _ string) (*cloudinfra.CapacityDemand, error) {
	a.calls++
	if a.calls == 2 {
		close(a.started)
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &a.d, nil
}

func TestDeploymentCapacityWaitCancellationKeepsAcceptedOperation(t *testing.T) {
	f, _, demand := expansionFixture(t, false, 0)
	expansion := ensureExpansion(t, f, demand)
	reconcileExpansion(t, f)
	db := ledgerDB(t, f)
	st := deployments.NewPgStore(db)
	d := capacityDeployment(t, st, &planning.Plan{ID: demand.PlanID, OrgID: demand.OrgID, ProjectID: demand.ProjectID, EnvironmentID: demand.EnvironmentID})
	d, err := st.SetCapacityWait(t.Context(), "org", d.ID, 0, []cloudinfra.CapacityBlock{{Reason: "provisioning"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	authority := &heldDemandAuthority{d: demand, started: make(chan struct{}), release: make(chan struct{})}
	f.svc.CapacityDemandAuthority = authority
	result := make(chan error, 1)
	go func() { _, e := f.svc.EnsureCapacity(t.Context(), demand); result <- e }()
	<-authority.started
	if _, err = st.TransitionCapacityWait(t.Context(), "org", d.ID, d.CapacityWait.Revision, deployments.StatusCancelled, time.Now()); err != nil {
		t.Fatal(err)
	}
	close(authority.release)
	// This fixture authority deliberately keeps returning its old approval. The
	// attachment transaction must reject it after cancellation has committed.
	if err = <-result; err == nil {
		t.Fatal("late attachment accepted")
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_expansion_demands WHERE org_id='org' AND plan_id=$1`, demand.PlanID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reattached: %d %v", count, err)
	}
	reconcileExpansion(t, f)
	operation, err := f.store.GetOperation(t.Context(), "org", "cloud", expansion.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	host, err := f.store.GetServer(t.Context(), "org", "cloud", operation.ServerID)
	if err != nil || host.ResourceID != "42" {
		t.Fatalf("lost accepted host: %+v %v", host, err)
	}
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations WHERE status='reserved'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost ledger: %d %v", count, err)
	}
}

type blockingCapacityCandidate struct{ calls int }

func (c *blockingCapacityCandidate) EnsureCapacity(_ context.Context, d cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	c.calls++
	return &cloudinfra.CapacityDecision{Block: &cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: d.Service, CloudID: d.CloudID, PoolID: d.PoolID}}, nil
}
func TestDeploymentCapacityWaitAllPlanBlockers(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "multi-plan", "env")
	raw, err := json.Marshal(plan.Actions[0])
	if err != nil {
		t.Fatal(err)
	}
	var other planning.Action
	if err = json.Unmarshal(raw, &other); err != nil {
		t.Fatal(err)
	}
	other.ID = "service:worker"
	other.Name = "worker"
	other.Service.CapacityIntent.Demand.Service = "worker"
	other.Service.CapacityIntent.Demand.IntentHash = ""
	other.Service.CapacityIntent.Demand.IntentHash, err = planning.Digest(other.Service.CapacityIntent)
	if err != nil {
		t.Fatal(err)
	}
	plan.Actions = append(plan.Actions, other)
	raw, err = json.Marshal(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions}',$2::jsonb) WHERE id=$1`, plan.ID, raw); err != nil {
		t.Fatal(err)
	}
	acceptFuture(t, f, plan.ID)
	st := deployments.NewPgStore(f.db)
	d := capacityDeployment(t, st, plan)
	candidate := &blockingCapacityCandidate{}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: candidate})
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: st, Plans: f.plans, Capacity: svc})
	wait, err := runner.AssessCapacity(t.Context(), "org", d.ID)
	if err != nil || wait == nil || len(wait.Blocks) != 2 || candidate.calls != 2 {
		t.Fatalf("all blockers: %+v calls=%d %v", wait, candidate.calls, err)
	}
}

func TestDeploymentCapacityWaitRemainsActive(t *testing.T) {
	clouds, sql, db := cloudSyncDatabase(t)
	for _, statement := range []string{
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('wait-manifest','org','proj',1,'version: 1','wait','usr',now())`,
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('active-wait','org','proj','env','production','approved','wait-manifest',1,'usr',now()),('next-release','org','proj','env','production','ready','wait-manifest',1,'usr',now())`,
	} {
		if _, err := sql.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	st := deployments.NewPgStore(db)
	d := capacityDeployment(t, st, &planning.Plan{ID: "active-wait", OrgID: "org", ProjectID: "proj", EnvironmentID: "env"})
	d, err := st.SetCapacityWait(t.Context(), "org", d.ID, 0, []cloudinfra.CapacityBlock{{Reason: "hard_limit"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if available, e := st.EnvironmentAvailable(t.Context(), "org", "env", "next-release"); e != nil || available {
		t.Fatalf("paused released environment: %v %v", available, e)
	}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: st, Plans: planning.NewPgStore(db)})
	if available, e := runner.ReleaseAvailable(t.Context(), "org", "next-release"); e != nil || available {
		t.Fatalf("paused release available: %v %v", available, e)
	}
	next := &deployments.Deployment{ID: "next-deployment", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: "next-release", Status: deployments.StatusRunning, StartedAt: time.Now(), StartedBy: "usr"}
	if _, e := st.CreateDeployment(t.Context(), next); e == nil {
		t.Fatal("database accepted a release over paused work")
	}
	preview, snapshot, sync := persistAssignmentPreview(t, clouds, "wait-replacement", "cloud", 0)
	if _, e := clouds.Accept(t.Context(), preview, snapshot, sync, "wait-replacement"); !errors.Is(e, cloudprofiles.ErrBusy) {
		t.Fatalf("paused allowed replacement: %v", e)
	}
	if _, err = st.TransitionCapacityWait(t.Context(), "org", d.ID, 1, deployments.StatusCancelled, time.Now()); err != nil {
		t.Fatal(err)
	}
	if available, e := runner.ReleaseAvailable(t.Context(), "org", "next-release"); e != nil || !available {
		t.Fatalf("cancelled retained environment: %v %v", available, e)
	}
	if _, err = st.CreateDeployment(t.Context(), next); err != nil {
		t.Fatalf("cancelled blocked next deployment: %v", err)
	}
	if err = st.Finish(t.Context(), "org", next.ID, deployments.StatusSucceeded, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = clouds.Accept(t.Context(), preview, snapshot, sync, "wait-replacement"); err != nil {
		t.Fatalf("cancelled blocked replacement: %v", err)
	}
}
