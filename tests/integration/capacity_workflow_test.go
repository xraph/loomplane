//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/keysmith"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	kspg "github.com/xraph/keysmith/store/postgres"

	"github.com/xraph/loomplane/internal/apikeys"

	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

type workflowPlanner struct{}

func (workflowPlanner) Plan(context.Context, string, string) (string, error) { return "ready", nil }
func (workflowPlanner) ApplyDecision(context.Context, string, string, orchestration.Decision) (string, error) {
	return "approved", nil
}
func (workflowPlanner) Expire(context.Context, string, string) (string, error) { return "expired", nil }
func (workflowPlanner) Fail(context.Context, string, string, string) error     { return nil }

type workflowCandidate struct {
	ready     bool
	candidate *cloudinfra.Placement
}

func (c *workflowCandidate) EnsureCapacity(context.Context, cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	if !c.ready {
		return &cloudinfra.CapacityDecision{Block: &cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "cpu", Retryable: true}}, nil
	}
	return &cloudinfra.CapacityDecision{Placement: c.candidate}, nil
}
func pumpWorkflow(t *testing.T, w *drt.Worker) {
	t.Helper()
	for range 100 {
		a, e := w.RunOnce(t.Context(), durable.TaskWorkflow)
		if e != nil {
			t.Fatal(e)
		}
		b, e := w.RunOnce(t.Context(), durable.TaskActivity)
		if e != nil {
			t.Fatal(e)
		}
		if !a && !b {
			return
		}
	}
	t.Fatal("workflow did not quiesce")
}
func TestCapacityWorkflowPostgresReplay(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "durable-capacity", "env")
	acceptFuture(t, f, plan.ID)
	if err := ops.Migrate(t.Context(), f.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	ds := dispatchpg.New(db)
	req := f.request("env", "api")
	req.CPUMillis = 2000
	req.PlanID = plan.ID
	candidate, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	capacity := &workflowCandidate{candidate: candidate}
	resolver := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: capacity})
	executor := &poolExecutor{}
	st := deployments.NewPgStore(f.db)
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: st, Plans: f.plans, Capacity: resolver, CloudProfiles: f.clouds, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	workflows := orchestration.NewDeployments()
	dispatcher, err := dispatch.New(dispatch.WithStore(ds))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(workflows.Options("legacy", 1)))
	if err != nil {
		t.Fatal(err)
	}
	workflows.Bind(workflowPlanner{}, runner, func() *engine.Engine { return eng })
	workflows.BindRunStore(orchestration.NewPgRunStore(f.db))
	// A new plan starts on the current build, which waits for capacity as
	// v3 does.
	options := workflows.ManagedCleanupOptions("capacity", 1)
	worker, err := drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindWorker(orchestration.CurrentBuild, worker)
	if err = workflows.Start(t.Context(), "org", plan.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	if err = workflows.Decide(t.Context(), plan.ID, "approve", orchestration.Decision{Approve: true, Revision: plan.Revision, By: "usr"}); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	dep, err := st.ForPlan(t.Context(), "org", plan.ID)
	if err != nil || dep.Status != deployments.StatusPaused {
		t.Fatalf("wait: %+v %v", dep, err)
	}
	identity := dep.ID
	route, err := orchestration.NewPgRunStore(f.db).ForDeployment(t.Context(), "org", identity)
	if err != nil || route.Request.BuildID != orchestration.CurrentBuild {
		t.Fatalf("route: %+v %v", route, err)
	}
	if _, err = orchestration.NewPgRunStore(f.db).ForDeployment(t.Context(), "other", identity); err == nil {
		t.Fatal("cross-org routing")
	}
	worker, err = drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindWorker(orchestration.CurrentBuild, worker)
	capacity.ready = true
	for range 2 {
		if err = workflows.SignalCapacity(t.Context(), "org", identity, dep.CapacityRevision); err != nil {
			t.Fatal(err)
		}
	}
	pumpWorkflow(t, worker)
	dep, err = st.GetDeployment(t.Context(), "org", identity)
	if err != nil || dep.Status != deployments.StatusSucceeded || dep.Retries != 0 {
		t.Fatalf("resume: %+v %v", dep, err)
	}
	saved, err := f.plans.GetPlan(t.Context(), "org", plan.ID)
	if err != nil || saved.Actions[0].Service.CloudPlacement != nil {
		t.Fatalf("approved review mutated: %+v %v", saved, err)
	}
	if err = workflows.RecoverCapacity(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListCapacityTransitions(t.Context(), "org", deployments.CapacityConsumerWorkflow, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("workflow ack: %+v %v", events, err)
	}
	events, err = st.ListCapacityTransitions(t.Context(), "org", deployments.CapacityConsumerNotification, 100)
	if err != nil || len(events) == 0 {
		t.Fatalf("notification consumer lost events: %+v %v", events, err)
	}
	// Saving a retried start retains the original interval and input.
	changed := route
	changed.Request.Input = []byte(`{}`)
	again, err := orchestration.NewPgRunStore(f.db).Save(t.Context(), changed)
	if err != nil || string(again.Request.Input) != string(route.Request.Input) {
		t.Fatalf("start fingerprint changed: %+v %v", again, err)
	}
	var in orchestration.DeploymentInput
	if err = json.Unmarshal(again.Request.Input, &in); err != nil || in.ReconcileInterval != 30*time.Second {
		t.Fatalf("timer input: %+v %v", in, err)
	}
	if _, _, err = runner.Start(t.Context(), "org", plan.ID); err == nil {
		t.Fatal("legacy worker accepted deferred plan")
	}
	if err = workflows.Decide(t.Context(), plan.ID, "late", orchestration.Decision{}); !errors.Is(err, orchestration.ErrNotRunning) {
		t.Fatalf("closed decision: %v", err)
	}
}

func TestCapacityWorkflowManagerKeyAuthority(t *testing.T) {
	for _, approval := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			for _, attempted := range []bool{false, true} {
				t.Run(fmt.Sprintf("approval=%v/expired=%v/attempted=%v", approval, expired, attempted), func(t *testing.T) {
					f, _, d := expansionFixture(t, approval, 0)
					if err := ops.Migrate(t.Context(), f.sql.Config().ConnString(), []*migrate.Group{kspg.Migrations}); err != nil {
						t.Fatal(err)
					}
					keyDB := pgdriver.New()
					if err := keyDB.Open(t.Context(), f.sql.Config().ConnString()); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = keyDB.Close() })
					keys := kspg.New(keyDB)
					ke, err := keysmith.NewEngine(keysmith.WithStore(keys))
					if err != nil {
						t.Fatal(err)
					}
					expires := time.Now().Add(time.Hour)
					k := &key.Key{ID: keyid.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: "manager", State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
					if err = keys.Keys().Create(t.Context(), k); err != nil {
						t.Fatal(err)
					}
					verifier := apikeys.New(ke, "app", nil, nil)
					f.svc.CapacityPrincipal = verifier
					p := f.p
					p.KeyID = k.ID.String()
					if !approval {
						policy, e := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
						if e != nil {
							t.Fatal(e)
						}
						if _, e = f.svc.SaveCapacityPolicy(t.Context(), p, "cloud", cloudinfra.CapacityPolicyInput{ExpectedRevision: policy.Revision, Policy: *policy}); e != nil {
							t.Fatal(e)
						}
					}
					expansion := ensureExpansion(t, f, d)
					if approval {
						if expansion, err = f.svc.ApproveExpansion(t.Context(), p, "cloud", expansion.ID, expansion.Revision); err != nil {
							t.Fatal(err)
						}
					}
					if attempted {
						reconcileExpansion(t, f)
					}
					if expired {
						past := time.Now().Add(-time.Minute)
						k.ExpiresAt = &past
					} else {
						k.State = key.StateRevoked
					}
					if err = keys.Keys().Update(t.Context(), k); err != nil {
						t.Fatal(err)
					}
					// Recreate both services. Authority comes from the saved policy/approval and key rows.
					restarted, err := keysmith.NewEngine(keysmith.WithStore(kspg.New(keyDB)))
					if err != nil {
						t.Fatal(err)
					}
					deps := f.svc.Deps
					deps.CapacityPrincipal = apikeys.New(restarted, "app", nil, nil)
					f.svc = cloudinfra.NewService(deps)
					reconcileExpansion(t, f)
					if !attempted && f.provider.creates != 0 {
						t.Fatal("revoked or expired manager key created infrastructure")
					}
					if attempted && f.provider.creates != 1 {
						t.Fatalf("accepted provider receipt repeated: %d", f.provider.creates)
					}
					op, err := f.store.GetOperation(t.Context(), "org", "cloud", expansion.OperationID)
					if err != nil {
						t.Fatal(err)
					}
					if attempted && op.Status != "waiting" {
						t.Fatalf("accepted operation could not reconcile: %+v", op)
					}
				})
			}
		}
	}
}

