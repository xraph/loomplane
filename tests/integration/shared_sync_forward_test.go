//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

type forwardExecutor struct {
	calls, removals map[string]int
	failCleanup     bool
	expectedActor   string
	prepared        map[string]int64
	attempts        map[string]cloudinfra.WorkloadAttempt
}

func (e *forwardExecutor) PreparePoolEffects(ctx context.Context, _ *deployments.Job) error {
	if _, ok := server.EffectFenceFrom(ctx); !ok {
		return errors.New("missing physical fence")
	}
	a, ok := cloudinfra.WorkloadAttemptFrom(ctx)
	if !ok {
		return errors.New("missing workload attempt")
	}
	e.prepared[a.Environment] = a.Generation
	return nil
}
func (e *forwardExecutor) Apply(ctx context.Context, job *deployments.Job) (*deployments.Outcome, error) {
	if err := e.PreparePoolEffects(ctx, job); err != nil {
		return nil, err
	}
	key := job.Deployment.EnvironmentID
	e.calls[key]++
	attempt, _ := cloudinfra.WorkloadAttemptFrom(ctx)
	e.attempts[key] = attempt
	if job.Env["PINNED"] != "accepted" || job.Action.Service.CommitSHA != "reviewed" {
		return nil, errors.New("applied inputs changed")
	}
	source, _, err := job.Source(ctx)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	raw, err := io.ReadAll(source)
	if err != nil || string(raw) != "accepted artifact" {
		return nil, errors.New("artifact changed")
	}
	if err := job.Checkpoint([]byte(`{"candidate":"accepted"}`)); err != nil {
		return nil, err
	}
	return &deployments.Outcome{ExternalID: "candidate-" + key + "-" + job.Action.Name, Revision: "accepted-revision"}, nil
}
func (e *forwardExecutor) Remove(ctx context.Context, job *deployments.Job, b *planning.Binding) error {
	if e.expectedActor != "" && job.Deployment.StartedBy != e.expectedActor {
		return errors.New("cleanup used the previous provider actor")
	}
	if err := e.PreparePoolEffects(ctx, job); err != nil {
		return err
	}
	if e.failCleanup {
		return errors.New("cleanup unavailable")
	}
	e.removals[b.ExternalID]++
	return nil
}
func (*forwardExecutor) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*deployments.Observation, error) {
	return &deployments.Observation{Exists: true}, nil
}

type forwardAuth struct{ deniedUser string }

func (a *forwardAuth) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	if p.UserID == a.deniedUser && a.deniedUser != "" {
		return security.ErrForbidden
	}
	return nil
}

type forwardSyncStore struct {
	*cloudprofiles.PgStore
	phases []string
}

func (s *forwardSyncStore) SaveSync(ctx context.Context, sync *cloudprofiles.Sync, snapshot *cloudprofiles.Snapshot) error {
	if err := s.PgStore.SaveSync(ctx, sync, snapshot); err != nil {
		return err
	}
	s.phases = append(s.phases, sync.Status)
	return nil
}

type forwardFixture struct {
	*poolFixture
	service  *planning.Service
	syncs    *forwardSyncStore
	auth     *forwardAuth
	executor *forwardExecutor
	runner   func() *deployments.Runner
	routes   *forwardRoutes
}

