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
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
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

type capacityHeldPlans struct {
	*planning.PgStore
	entered, release chan struct{}
}

func (p *capacityHeldPlans) GetPlan(ctx context.Context, org, id string) (*planning.Plan, error) {
	plan, err := p.PgStore.GetPlan(ctx, org, id)
	close(p.entered)
	select {
	case <-p.release:
		return plan, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func cancelCapacityDeployment(t *testing.T, st *deployments.PgStore, id string) {
	t.Helper()
	d, err := st.GetDeployment(t.Context(), "org", id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != deployments.StatusPaused {
		d, err = st.SetCapacityWait(t.Context(), "org", id, d.CapacityRevision, []cloudinfra.CapacityBlock{{Reason: "hard_limit"}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.TransitionCapacityWait(t.Context(), "org", id, d.CapacityRevision, deployments.StatusCancelled, time.Now()); err != nil {
		t.Fatal(err)
	}
}
func TestDeploymentCapacityWaitStaleActivity(t *testing.T) {
	f := newPoolFixture(t)
	req := f.request("env", "api")
	pin, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "stale-activity", req, pin)
	f.accept(t, "stale-activity")
	executor := &poolExecutor{}
	id, _, err := poolTestRunner(f, executor).Start(t.Context(), "org", "stale-activity")
	if err != nil {
		t.Fatal(err)
	}
	held := &capacityHeldPlans{PgStore: f.plans, entered: make(chan struct{}), release: make(chan struct{})}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: held, CloudProfiles: f.clouds, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(t.Context(), "org", id, "service:api", nil, func([]byte) error { return nil })
	}()
	<-held.entered
	cancelCapacityDeployment(t, deployments.NewPgStore(f.db), id)
	close(held.release)
	err = <-done
	if err == nil || executor.calls != 0 {
		t.Fatalf("cancelled activity reached executor: calls=%d err=%v", executor.calls, err)
	}
}

type partialCapacityCandidate struct{ placement *cloudinfra.Placement }

func (c partialCapacityCandidate) EnsureCapacity(_ context.Context, d cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	if d.Service == "worker" {
		return &cloudinfra.CapacityDecision{Block: &cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "cpu"}}, nil
	}
	return &cloudinfra.CapacityDecision{Placement: c.placement}, nil
}
func TestDeploymentCapacityWaitPartialAdmission(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "partial", "env")
	raw, _ := json.Marshal(plan.Actions[0])
	var other planning.Action
	if err := json.Unmarshal(raw, &other); err != nil {
		t.Fatal(err)
	}
	other.ID, other.Name = "service:worker", "worker"
	other.Service.CapacityIntent.Demand.Service = "worker"
	other.Service.CapacityIntent.Demand.IntentHash = ""
	other.Service.CapacityIntent.Demand.IntentHash, _ = planning.Digest(other.Service.CapacityIntent)
	plan.Actions = append(plan.Actions, other)
	raw, _ = json.Marshal(plan.Actions)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions}',$2::jsonb) WHERE id=$1`, plan.ID, raw); err != nil {
		t.Fatal(err)
	}
	acceptFuture(t, f, plan.ID)
	req := f.request("env", "api")
	req.CPUMillis = 2000
	candidate, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: partialCapacityCandidate{candidate}})
	st := deployments.NewPgStore(f.db)
	d := capacityDeployment(t, st, plan)
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: st, Plans: f.plans, Capacity: svc})
	wait, err := runner.AssessCapacity(t.Context(), "org", d.ID)
	if err != nil || wait == nil || len(wait.Blocks) != 1 {
		t.Fatalf("partial wait: %+v %v", wait, err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations r JOIN loomplane_pool_execution x USING(org_id,plan_id,service) WHERE r.plan_id=$1 AND NOT x.started AND x.phase='reserved'`, plan.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ordinary admission receipt: %d %v", count, err)
	}
	cancelCapacityDeployment(t, st, d.ID)
	replacement := futurePlan(t, f, "replacement", "env")
	acceptFuture(t, f, replacement.ID)
	candidate, err = f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatalf("cancelled unused admission blocks replacement: %v", err)
	}
	svc = planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: capacityPlanCandidate{candidate}})
	if err = svc.ResolveCapacityIntents(t.Context(), "org", replacement.ID); err != nil {
		t.Fatalf("replacement admission: %v", err)
	}
}

func TestDeploymentCapacityWaitFirstEffectFence(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unstarted", true: "accepted"}[accepted], func(t *testing.T) {
			f := newPoolFixture(t)
			req := f.request("env", "api")
			pin, err := f.store.PreviewPlacement(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			f.plan(t, "effects", req, pin)
			f.accept(t, "effects")
			id, _, err := poolTestRunner(f, &poolExecutor{}).Start(t.Context(), "org", "effects")
			if err != nil {
				t.Fatal(err)
			}
			lock, err := f.store.BeginPoolExecution(t.Context(), "org", "effects", "api")
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			attempt, err := f.store.BeginWorkloadAttempt(lock.Context(t.Context()), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: "effects", Deployment: id}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if accepted {
				if err = f.store.MarkWorkloadEffects(t.Context(), *attempt); err != nil {
					t.Fatal(err)
				}
				// Workload acceptance precedes the physical pool effect marker. Retain
				// this uncertain interval even though pool.started is still false.
			}
			st := deployments.NewPgStore(f.db)
			if _, err = st.SetCapacityWait(t.Context(), "org", id, 0, []cloudinfra.CapacityBlock{{Reason: "hard_limit"}}, time.Now()); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var body string
				if e := f.sql.QueryRow(t.Context(), `SELECT jsonb_build_object('reservations',(SELECT jsonb_agg(to_jsonb(r)) FROM loomplane_capacity_reservations r WHERE plan_id='effects'),'execution',(SELECT jsonb_agg(to_jsonb(x)) FROM loomplane_pool_execution x WHERE plan_id='effects'),'attempts',(SELECT jsonb_agg(to_jsonb(w)) FROM loomplane_workload_attempts w WHERE plan_id='effects'))::text`).Scan(&body); e != nil {
					t.Fatal(e)
				}
				return body
			}
			before := snapshot()
			if _, err = st.TransitionCapacityWait(t.Context(), "org", id, 0, deployments.StatusCancelled, time.Now()); !errors.Is(err, deployments.ErrCapacityWaitConflict) {
				t.Fatalf("stale cancel: %v", err)
			}
			if after := snapshot(); before != after {
				t.Fatal("stale CAS committed fencing or released reservation")
			}
			if _, err = f.sql.Exec(t.Context(), `ALTER TABLE loomplane_deployment_capacity_events ADD CONSTRAINT reject_cancel CHECK(body->>'status'<>'cancelled')`); err != nil {
				t.Fatal(err)
			}
			if _, err = st.TransitionCapacityWait(t.Context(), "org", id, 1, deployments.StatusCancelled, time.Now()); err == nil {
				t.Fatal("expected cancellation event failure")
			}
			if after := snapshot(); before != after {
				t.Fatal("event failure committed fencing or released reservation")
			}
			if _, err = f.sql.Exec(t.Context(), `ALTER TABLE loomplane_deployment_capacity_events DROP CONSTRAINT reject_cancel`); err != nil {
				t.Fatal(err)
			}
			cancelCapacityDeployment(t, st, id)
			markErr := f.store.MarkWorkloadEffects(t.Context(), *attempt)
			poolErr := lock.Attempt(t.Context())
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations WHERE plan_id='effects'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if accepted {
				if count != 1 || markErr != nil || poolErr != nil {
					t.Fatalf("accepted evidence lost: count=%d mark=%v pool=%v", count, markErr, poolErr)
				}
			} else if count != 0 || markErr == nil || poolErr == nil {
				t.Fatalf("unstarted fence: count=%d mark=%v pool=%v", count, markErr, poolErr)
			}
			if _, err = f.store.BeginWorkloadAttempt(t.Context(), *attempt, nil, false); err == nil {
				t.Fatal("cancelled deployment recreated workload attempt")
			}
		})
	}
}

func TestDeploymentCapacityWaitPauseEffectBoundary(t *testing.T) {
	f := newPoolFixture(t)
	req := f.request("env", "api")
	pin, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "pause-effects", req, pin)
	f.accept(t, "pause-effects")
	id, _, err := poolTestRunner(f, &poolExecutor{}).Start(t.Context(), "org", "pause-effects")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := f.store.BeginPoolExecution(t.Context(), "org", "pause-effects", "api")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	attempt, err := f.store.BeginWorkloadAttempt(lock.Context(t.Context()), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: "pause-effects", Deployment: id}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	st := deployments.NewPgStore(f.db)
	if _, err = st.SetCapacityWait(t.Context(), "org", id, 0, []cloudinfra.CapacityBlock{{Reason: "hard_limit"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = f.store.MarkWorkloadEffects(t.Context(), *attempt); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("paused workload accepted effect: %v", err)
	}
	if err = lock.Attempt(t.Context()); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("paused pool accepted effect: %v", err)
	}
	if _, err = st.TransitionCapacityWait(t.Context(), "org", id, 1, deployments.StatusRunning, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = f.store.MarkWorkloadEffects(t.Context(), *attempt); err != nil {
		t.Fatalf("resume lost attempt: %v", err)
	}
	if err = lock.Attempt(t.Context()); err != nil {
		t.Fatalf("resume lost pool: %v", err)
	}
}

func TestDeploymentCapacityWaitRetainsUncertainAdmissions(t *testing.T) {
	for _, receipt := range []string{"started", "checkpoint", "bound", "missing", "legacy"} {
		t.Run(receipt, func(t *testing.T) {
			f := newPoolFixture(t)
			req := f.request("env", "api")
			pin, err := f.store.PreviewPlacement(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			f.plan(t, "uncertain", req, pin)
			f.accept(t, "uncertain")
			id, _, err := poolTestRunner(f, &poolExecutor{}).Start(t.Context(), "org", "uncertain")
			if err != nil {
				t.Fatal(err)
			}
			switch receipt {
			case "started":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_pool_execution SET started=true,active=true WHERE plan_id='uncertain'`)
			case "checkpoint":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_pool_execution SET checkpoint='receipt'::bytea WHERE plan_id='uncertain'`)
			case "bound":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_capacity_reservations SET binding_id='bound-receipt' WHERE plan_id='uncertain'`)
			case "missing":
				_, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_pool_execution WHERE plan_id='uncertain'`)
			case "legacy":
				_, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_workload_attempts(org_id,project_id,environment_id,service,plan_id,deployment_id,legacy_unfenced) VALUES('org','proj','env','api','uncertain',$1,true)`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var body string
				if e := f.sql.QueryRow(t.Context(), `SELECT jsonb_build_object('reservations',(SELECT jsonb_agg(to_jsonb(r)) FROM loomplane_capacity_reservations r WHERE plan_id='uncertain'),'execution',(SELECT jsonb_agg(to_jsonb(x)) FROM loomplane_pool_execution x WHERE plan_id='uncertain'),'attempts',(SELECT jsonb_agg(to_jsonb(w)) FROM loomplane_workload_attempts w WHERE plan_id='uncertain'))::text`).Scan(&body); e != nil {
					t.Fatal(e)
				}
				return body
			}
			before := snapshot()
			cancelCapacityDeployment(t, deployments.NewPgStore(f.db), id)
			if after := snapshot(); before != after {
				t.Fatalf("uncertain receipt changed: before=%s after=%s", before, after)
			}
		})
	}
}