type syncWorkflowCandidate struct {
	fixture *poolFixture
	ready   bool
}

func (c *syncWorkflowCandidate) EnsureCapacity(ctx context.Context, d cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	if !c.ready {
		return &cloudinfra.CapacityDecision{Block: &cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "cpu", CloudID: d.CloudID, PoolID: d.PoolID, Retryable: true}}, nil
	}
	req := c.fixture.request(d.EnvironmentID, d.Service)
	req.PlanID = d.PlanID
	req.PoolID = d.PoolID
	req.TargetRevision = 2
	req.CPUMillis = d.Resources.CPUMillis
	req.MemoryBytes = d.Resources.MemoryBytes
	p, err := c.fixture.store.PreviewPlacement(ctx, req)
	if err != nil {
		return nil, err
	}
	return &cloudinfra.CapacityDecision{Placement: p}, nil
}
func TestCapacityWorkflowCloudSyncRestart(t *testing.T) {
	f, _ := sharedAcceptanceFixture(t)
	next, err := f.store.GetPool(t.Context(), "org", "cloud", "next-pool")
	if err != nil {
		t.Fatal(err)
	}
	next.Members = nil
	next.Revision++
	if err = f.store.SavePool(t.Context(), next, next.Revision-1); err != nil {
		t.Fatal(err)
	}
	template := cloudinfra.PoolExpansionPolicy{PoolID: "next-pool", ConnectionID: "runtime-1", Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"small"}}
	policy := cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Enabled: true, Pools: []cloudinfra.PoolExpansionPolicy{template}}
	raw, _ := json.Marshal(policy)
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,enabled,servers,cpu_millis,memory_bytes,storage_bytes,body) VALUES('org','cloud',1,true,10,20000,21474836480,107374182400,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	secrets, sources := syncSecrets{}, syncSources{}
	for _, env := range []string{"env", "second-env"} {
		bindings, e := f.plans.ListBindings(t.Context(), "org", env)
		if e != nil {
			t.Fatal(e)
		}
		b := bindings[0]
		var cfg planning.ServiceConfig
		if e = json.Unmarshal(b.Config, &cfg); e != nil {
			t.Fatal(e)
		}
		ref := "org/" + env + "/applied-inputs/source/api"
		artifact := deployments.SourceKey("org", b.ProjectID, "app", "reviewed")
		cfg.AppliedInputs.Variables = ref
		cfg.AppliedInputs.ArtifactKey = artifact
		secrets[ref] = []byte(`{"values":{"PINNED":"accepted"}}`)
		sources[artifact] = "accepted artifact"
		b.Config, e = json.Marshal(cfg)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.plans.PutBinding(t.Context(), b); e != nil {
			t.Fatal(e)
		}
	}
	candidate := &syncWorkflowCandidate{fixture: f}
	accounts := &syncAccounts{store: connections.NewPgStore(f.db)}
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: accounts, Providers: providers.Registry(), Authz: &syncAuth{}, CapacityTemplates: capacityPlanningTemplate{}, Capacity: candidate})
	executor := &forwardExecutor{calls: map[string]int{}, removals: map[string]int{}, prepared: map[string]int64{}, attempts: map[string]cloudinfra.WorkloadAttempt{}}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, Credentials: accounts, Variables: syncVars{}, Sources: sources, Resources: secrets, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}, CloudProfiles: f.clouds, CloudSyncAuth: svc, Capacity: svc})
	svc.BindCloudSyncRunner(runner, runner)
	preview := reviewPoolChange(t, svc)
	accepted, err := svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "workflow")
	if err != nil {
		t.Fatal(err)
	}
	if err = ops.Migrate(t.Context(), f.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	ds := dispatchpg.New(db)
	dispatcher, err := dispatch.New(dispatch.WithStore(ds))
	if err != nil {
		t.Fatal(err)
	}
	workflows := orchestration.NewDeployments()
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(workflows.Options("sync-legacy", 1)))
	if err != nil {
		t.Fatal(err)
	}
	workflows.Bind(svc, runner, func() *engine.Engine { return eng })
	workflows.BindCloudSync(svc)
	workflows.BindRunStore(orchestration.NewPgRunStore(f.db))
	opts := workflows.CapacityOptions("sync-capacity", 1)
	worker, err := drt.NewWorker(ds, opts)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindCapacityWorker(worker)
	if err = workflows.StartCloudSync(t.Context(), "org", "", accepted.ID, accepted.Attempt); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	if len(executor.calls) != 0 {
		t.Fatalf("mixed sync had premature effects: %v", executor.calls)
	}
	dep, err := deployments.NewPgStore(f.db).ForPlan(t.Context(), "org", accepted.ID+".env")
	if err != nil || dep.Status != deployments.StatusPaused {
		t.Fatalf("sync wait: %+v %v", dep, err)
	}
	route, err := orchestration.NewPgRunStore(f.db).ForDeployment(t.Context(), "org", dep.ID)
	if err != nil || route.Request.WorkflowType != "loomplane.cloud.sync.v1" || route.Request.BuildID != orchestration.CapacityBuild {
		t.Fatalf("participant route: %+v %v", route, err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_server_pool_members(org_id,pool_id,server_id,cpu_millis,memory_bytes) VALUES('org','next-pool','server-2',2000,2147483648)`); err != nil {
		t.Fatal(err)
	}
	next.Members = []cloudinfra.PoolMember{{ServerID: "server-2", CPUMillis: 2000, MemoryBytes: 2 << 30}}
	raw, _ = json.Marshal(next)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_server_pools SET body=$1 WHERE id='next-pool'`, raw); err != nil {
		t.Fatal(err)
	}
	candidate.ready = true
	worker, err = drt.NewWorker(ds, opts)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindCapacityWorker(worker)
	if err = workflows.SignalCapacity(t.Context(), "org", dep.ID, dep.CapacityRevision); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	sync, _, err := f.clouds.GetSync(t.Context(), "org", "", accepted.ID)
	if err != nil || sync.Status != cloudprofiles.SyncSucceeded {
		execution, _ := ds.GetExecution(t.Context(), route.Request.Key)
		t.Fatalf("sync resume: %+v %v workflow=%+v", sync, err, execution)
	}
	for _, env := range []string{"env", "second-env"} {
		if executor.calls[env] != 1 {
			t.Fatalf("participant effects: %v", executor.calls)
		}
		p, e := f.plans.GetPlan(t.Context(), "org", accepted.ID+"."+env)
		if e != nil || p.Actions[0].Service.CapacityIntent == nil || p.Actions[0].Service.CloudPlacement != nil {
			t.Fatalf("immutable sync plan: %+v %v", p, e)
		}
	}
	if err = workflows.StartCloudSync(t.Context(), "org", "", accepted.ID, accepted.Attempt); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	if executor.calls["env"] != 1 || executor.calls["second-env"] != 1 {
		t.Fatal("duplicate sync repeated effects")
	}
}