func newForwardFixture(t *testing.T) *forwardFixture {
	t.Helper()
	f, _ := sharedAcceptanceFixture(t)
	auth := &forwardAuth{}
	syncs := &forwardSyncStore{PgStore: f.clouds}
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: syncs, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: auth})
	secrets, sources := syncSecrets{}, syncSources{}
	for _, env := range []string{"env", "second-env"} {
		bindings, err := f.plans.ListBindings(t.Context(), "org", env)
		if err != nil {
			t.Fatal(err)
		}
		b := bindings[0]
		var cfg planning.ServiceConfig
		if err = json.Unmarshal(b.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		ref := "org/" + env + "/applied-inputs/source/api"
		key := deployments.SourceKey("org", b.ProjectID, "app", "reviewed")
		cfg.AppliedInputs.Variables, cfg.AppliedInputs.ArtifactKey = ref, key
		secrets[ref] = []byte(`{"values":{"PINNED":"accepted"}}`)
		sources[key] = "accepted artifact"
		b.Config, err = json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.plans.PutBinding(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	exec := &forwardExecutor{calls: map[string]int{}, removals: map[string]int{}, failCleanup: true, prepared: map[string]int64{}, attempts: map[string]cloudinfra.WorkloadAttempt{}}
	routes := &forwardRoutes{executor: exec}
	accounts := &syncAccounts{store: connections.NewPgStore(f.db)}
	runner := func() *deployments.Runner {
		return deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Routes: routes, Plans: f.plans, Credentials: accounts, Variables: syncVars{}, Sources: sources, Resources: secrets, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": exec}, CloudProfiles: f.clouds, CloudSyncAuth: svc})
	}
	r := runner()
	svc.BindCloudSyncRunner(r, r)
	return &forwardFixture{poolFixture: f, service: svc, syncs: syncs, auth: auth, executor: exec, runner: runner, routes: routes}
}

func TestSharedSyncRetainsUnroutedAttemptsUntilCleanup(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	p := reviewPoolChange(t, f.service)
	sync, err := f.service.ApplyCloudChange(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "", p.ID, p.Revision, "forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("cleanup failure missing")
	}
	if slices.Contains(f.syncs.phases, cloudprofiles.SyncRouting) {
		t.Fatalf("route-free sync exposed routing wait: %v", f.syncs.phases)
	}
	saved, _, err := f.clouds.GetSync(ctx, "org", "", sync.ID)
	if err != nil || saved.Status != cloudprofiles.SyncCleanup {
		t.Fatalf("sync=%+v error=%v", saved, err)
	}
	for _, env := range []string{"env", "second-env"} {
		var settled bool
		if err = f.sql.QueryRow(ctx, `SELECT settled FROM loomplane_workload_attempts WHERE environment_id=$1`, env).Scan(&settled); err != nil || settled {
			t.Fatalf("%s attempt released before shared cleanup: settled=%v error=%v", env, settled, err)
		}
		b, readErr := f.plans.ListBindings(ctx, "org", env)
		if readErr != nil || len(b) != 1 || len(b[0].Replaced) != 1 {
			t.Fatalf("%s source lost: %+v %v", env, b, readErr)
		}
	}
	if _, err = f.clouds.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	plan, err := f.plans.GetPlan(ctx, "org", sync.ID+".env")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ValidateCloudSync(ctx, plan); err == nil {
		t.Fatal("cleanup authority authorized new provider effects after revoke")
	}
	f.executor.failCleanup = false
	restarted := f.runner()
	f.service.BindCloudSyncRunner(restarted, restarted)
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	finished, _, err := f.clouds.GetSync(ctx, "org", "", sync.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range finished.Environments {
		if env.Error != "" {
			t.Fatalf("completed cleanup retained its old error: %+v", env)
		}
	}
	for _, env := range []string{"env", "second-env"} {
		var settled bool
		if err = f.sql.QueryRow(ctx, `SELECT settled FROM loomplane_workload_attempts WHERE environment_id=$1`, env).Scan(&settled); err != nil || !settled {
			t.Fatalf("%s final guard=%v %v", env, settled, err)
		}
		if f.executor.calls[env] != 1 || f.executor.removals["instance-"+env] != 1 {
			t.Fatalf("duplicate apply/cleanup: %+v %+v", f.executor.calls, f.executor.removals)
		}
	}
}

// This route coordinator is a readiness fixture. The shared Runner, plans,
// reservations, workload guards and sync checkpoints use PostgreSQL.
type forwardRoutes struct {
	executor       *forwardExecutor
	pending, drift bool
	cleanupPending bool
	calls          int
	expectedActor  string
}

func (*forwardRoutes) ValidateRuntimeRemoval(context.Context, string, string, string) error {
	return nil
}
func (r *forwardRoutes) ReconcileReleaseRoutes(ctx context.Context, actor security.Principal, in cloudinfra.ReleaseRouteRequest) error {
	a, ok := cloudinfra.WorkloadAttemptFrom(ctx)
	if !ok {
		return errors.New("routing lost attempt guard")
	}
	if r.executor.prepared[a.Environment] != a.Generation {
		return errors.New("routing preceded physical workload fence")
	}
	if _, ok := server.EffectFenceFrom(ctx); !ok {
		return errors.New("routing lost physical fence")
	}
	if r.expectedActor != "" && actor.UserID != r.expectedActor {
		return errors.New("routing used the previous actor")
	}
	r.calls++
	if in.Checkpoint != nil {
		status := "succeeded"
		if r.pending {
			status = "waiting"
		}
		if err := in.Checkpoint(cloudprofiles.SyncRoute{RouteID: in.Routes[0].ID, PrepareID: in.Key + "/prepare", CutoverID: in.Key + "/cutover", ReleaseID: in.Key + "/release", Status: status, Hostname: in.Routes[0].Input.Hostname, RecordType: "A", Value: "192.0.2.22", TTL: 3600, NotBefore: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}); err != nil {
			return err
		}
	}
	if r.pending || r.drift || in.CleanupOnly && r.cleanupPending {
		return cloudinfra.ErrPending
	}
	return nil
}
func addForwardRoute(t *testing.T, f *forwardFixture) {
	t.Helper()
	route := cloudinfra.Route{ID: "route-env", OrgID: "org", ProjectID: "proj", Status: "ready", Revision: 1, Input: cloudinfra.RouteInput{ZoneID: "zone", CloudID: "cloud", EnvironmentID: "env", Service: "api", Hostname: "api.example.test", Port: 8080, ExternalTTLSeconds: 3600}, Binding: cloudinfra.RouteBinding{BindingID: "binding-env", InstanceID: "instance-env", ServerID: "server-1", ConnectionID: "runtime-1"}}
	raw, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_dns_zones(id,org_id,name,revision,body) VALUES('zone','org','example.test',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body) VALUES('route-env','org','proj','env','cloud','server-1','runtime-1','zone','api.example.test','ready',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
}
func retainUnrelatedForwardProject(t *testing.T, f *forwardFixture) json.RawMessage {
	t.Helper()
	ctx := t.Context()
	target, err := f.clouds.GetTarget(ctx, "org", "", "cloud", "pool-target")
	if err != nil {
		t.Fatal(err)
	}
	target.ID, target.Name = "unrelated-target", "unrelated"
	if err = f.clouds.CreateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_bindings SET config=jsonb_set(jsonb_set(config,'{cloud_placement,target_id}','"unrelated-target"'),'{cloud_placement,target_name}','"unrelated"') WHERE id='binding-second-env'`); err != nil {
		t.Fatal(err)
	}
	unrelated, err := f.clouds.SnapshotBindings(ctx, "org", "second-env")
	if err != nil {
		t.Fatal(err)
	}
	return unrelated
}

func TestSharedSyncRoutingRetainsSourceAndUnrelatedProject(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	// Both projects share server-1. Only the reviewed target migrates.
	unrelated := retainUnrelatedForwardProject(t, f)
	addForwardRoute(t, f)
	f.routes.pending = true
	p := reviewPoolChange(t, f.service)
	sync, err := f.service.ApplyCloudChange(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "", p.ID, p.Revision, "route-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, func([]byte) error { return nil }); !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatal("route did not wait", err)
	}
	saved, snapshot, err := f.clouds.GetSync(ctx, "org", "", sync.ID)
	if err != nil || saved.Status != cloudprofiles.SyncRouting {
		t.Fatalf("routing status %+v %v", saved, err)
	}
	var progress cloudprofiles.EnvironmentChange
	for _, env := range saved.Environments {
		if env.EnvironmentID == "env" {
			progress = env
		}
	}
	if progress.Status != cloudprofiles.SyncSucceeded || progress.RoutingStatus != cloudprofiles.SyncRouting || len(progress.Routes) != 1 || progress.Routes[0].Value != "192.0.2.22" || progress.Routes[0].ReleaseID == "" {
		t.Fatalf("missing independent route progress %+v", progress)
	}
	planBefore, err := f.plans.GetPlan(ctx, "org", progress.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	// Even a legacy external-routing flag cannot bypass newly recorded routes.
	for i := range saved.Environments {
		if saved.Environments[i].EnvironmentID == "env" {
			saved.Environments[i].ExternalRouting = true
		}
	}
	if err = f.clouds.SaveSync(ctx, saved, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.CompleteCloudRouting(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "", sync.ID, "env", "skip verification"); err == nil {
		t.Fatal("manual evidence bypassed recorded route")
	}
	for i := range saved.Environments {
		saved.Environments[i].ExternalRouting = false
	}
	if err = f.clouds.SaveSync(ctx, saved, snapshot); err != nil {
		t.Fatal(err)
	}
	b, err := f.plans.ListBindings(ctx, "org", "env")
	if err != nil || len(b[0].Replaced) != 1 {
		t.Fatal("source missing", err)
	}
	stale := f.executor.attempts["env"]
	if err = f.plans.SetReplaced(stale.Context(ctx), "org", b[0].ID, nil); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale apply attempt changed retained source", err)
	}
	if err = f.store.FinishWorkloadAttempt(ctx, stale, true); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale apply attempt settled routing guard", err)
	}
	if f.executor.calls["env"] != 1 || f.executor.calls["second-env"] != 0 || len(f.executor.removals) != 0 {
		t.Fatalf("unexpected effects %+v %+v", f.executor.calls, f.executor.removals)
	}
	pending, err := f.clouds.PendingSyncs(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != sync.ID {
		t.Fatal("routing recovery missing", pending, err)
	}
	restarted := f.runner()
	f.service.BindCloudSyncRunner(restarted, restarted)
	// A drifted external record must still block cleanup after a worker restart.
	f.routes.pending, f.routes.drift = false, true
	retryActor := security.Principal{OrgID: "org", UserID: "retry-user"}
	f.auth.deniedUser = "usr"
	if _, err = f.service.RetryCloudSync(ctx, retryActor, "", sync.ID); err != nil {
		t.Fatal(err)
	}
	snapshot.Actor = retryActor
	f.routes.expectedActor = retryActor.UserID
	f.executor.expectedActor = retryActor.UserID
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, func([]byte) error { return nil }); !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatal("external drift ignored", err)
	}
	if len(f.executor.removals) != 0 {
		t.Fatal("external drift removed source")
	}
	f.routes.drift = false
	f.executor.failCleanup = false
	f.routes.cleanupPending = true
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, nil); !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatal("cleanup missed destination drift", err)
	}
	cleanupWait, _, readErr := f.clouds.GetSync(ctx, "org", "", sync.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, env := range cleanupWait.Environments {
		if env.EnvironmentID == "env" && env.RoutingStatus != cloudprofiles.SyncRouting {
			t.Fatal("cleanup drift still reports routing success", env)
		}
	}
	f.routes.cleanupPending = false
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after, afterSnapshot, err := f.clouds.GetSync(ctx, "org", "", sync.ID)
	if err != nil || after.Status != cloudprofiles.SyncSucceeded || !reflect.DeepEqual(snapshot, afterSnapshot) {
		t.Fatal("accepted snapshot changed", err)
	}
	planAfter, err := f.plans.GetPlan(ctx, "org", progress.PlanID)
	if err != nil || !reflect.DeepEqual(planBefore, planAfter) {
		t.Fatal("accepted plan changed", err)
	}
	historical, readErr := deployments.NewPgStore(f.db).GetDeployment(ctx, "org", progress.DeploymentID)
	if readErr != nil || historical.StartedBy != "usr" {
		t.Fatal("retry rewrote the deployment initiator", readErr)
	}
	actual, err := f.clouds.SnapshotBindings(ctx, "org", "second-env")
	if err != nil || string(actual) != string(unrelated) {
		t.Fatal("unrelated project changed", err)
	}
	if f.executor.calls["env"] != 1 || f.executor.removals["instance-env"] != 1 || len(f.executor.removals) != 1 {
		t.Fatalf("duplicate or foreign effects %+v %+v", f.executor.calls, f.executor.removals)
	}
}

func TestSharedSyncRejectsFabricatedAllocationBeforeEffects(t *testing.T) {
	f := newForwardFixture(t)
	addForwardRoute(t, f)
	p := reviewPoolChange(t, f.service)
	sync, err := f.service.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", p.ID, p.Revision, "fabricated")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.plans.GetPlan(t.Context(), "org", sync.ID+".env")
	if err != nil {
		t.Fatal(err)
	}
	pin := plan.Actions[0].Service.CloudPlacement
	if pin.Allocation == "" || pin.Allocation == plan.ID {
		t.Fatal("fixture lost its immutable preview allocation")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,cloud_placement,allocation}','"fabricated"') WHERE id=$1`, plan.ID); err == nil {
		t.Fatal("accepted plan mutation was not rejected")
	}
	saved, snapshot, err := f.clouds.GetSync(t.Context(), "org", "", sync.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved.Status = cloudprofiles.SyncRunning
	if err = f.clouds.SaveSync(t.Context(), saved, snapshot); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ValidateCloudSync(t.Context(), plan); err != nil {
		t.Fatal("accepted preview allocation was rejected", err)
	}
	// The shared sync ID alone does not authorize an altered candidate.
	pin.Allocation = "fabricated"
	if err = f.service.ValidateCloudSync(t.Context(), plan); !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatal("fabricated shared allocation was accepted")
	}
	if len(f.executor.calls) != 0 || len(f.executor.removals) != 0 || f.routes.calls != 0 {
		t.Fatal("fabricated allocation reached effects")
	}
}

func TestSharedSyncRouteHistoryIsProjectScoped(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	participants := []cloudprofiles.EnvironmentSnapshot{{ProjectID: "proj", EnvironmentID: "env"}, {ProjectID: "other", EnvironmentID: "other-env"}}
	insertSharedCloudHistory(t, conn, "org", "route-history", "", participants, time.Now().UTC())
	saved, snapshot, err := st.GetSync(t.Context(), "org", "", "route-history")
	if err != nil {
		t.Fatal(err)
	}
	for i := range saved.Environments {
		saved.Environments[i].Routes = []cloudprofiles.SyncRoute{{RouteID: saved.Environments[i].EnvironmentID, Hostname: saved.Environments[i].EnvironmentID + ".example.test", Value: "192.0.2.22"}}
	}
	if err = st.SaveSync(t.Context(), saved, snapshot); err != nil {
		t.Fatal(err)
	}
	svc := planning.NewService(planning.Deps{Projects: projects.NewPgStore(db), CloudProfiles: st, Authz: sharedHistoryAuthorizer{project: "proj"}})
	actor := security.Principal{OrgID: "org", UserID: "developer"}
	single, err := svc.CloudSync(t.Context(), actor, "proj", saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.CloudSyncs(t.Context(), actor, "proj")
	if err != nil || len(list) != 1 {
		t.Fatal("history list", err)
	}
	for _, view := range []*cloudprofiles.Sync{single, list[0]} {
		raw, encodeErr := json.Marshal(view)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if len(view.Environments) != 1 || view.Environments[0].Routes[0].Hostname != "env.example.test" || strings.Contains(string(raw), "other-env") {
			t.Fatal("route history leaked another project", string(raw))
		}
	}
	manager := planning.NewService(planning.Deps{CloudProfiles: st, Authz: &syncAuth{}})
	full, err := manager.CloudSync(t.Context(), actor, "", saved.ID)
	if err != nil || len(full.Environments) != 2 || len(full.Environments[1].Routes) != 1 {
		t.Fatal("manager lost route history", err)
	}
}

func TestSharedSyncMixedRoutingEvidenceCannotBypassRecordedRoutes(t *testing.T) {
	f := newForwardFixture(t)
	ctx := t.Context()
	addForwardRoute(t, f)
	// Add a public service without recorded routes through a real accepted source plan.
	retainUnrelatedForwardProject(t, f)
	host, err := f.store.GetServer(ctx, "org", "cloud", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	host.Health.EffectiveCPUMillis = 3000
	if err = f.store.SaveServer(ctx, host); err != nil {
		t.Fatal(err)
	}
	f.pool.Members[0].CPUMillis = 3000
	if err = f.store.SavePool(ctx, f.pool, f.pool.Revision); err != nil {
		t.Fatal(err)
	}

	req := f.request("env", "public")
	req.CPUMillis = 500
	req.StickyServerID = "server-1"
	placement, err := f.store.PreviewPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.plan(t, "source-public", req, placement)
	f.accept(t, "source-public")
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('source-public','org','proj','env','production','source-public','reviewed','succeeded','usr',now())`); err != nil {
		t.Fatal(err)
	}
	bindings, err := f.plans.ListBindings(ctx, "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	source := *bindings[0]
	var cfg planning.ServiceConfig
	if err = json.Unmarshal(source.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Service, cfg.Public, cfg.Port = "public", true, 8080
	cfg.Size = &manifest.Size{CPU: 0.5, Memory: 512}
	cfg.Endpoint = &manifest.Endpoint{HostPort: 18080}
	cfg.Target, cfg.CloudPlacement = placement.ConnectionID, pin
	cfg.TargetSettings = map[string]string{"docker_id": placement.ConnectionID, "architecture": "amd64"}
	source.ID, source.Name, source.ExternalID, source.PlanID, source.ConnectionID = "binding-public", "public", "instance-public", "source-public", placement.ConnectionID
	source.Config, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.plans.PutBinding(ctx, &source); err != nil {
		t.Fatal(err)
	}
	p := reviewPoolChange(t, f.service)
	if !slices.ContainsFunc(p.Environments, func(e cloudprofiles.EnvironmentChange) bool {
		return e.EnvironmentID == "env" && e.ExternalRouting && len(e.Services) == 2
	}) {
		t.Fatalf("mixed preview missing unmanaged requirement: %+v", p.Environments)
	}
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	sync, err := f.service.ApplyCloudChange(ctx, actor, "", p.ID, p.Revision, "mixed")
	if err != nil {
		t.Fatal(err)
	}
	f.routes.pending = true
	f.executor.failCleanup = false
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, nil); !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatal("recorded route did not wait", err)
	}
	f.routes.pending = false
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, nil); err != nil {
		t.Fatal(err)
	}
	waiting, _, err := f.clouds.GetSync(ctx, "org", "", sync.ID)
	if err != nil || waiting.Status != cloudprofiles.SyncRouting || len(f.executor.removals) != 0 || !slices.ContainsFunc(waiting.Environments, func(e cloudprofiles.EnvironmentChange) bool {
		return e.EnvironmentID == "env" && e.RoutingStatus == cloudprofiles.SyncSucceeded && e.RoutingEvidence == "" && len(e.Routes) == 1
	}) {
		t.Fatal("recorded readiness bypassed unmanaged evidence", waiting, err)
	}
	f.routes.pending = true
	if _, err = f.service.CompleteCloudRouting(ctx, actor, "", sync.ID, "env", "unmanaged service independently verified"); err != nil {
		t.Fatal("mixed environment cannot submit unmanaged evidence", err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, nil); !errors.Is(err, cloudinfra.ErrPending) || len(f.executor.removals) != 0 {
		t.Fatal("manual evidence bypassed recorded wait", err, f.executor.removals)
	}
	if _, err = f.service.RetryCloudSync(ctx, actor, "", sync.ID); err != nil {
		t.Fatal(err)
	}
	f.routes.pending, f.routes.drift = false, true
	f.service.BindCloudSyncRunner(f.runner(), f.runner())
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, nil); !errors.Is(err, cloudinfra.ErrPending) || len(f.executor.removals) != 0 {
		t.Fatal("manual evidence bypassed drift after restart", err)
	}
	f.routes.drift = false
	if err = f.service.ReconcileCloudSync(ctx, "org", "", sync.ID, nil); err != nil {
		t.Fatal(err)
	}
	done, _, err := f.clouds.GetSync(ctx, "org", "", sync.ID)
	if err != nil || done.Status != cloudprofiles.SyncSucceeded || len(f.executor.removals) != 2 {
		t.Fatal("mixed routing did not finish", done, err, f.executor.removals)
	}
}

type routeRetryAuth struct {
	actors []string
	denied string
}

func (a *routeRetryAuth) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	a.actors = append(a.actors, p.UserID)
	if p.UserID == a.denied {
		return security.ErrForbidden
	}
	return nil
}
func TestSharedRouteRetryPersistsExecutionActorAndRejectsStaleWorker(t *testing.T) {
	f := newForwardFixture(t)
	addForwardRoute(t, f)
	ctx := t.Context()
	route, err := f.store.Route(ctx, "org", "proj", "route-env")
	if err != nil {
		t.Fatal(err)
	}
	original := security.Principal{OrgID: "org", UserID: "original"}
	current := security.Principal{OrgID: "org", UserID: "current"}
	op := &cloudinfra.RouteOperation{ID: "accepted-retry", Kind: "prepare", Status: "waiting", Attempt: 1, Revision: 1, Principal: original, Route: *route, Source: route}
	if err = f.store.PreviewRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	stale, err := f.store.RouteOperation(ctx, "org", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	auth := &routeRetryAuth{denied: "original"}
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Domains: f.store, Authz: auth})
	if _, err = svc.RetryRoute(ctx, original, "proj", op.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("revoked retry accepted", err)
	}
	unchanged, err := f.store.RouteOperation(ctx, "org", op.ID)
	if err != nil || !reflect.DeepEqual(stale, unchanged) {
		t.Fatal("denied retry changed durable operation", err)
	}
	accepted, err := svc.RetryRoute(ctx, current, "proj", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	restartedStore := cloudinfra.NewPgStore(f.db)
	saved, err := restartedStore.RouteOperation(ctx, "org", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ExecutionPrincipal == nil || *saved.ExecutionPrincipal != current || saved.Principal != original || saved.ID != op.ID || !reflect.DeepEqual(saved.Route, op.Route) || saved.Attempt != 2 || saved.Revision != accepted.Revision {
		t.Fatalf("retry storage lost current actor or accepted inputs: %+v", saved)
	}
	public, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "current") || strings.Contains(string(public), "execution_principal") {
		t.Fatal("private actor leaked", string(public))
	}
	stale.Status = "succeeded"
	if err = f.store.SaveRouteOperation(ctx, stale); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale worker overwrote retry", err)
	}
	if _, err = f.store.BeginIngress(ctx, stale); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale worker reclaimed execution", err)
	}
	auth.actors = nil
	worker := cloudinfra.NewService(cloudinfra.Deps{Store: restartedStore, Domains: restartedStore, Authz: auth})
	// Binding resolution stops this fixture before DNS or SSH. Authorization runs in the real worker.
	if err = worker.ReconcileRoute(ctx, "org", op.ID, nil); err != nil && !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatal(err)
	}
	if len(auth.actors) == 0 || slices.Contains(auth.actors, "original") {
		t.Fatal("restarted worker did not use durable retry actor", auth.actors)
	}
	final, err := restartedStore.RouteOperation(ctx, "org", op.ID)
	if err != nil || final.Principal != original || final.ExecutionPrincipal == nil || *final.ExecutionPrincipal != current {
		t.Fatal("worker changed audit or execution actor", err)
	}
}