type capacityHeldCredentials struct {
	*syncAccounts
	once             sync.Once
	entered, release chan struct{}
}

func (c *capacityHeldCredentials) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.syncAccounts.Credential(ctx, org, id)
}
func TestDeploymentCapacityWaitPausedActivity(t *testing.T) {
	f := newPoolFixture(t)
	req := f.request("env", "api")
	pin, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "paused-activity", req, pin)
	f.accept(t, "paused-activity")
	executor := &poolExecutor{}
	id, _, err := poolTestRunner(f, executor).Start(t.Context(), "org", "paused-activity")
	if err != nil {
		t.Fatal(err)
	}
	st := deployments.NewPgStore(f.db)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET steps=steps || '[{"action_id":"done","status":"succeeded","checkpoint":"cmVjZWlwdA=="}]'::jsonb WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	creds := &capacityHeldCredentials{syncAccounts: &syncAccounts{store: connections.NewPgStore(f.db)}, entered: make(chan struct{}), release: make(chan struct{})}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: st, Plans: f.plans, CloudProfiles: f.clouds, Credentials: creds, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(t.Context(), "org", id, "service:api", nil, func([]byte) error { return nil })
	}()
	<-creds.entered
	if _, err = st.SetCapacityWait(t.Context(), "org", id, 0, []cloudinfra.CapacityBlock{{Reason: "hard_limit"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	close(creds.release)
	err = <-done
	d, readErr := st.GetDeployment(t.Context(), "org", id)
	if !errors.Is(err, deployments.ErrCapacityPaused) || readErr != nil || executor.calls != 0 || d.Step("service:api").Status == deployments.StepFailed || string(d.Step("done").Checkpoint) != "receipt" {
		t.Fatalf("paused activity damaged progress: err=%v read=%v calls=%d deployment=%+v", err, readErr, executor.calls, d)
	}
	if _, err = st.TransitionCapacityWait(t.Context(), "org", id, 1, deployments.StatusRunning, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = runner.Run(t.Context(), "org", id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	d, err = st.GetDeployment(t.Context(), "org", id)
	if err != nil || executor.calls != 1 || d.Retries != 0 || d.Step("service:api").Status != deployments.StepSucceeded || string(d.Step("done").Checkpoint) != "receipt" {
		t.Fatalf("resumed activity: %+v calls=%d err=%v", d, executor.calls, err)
	}
}