func TestCapacityWorkflowPersistedRetryRecoveryRouting(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "routed-capacity", "env")
	acceptFuture(t, f, plan.ID)
	st := deployments.NewPgStore(f.db)
	dep := capacityDeployment(t, st, plan)
	if err := ops.Migrate(t.Context(), f.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	ds := dispatchpg.New(db)
	dispatcher, err := dispatch.New(dispatch.WithStore(ds))
	if err != nil {
		t.Fatal(err)
	}
	workflows := orchestration.NewDeployments()
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(workflows.Options("routing-legacy", 1)))
	if err != nil {
		t.Fatal(err)
	}
	workflows.Bind(workflowPlanner{}, deployments.NewRunner(deployments.RunnerDeps{Store: st, Plans: f.plans}), func() *engine.Engine { return eng })
	routes := orchestration.NewPgRunStore(f.db)
	workflows.BindRunStore(routes)
	options := workflows.CapacityOptions("routing", 1)
	worker, err := drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindCapacityWorker(worker)
	raw, err := json.Marshal(orchestration.DeploymentInput{OrgID: "org", PlanID: plan.ID, Window: time.Hour, ReconcileInterval: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	savedStart := orchestration.DeploymentRun{OrgID: "org", PlanID: plan.ID, Request: durable.StartRequest{Key: durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: plan.ID, RunID: "1"}, RequestID: "start:" + plan.ID, BuildID: orchestration.CapacityBuild, Queue: orchestration.DurableQueue, WorkflowType: orchestration.WorkflowDeployment, Input: raw}}
	if _, err = routes.Save(t.Context(), savedStart); err != nil {
		t.Fatal(err)
	}
	// A crash before Dispatch accepts Start leaves the exact request pending.
	if err = workflows.RecoverCapacity(t.Context()); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	// A crash after acceptance but before acknowledgement repeats only the receipt.
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployment_workflow_runs SET start_delivered=false WHERE workflow_id=$1`, plan.ID); err != nil {
		t.Fatal(err)
	}
	before, err := ds.GetExecution(t.Context(), savedStart.Request.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = workflows.RecoverCapacity(t.Context()); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	after, err := ds.GetExecution(t.Context(), savedStart.Request.Key)
	if err != nil || after.LastSequence != before.LastSequence || string(after.Input) != string(raw) {
		t.Fatalf("start redelivery changed history: %+v %+v %v", before, after, err)
	}
	if err = workflows.Cancel(t.Context(), plan.ID, "fixture original closed"); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	if err = st.Finish(t.Context(), "org", dep.ID, deployments.StatusFailed, "fixture failure", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Reopen(t.Context(), "org", dep.ID); err != nil {
		t.Fatal(err)
	}
	retryID := dep.ID + ".retry.3"
	if err = workflows.Retry(t.Context(), "org", dep.ID, retryID, [][]string{{"pending"}}); err != nil {
		t.Fatal(err)
	}
	saved, err := orchestration.NewPgRunStore(f.db).ForDeployment(t.Context(), "org", dep.ID)
	if err != nil || saved.Request.WorkflowID != retryID || saved.Request.BuildID != orchestration.CapacityBuild {
		t.Fatalf("retry routing: %+v %v", saved, err)
	}
	if err = workflows.SignalCapacity(t.Context(), "org", dep.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = workflows.Cancel(t.Context(), plan.ID, "fixture retry closed"); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	retry, err := ds.GetExecution(t.Context(), saved.Request.Key)
	if err != nil || retry.State != durable.StateCancelled {
		t.Fatalf("cancel did not reach retry: %+v %v", retry, err)
	}
	// Recreate the router and worker before release recovery.
	workflows.BindRunStore(orchestration.NewPgRunStore(f.db))
	worker, err = drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindCapacityWorker(worker)
	if err = workflows.ResumeRelease(t.Context(), "org", plan.ID); err != nil {
		t.Fatal(err)
	}
	saved, err = routes.ForDeployment(t.Context(), "org", dep.ID)
	if err != nil || saved.Request.WorkflowID != "resume-release-v3:"+plan.ID || saved.Request.BuildID != orchestration.CapacityBuild {
		t.Fatalf("recovery routing: %+v %v", saved, err)
	}
	if err = workflows.SignalCapacity(t.Context(), "org", dep.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = workflows.Cancel(t.Context(), plan.ID, "fixture recovery closed"); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	recovered, err := ds.GetExecution(t.Context(), saved.Request.Key)
	if err != nil || recovered.State != durable.StateCancelled {
		t.Fatalf("cancel did not reach recovery: %+v %v", recovered, err)
	}
	// A full failed batch rotates behind a later valid request on the next pass.
	for i := range 101 {
		r := savedStart
		r.Request.WorkflowID = fmt.Sprintf("pending-%d", i)
		r.Request.RequestID = "start:" + r.Request.WorkflowID
		if i < 100 {
			r.Request.WorkflowType = "unsupported"
		}
		if _, err = routes.Save(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	if err = workflows.RecoverCapacity(t.Context()); err == nil {
		t.Fatal("unsupported pending starts should remain undelivered")
	}
	laterKey := savedStart.Request.Key
	laterKey.WorkflowID = "pending-100"
	if _, err = ds.GetExecution(t.Context(), laterKey); !errors.Is(err, durable.ErrNotFound) {
		t.Fatalf("first bounded batch unexpectedly reached later request: %v", err)
	}
	if err = workflows.RecoverCapacity(t.Context()); err == nil {
		t.Fatal("failed requests disappeared")
	}
	if later, readErr := ds.GetExecution(t.Context(), laterKey); readErr != nil || later.BuildID != orchestration.CapacityBuild {
		t.Fatalf("failed batch starved later start: %+v %v", later, readErr)
	}
}
