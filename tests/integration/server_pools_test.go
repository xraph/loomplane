//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type poolFixture struct {
	store  *cloudinfra.PgStore
	plans  *planning.PgStore
	clouds *cloudprofiles.PgStore
	sql    *pgx.Conn
	db     *pgdriver.PgDB
	pool   *cloudinfra.Pool
}

func newPoolFixture(t *testing.T) *poolFixture {
	t.Helper()
	clouds, sql, db := cloudSyncDatabase(t)
	st := cloudinfra.NewPgStore(db)
	now := time.Now().UTC()
	cs := connections.NewPgStore(db)
	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("runtime-%d", i)
		if err := cs.CreateConnection(t.Context(), &connections.Connection{ID: id, OrgID: "org", Cloud: "server", Name: id, Account: id, Details: map[string]string{"docker_id": id, "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		v := &cloudinfra.Server{ID: fmt.Sprintf("server-%d", i), OrgID: "org", CloudID: "cloud", ConnectionID: id, RuntimeConnectionID: id, Ownership: "adopted", Provider: []string{"", "digitalocean", "generic"}[i], AccountID: id, Status: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now, Health: cloudinfra.RuntimeHealth{DockerID: id, Architecture: "amd64", CPUs: 16, MemoryBytes: 8 << 30, EffectiveCPUMillis: 2000, EffectiveMemoryBytes: 2 << 30, CheckedAt: now}}
		if err := st.SaveServer(t.Context(), v); err != nil {
			t.Fatal(err)
		}
	}
	pool := &cloudinfra.Pool{ID: "pool", OrgID: "org", CloudID: "cloud", Name: "Mixed", Revision: 1, Members: []cloudinfra.PoolMember{{ServerID: "server-1", CPUMillis: 2000, MemoryBytes: 2 << 30}, {ServerID: "server-2", CPUMillis: 2000, MemoryBytes: 2 << 30}}}
	if err := st.SavePool(t.Context(), pool, 0); err != nil {
		t.Fatal(err)
	}
	if err := clouds.CreateTarget(t.Context(), &cloudprofiles.Target{ID: "pool-target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "shared", DisplayName: "Shared", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1),('org','proj','stage','cloud',1)`, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('second','org','Second','second','ready','usr',now(),now())`, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('second-env','org','second','production','production',now(),now())`, `INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id) VALUES('org','cloud','second')`, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','second','second-env','cloud',1)`} {
		if _, err := sql.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	for _, project := range []string{"proj", "second"} {
		if _, err := sql.Exec(t.Context(), `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('pool-manifest-'||$1,'org',$1,1,'version: 1','pool','usr',now())`, project); err != nil {
			t.Fatal(err)
		}
	}
	return &poolFixture{store: st, plans: planning.NewPgStore(db), clouds: clouds, sql: sql, db: db, pool: pool}
}
func (f *poolFixture) request(env, name string) cloudinfra.PlacementRequest {
	project := "proj"
	if env == "second-env" {
		project = "second"
	}
	return cloudinfra.PlacementRequest{OrgID: "org", ProjectID: project, EnvironmentID: env, CloudID: "cloud", TargetID: "pool-target", TargetRevision: 1, PoolID: "pool", Service: name, CPUMillis: 1000, MemoryBytes: 512 << 20, Replicas: 1, Architecture: "amd64"}
}
func (f *poolFixture) plan(t *testing.T, id string, r cloudinfra.PlacementRequest, p *cloudinfra.Placement) *planning.CloudPlacement {
	t.Helper()
	pin := &planning.CloudPlacement{Allocation: p.Allocation, CloudID: "cloud", CloudRevision: 1, GrantRevision: 1, AssignmentRevision: 1, TargetID: "pool-target", TargetRevision: 1, TargetName: "shared", Provider: "server", ConnectionID: p.ConnectionID, AccountIdentity: connections.AccountIdentity("server", p.ConnectionID, map[string]string{"docker_id": p.ConnectionID, "architecture": "amd64"}), PoolID: p.PoolID, PoolRevision: p.PoolRevision, ServerID: p.ServerID, ServerRevision: p.ServerRevision, ConnectionGeneration: p.ConnectionGeneration, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes, GPUKind: p.GPUKind, GPUs: p.GPUs}
	size := &manifest.Size{CPU: float64(r.CPUMillis) / 1000, Memory: int(r.MemoryBytes >> 20)}
	if r.GPUs > 0 {
		size.GPU = &manifest.GPU{Kind: r.GPUKind, Count: int(r.GPUs)}
	}
	result := planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:" + r.Service, Subject: planning.ServiceSubject, Name: r.Service, Op: planning.Create, Provider: "server", ConnectionID: p.ConnectionID, Service: &planning.ServiceConfig{Provider: "server", Target: p.ConnectionID, TargetSettings: map[string]string{"docker_id": p.ConnectionID, "architecture": "amd64"}, Size: size, CloudPlacement: pin}}}}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org',$2,$3,'production','ready','review','pool-manifest-'||$2,1,$4,true,'usr',now())`, id, r.ProjectID, r.EnvironmentID, raw)
	if err != nil {
		t.Fatal(err)
	}
	return pin
}
func (f *poolFixture) accept(t *testing.T, id string) {
	t.Helper()
	ok, err := f.plans.Decide(t.Context(), "org", id, "approved", "usr", "", time.Now())
	if err != nil || !ok {
		t.Fatalf("accept %s: %v %v", id, ok, err)
	}
}
func TestServerPoolsCapacityStickyAndCleanup(t *testing.T) {
	f := newPoolFixture(t)
	for _, tc := range []struct{ id, env, name, host string }{{"p1", "env", "api", "server-1"}, {"p2", "second-env", "worker", "server-1"}, {"p3", "stage", "jobs", "server-2"}} {
		r := f.request(tc.env, tc.name)
		p, err := f.store.PreviewPlacement(t.Context(), r)
		if err != nil || p.ServerID != tc.host {
			t.Fatalf("placement: %+v %v", p, err)
		}
		pin := f.plan(t, tc.id, r, p)
		f.accept(t, tc.id)
		if err = f.store.ValidatePlacement(t.Context(), "org", r.ProjectID, r.EnvironmentID, tc.id, r.Service, *p); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(planning.ServiceConfig{Provider: "server", Target: p.ConnectionID, CloudPlacement: pin})
		b := &planning.Binding{ID: "binding-" + tc.id, OrgID: "org", ProjectID: r.ProjectID, EnvironmentID: r.EnvironmentID, Subject: planning.ServiceSubject, Name: r.Service, Provider: "server", ConnectionID: p.ConnectionID, Config: raw, PlanID: tc.id, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err = f.plans.PutBinding(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	r := f.request("env", "api")
	r.StickyServerID = "server-1"
	p, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil || p.ServerID != "server-1" {
		t.Fatalf("sticky %+v %v", p, err)
	}
	v, err := f.store.GetServer(t.Context(), "org", "cloud", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.RetireServer(t.Context(), v); err == nil {
		t.Fatal("retired reserved server")
	}
	if err = f.plans.DeleteBinding(t.Context(), "org", "binding-p1"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations WHERE plan_id='p1'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cleanup %d %v", count, err)
	}
}
func TestServerPoolsConcurrentAcceptance(t *testing.T) {
	f := newPoolFixture(t)
	ids := []string{"first", "second"}
	for i, env := range []string{"env", "second-env"} {
		r := f.request(env, "api")
		r.CPUMillis = 2000
		p, err := f.store.PreviewPlacement(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		f.plan(t, ids[i], r, p)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range ids {
		wg.Go(func() {
			_, err := f.plans.Decide(t.Context(), "org", id, "approved", "usr", "", time.Now())
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d competing full-host plans", success)
	}
	var n int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reservations %d %v", n, err)
	}
}
func TestServerPoolsStaleIncompatibleAndConstrained(t *testing.T) {
	f := newPoolFixture(t)
	r := f.request("env", "api")
	r.Architecture = "arm64"
	if _, err := f.store.PreviewPlacement(t.Context(), r); err == nil {
		t.Fatal("incompatible architecture")
	}
	r.Architecture = "amd64"
	r.Replicas = 2
	if _, err := f.store.PreviewPlacement(t.Context(), r); err == nil {
		t.Fatal("replicas")
	}
	r.Replicas = 1
	p, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "stale", r, p)
	f.pool.Revision++
	f.pool.Name = "Renamed"
	if err = f.store.SavePool(t.Context(), f.pool, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plans.Decide(t.Context(), "org", "stale", "approved", "usr", "", time.Now()); err == nil {
		t.Fatal("stale pool accepted")
	}
	f.pool.Revision++
	f.pool.Members[0].CPUMillis = 4000
	if err = f.store.SavePool(t.Context(), f.pool, 2); err == nil {
		t.Fatal("outer Docker capacity overrode constrained effective budget")
	}
	v, err := f.store.GetServer(t.Context(), "org", "cloud", "server-2")
	if err != nil {
		t.Fatal(err)
	}
	v.ID = "duplicate"
	v.StateRevision = 0
	v.RuntimeConnectionID = "runtime-1"
	if err = f.store.SaveServer(t.Context(), v); err == nil {
		t.Fatal("duplicate Docker host counted twice")
	}
}

type poolExecutor struct {
	fail  bool
	calls int
}

func (e *poolExecutor) Apply(_ context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	e.calls++
	if err := j.Checkpoint([]byte(`{"attempted":true}`)); err != nil {
		return nil, err
	}
	if e.fail {
		return nil, errors.New("ambiguous runtime outcome")
	}
	return &deployments.Outcome{ExternalID: j.Connection.ID + "-" + j.Action.Name, Revision: "image-digest"}, nil
}
func (*poolExecutor) Remove(context.Context, *deployments.Job, *planning.Binding) error { return nil }
func (*poolExecutor) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*deployments.Observation, error) {
	return &deployments.Observation{Exists: true}, nil
}
func TestServerPoolsPersistedRunnerAndAmbiguousFailure(t *testing.T) {
	f := newPoolFixture(t)
	r := f.request("env", "api")
	p, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "runner", r, p)
	f.accept(t, "runner")
	executor := &poolExecutor{fail: true}
	accounts := &syncAccounts{store: connections.NewPgStore(f.db)}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: accounts, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	id, _, err := runner.Start(t.Context(), "org", "runner")
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Run(t.Context(), "org", id, "service:api", nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("fixture failure disappeared")
	}
	if executor.calls != 1 {
		t.Fatalf("runtime pin validation did not reach executor: %d, %v", executor.calls, err)
	}
	if err = f.store.ValidatePlacement(t.Context(), "org", "proj", "env", "runner", "api", *p); err != nil {
		t.Fatalf("ambiguous runtime lost reservation: %v", err)
	}
	executor.fail = false
	if err = runner.Run(t.Context(), "org", id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	bindings, err := f.plans.ListBindings(t.Context(), "org", "env")
	if err != nil || len(bindings) != 1 {
		t.Fatalf("applied binding: %v %v", bindings, err)
	}
	r.StickyServerID = p.ServerID
	r.CPUMillis = 2000
	next, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "next-release", r, next)
	f.accept(t, "next-release")
	r.CPUMillis = 1000
	r.PlanID = "next-release"
	different, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil || different.ServerID != p.ServerID {
		t.Fatalf("future release moved: %+v %v", different, err)
	}
	accounts.missing = p.ConnectionID
	if _, _, err = runner.Start(t.Context(), "org", "runner"); err == nil {
		t.Fatal("missing runtime credential accepted")
	}
}
func TestServerPoolsDirectBypassGrantAndGlobalDrain(t *testing.T) {
	f := newPoolFixture(t)
	r := f.request("env", "api")
	p, err := f.store.PreviewPlacement(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "bypass", r, p)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,cloud_placement}','null') WHERE id='bypass'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plans.Decide(t.Context(), "org", "bypass", "approved", "usr", "", time.Now()); err == nil {
		t.Fatal("direct path bypassed pool capacity")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET status='rejected' WHERE id='bypass'`); err != nil {
		t.Fatal(err)
	}
	f.plan(t, "revoked", r, p)
	if _, err = f.clouds.SetProjectGrant(t.Context(), "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plans.Decide(t.Context(), "org", "revoked", "approved", "usr", "", time.Now()); err == nil {
		t.Fatal("revoked grant accepted")
	}
	if _, err = f.store.PreviewPlacement(t.Context(), r); err == nil {
		t.Fatal("revoked project preview leaked capacity")
	}
	if _, err = f.clouds.SetProjectGrant(t.Context(), "org", "cloud", "proj", nil, false); err != nil {
		t.Fatal(err)
	}
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &syncAuth{}})
	actor := security.Principal{OrgID: "org", UserID: "manager"}
	review, err := svc.PreviewDrain(t.Context(), actor, "cloud", p.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyDrain(t.Context(), actor, "cloud", p.ServerID, cloudinfra.ApplyInput{Review: review.Review}); err != nil {
		t.Fatal(err)
	}
	second := *f.pool
	second.ID = "second-pool"
	if err = f.store.SavePool(t.Context(), &second, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = f.clouds.CreateTarget(t.Context(), &cloudprofiles.Target{ID: "second-target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "second", DisplayName: "Second", Provider: "server", Runtime: "server", PoolID: second.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	r.TargetID = "second-target"
	r.PoolID = second.ID
	p, err = f.store.PreviewPlacement(t.Context(), r)
	if err != nil || p.ServerID != "server-2" {
		t.Fatalf("other pool bypassed drain: %+v %v", p, err)
	}
}

func TestServerPoolsReviewedMoveAndExactReplacementCleanup(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	r := f.request("env", "api")
	p, err := f.store.PreviewPlacement(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.plan(t, "original", r, p)
	f.accept(t, "original")
	cfg := planning.ServiceConfig{Provider: "server", Target: p.ConnectionID, TargetSettings: map[string]string{"docker_id": p.ConnectionID, "architecture": "amd64"}, CloudPlacement: pin, Size: &manifest.Size{CPU: 1, Memory: 512}}
	raw, _ := json.Marshal(cfg)
	old := planning.Binding{ID: "live-binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: p.ConnectionID, Config: raw, PlanID: "original", ExternalID: "original-runtime", Revision: "old-image", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = f.plans.PutBinding(ctx, &old); err != nil {
		t.Fatal(err)
	}
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &syncAuth{}})
	actor := security.Principal{OrgID: "org", UserID: "manager"}
	drain, err := svc.PreviewDrain(ctx, actor, "cloud", p.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyDrain(ctx, actor, "cloud", p.ServerID, cloudinfra.ApplyInput{Review: drain.Review}); err != nil {
		t.Fatal(err)
	}
	r.StickyServerID = p.ServerID
	sticky, err := f.store.PreviewPlacement(ctx, r)
	if err != nil || sticky.ServerID != p.ServerID {
		t.Fatalf("drain silently moved service: %+v %v", sticky, err)
	}
	in := cloudinfra.MoveInput{ProjectID: "proj", EnvironmentID: "env", Service: "api", PoolID: "pool", DestinationServerID: "server-2"}
	review, err := svc.PreviewMove(ctx, actor, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	in.Review = review.Review
	if _, err = svc.AcceptMove(ctx, actor, "cloud", in); err != nil {
		t.Fatal(err)
	}
	destination, err := f.store.PreviewPlacement(ctx, r)
	if err != nil || destination.ServerID != "server-2" {
		t.Fatalf("explicit destination %+v %v", destination, err)
	}
	nextPin := f.plan(t, "move", r, destination)
	f.accept(t, "move")
	assertClaims := func(want int) {
		t.Helper()
		var n int
		if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_capacity_reservations WHERE environment_id='env' AND service='api'`).Scan(&n); err != nil || n != want {
			t.Fatalf("claims=%d want=%d: %v", n, want, err)
		}
	}
	assertClaims(2)
	next := old
	next.PlanID = "move"
	next.ConnectionID = destination.ConnectionID
	next.ExternalID = "destination-runtime"
	next.Replaced = []planning.Binding{old}
	cfg.Target = destination.ConnectionID
	cfg.CloudPlacement = nextPin
	next.Config, _ = json.Marshal(cfg)
	if err = f.plans.PutBinding(ctx, &next); err != nil {
		t.Fatal(err)
	}
	assertClaims(2)
	if err = f.plans.SetReplaced(ctx, "org", next.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertClaims(1)
	var host string
	if err = f.sql.QueryRow(ctx, `SELECT server_id FROM loomplane_capacity_reservations WHERE environment_id='env' AND service='api'`).Scan(&host); err != nil || host != "server-2" {
		t.Fatalf("released destination: %s %v", host, err)
	}
}
func TestServerPoolsPreviewAccountsForWholePlan(t *testing.T) {
	f := newPoolFixture(t)
	selected := make([]cloudinfra.PlannedPlacement, 0, 4)
	for i := range 4 {
		r := f.request("env", fmt.Sprintf("service-%d", i))
		r.Planned = selected
		p, err := f.store.PreviewPlacement(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		want := "server-1"
		if i >= 2 {
			want = "server-2"
		}
		if p.ServerID != want {
			t.Fatalf("service %d host %s want %s", i, p.ServerID, want)
		}
		selected = append(selected, cloudinfra.PlannedPlacement{Service: r.Service, Placement: *p})
	}
	r := f.request("env", "overflow")
	r.Planned = selected
	if _, err := f.store.PreviewPlacement(t.Context(), r); err == nil {
		t.Fatal("preview overcommitted host")
	}
}

func TestServerPoolsCleanupRetainsMissingConnectionAndRevokedGrant(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	r := f.request("env", "api")
	p, err := f.store.PreviewPlacement(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.plan(t, "applied", r, p)
	f.accept(t, "applied")
	raw, _ := json.Marshal(planning.ServiceConfig{Provider: "server", Target: p.ConnectionID, CloudPlacement: pin})
	bound := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: p.ConnectionID, Config: raw, PlanID: "applied", ExternalID: "runtime-instance", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = f.plans.PutBinding(ctx, bound); err != nil {
		t.Fatal(err)
	}
	f.plan(t, "remove", r, p)
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_plans SET result=jsonb_set(jsonb_set(result,'{actions,0,op}','"delete"'),'{actions,0,service}','null') WHERE id='remove'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.clouds.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	f.accept(t, "remove")
	accounts := &syncAccounts{store: connections.NewPgStore(f.db), missing: p.ConnectionID}
	executor := &poolExecutor{}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: accounts, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	id, _, err := runner.Start(ctx, "org", "remove")
	if err != nil || id == "" {
		t.Fatalf("retained cleanup start: %s %v", id, err)
	}
	if err = runner.Run(ctx, "org", id, "service:api", nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("missing connection discarded binding")
	}
	bindings, err := f.plans.ListBindings(ctx, "org", "env")
	if err != nil || len(bindings) != 1 {
		t.Fatalf("cleanup lost binding: %+v %v", bindings, err)
	}
	if err = f.store.ValidatePlacement(ctx, "org", "proj", "env", "applied", "api", *p); err != nil {
		t.Fatal("cleanup lost reservation", err)
	}
	accounts.missing = ""
	if err = runner.Run(ctx, "org", id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	bindings, err = f.plans.ListBindings(ctx, "org", "env")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("confirmed cleanup retained binding: %+v %v", bindings, err)
	}
	var count int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_capacity_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("confirmed cleanup claims: %d %v", count, err)
	}
}

type poolVars struct{}

func (poolVars) Values(context.Context, string, string, string, string) (map[string]string, map[string]bool, error) {
	return map[string]string{}, map[string]bool{}, nil
}
func TestServerPoolsRetiredHistoryAllowsConnectionReuse(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	r := f.request("env", "api")
	p, err := f.store.PreviewPlacement(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.store.GetServer(ctx, "org", "cloud", p.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.RetireServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	v.Status = "disconnected"
	if err = f.store.SaveServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	f.plan(t, "legacy-reuse", r, p)
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,cloud_placement}','null') WHERE id='legacy-reuse'`); err != nil {
		t.Fatal(err)
	}
	f.accept(t, "legacy-reuse")
}

func (*poolExecutor) PreparePoolEffects(context.Context, *deployments.Job) error { return nil }

func directPoolPlan(t *testing.T, f *poolFixture, id, connection string) {
	t.Helper()
	f.plan(t, id, f.request("env", "api"), &cloudinfra.Placement{ConnectionID: "runtime-1"})
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(jsonb_set(jsonb_set(result,'{actions,0,service,cloud_placement}','null'),'{actions,0,connection_id}',to_jsonb($2::text)),'{actions,0,service,target}',to_jsonb($2::text)) WHERE id=$1`, id, connection); err != nil {
		t.Fatal(err)
	}
}
func poolTestRunner(f *poolFixture, executor deployments.Executor) *deployments.Runner {
	return deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
}
func TestServerPoolsVerifiedAliasAdmission(t *testing.T) {
	f := newPoolFixture(t)
	now := time.Now()
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), &connections.Connection{ID: "alias", OrgID: "org", Cloud: "server", Name: "alias", Account: "runtime-1", Details: map[string]string{"docker_id": "runtime-1", "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	aliases, err := f.plans.PooledConnections(t.Context(), "org")
	if err != nil || !aliases["alias"] {
		t.Fatal("unadopted alias missing from pool admission", aliases, err)
	}
	directPoolPlan(t, f, "alias-plan", "alias")
	if _, err = f.plans.Decide(t.Context(), "org", "alias-plan", "approved", "usr", "", now); err == nil {
		t.Fatal("alias approval bypassed capacity")
	}
	// Historical approval, before pool enrollment, still cannot execute on a
	// currently pooled daemon through its unadopted connection alias.
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members`); err != nil {
		t.Fatal(err)
	}
	f.accept(t, "alias-plan")
	executor := &poolExecutor{}
	runner := poolTestRunner(f, executor)
	dep, _, err := runner.Start(t.Context(), "org", "alias-plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_server_pool_members(org_id,pool_id,server_id,cpu_millis,memory_bytes) VALUES('org','pool','server-1',2000,2147483648)`); err != nil {
		t.Fatal(err)
	}
	if err = runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil }); err == nil || executor.calls != 0 {
		t.Fatal("alias runtime bypassed pool", err)
	}
}

func waitPoolSQL(t *testing.T, f *poolFixture, query string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		if err := f.db.QueryRow(t.Context(), query).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("database barrier was not reached")
}
func TestServerPoolsEnrollmentSerializesApproval(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members`); err != nil {
		t.Fatal(err)
	}
	directPoolPlan(t, f, "direct", "runtime-1")
	barrier, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(context.Background()) }()
	if _, err = barrier.Exec(t.Context(), `SELECT id FROM loomplane_cloud_servers WHERE id='server-1' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	enrolled := make(chan error, 1)
	next := *f.pool
	next.Revision++
	go func() { enrolled <- f.store.SavePool(t.Context(), &next, 1) }()
	waitPoolSQL(t, f, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT id FROM loomplane_cloud_servers%')`)
	approved := make(chan error, 1)
	go func() {
		_, e := f.plans.Decide(t.Context(), "org", "direct", "approved", "usr", "", time.Now())
		approved <- e
	}()
	select {
	case e := <-approved:
		t.Fatal("approval passed uncommitted enrollment", e)
	case <-time.After(100 * time.Millisecond):
	}
	if err = barrier.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-enrolled; err != nil {
		t.Fatal(err)
	}
	if err = <-approved; err == nil {
		t.Fatal("direct approval escaped committed enrollment")
	}
}

type blockingDirectPoolExecutor struct {
	poolExecutor
	entered, release chan struct{}
}

// Apply also gives up when the test ends, so a failed test rolls back the
// admission it holds instead of hanging its cleanup.
func (e *blockingDirectPoolExecutor) Apply(ctx context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	close(e.entered)
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.poolExecutor.Apply(ctx, j)
}
func TestServerPoolsEnrollmentSerializesDirectExecution(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members`); err != nil {
		t.Fatal(err)
	}
	directPoolPlan(t, f, "direct", "runtime-1")
	f.accept(t, "direct")
	executor := &blockingDirectPoolExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	runner := poolTestRunner(f, executor)
	dep, _, err := runner.Start(t.Context(), "org", "direct")
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan error, 1)
	go func() {
		ran <- runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil })
	}()
	select {
	case <-executor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary unclaimed host did not execute")
	}
	// Enrollment refuses while the direct apply runs. Waiting would hold the
	// organization lock the apply needs for its binding.
	enrolled := make(chan error, 1)
	next := *f.pool
	next.Revision++
	go func() { enrolled <- f.store.SavePool(t.Context(), &next, 1) }()
	select {
	case err = <-enrolled:
		if !errors.Is(err, cloudinfra.ErrBusy) {
			close(executor.release)
			t.Fatalf("enrollment beside a running direct apply: %v", err)
		}
	case <-time.After(15 * time.Second):
		close(executor.release)
		t.Fatalf("enrollment waited on a running direct apply\n%s", lockReport(f))
	}
	close(executor.release)
	select {
	case err = <-ran:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("direct apply never finished\n%s", lockReport(f))
	}
	if err = f.store.SavePool(t.Context(), &next, 1); err == nil {
		t.Fatal("enrollment ignored direct effects and binding")
	}
}

func TestServerPoolsOrdinaryApprovalStillNeedsConcretePlacement(t *testing.T) {
	f := newPoolFixture(t)
	req := f.request("env", "api")
	candidate, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	candidate.ServerID = ""
	f.plan(t, "missing-host", req, candidate)
	if accepted, approveErr := f.plans.Decide(t.Context(), "org", "missing-host", "approved", "usr", "", time.Now()); approveErr == nil || accepted {
		t.Fatal("ordinary approval omitted its concrete host reservation")
	}
}

func TestServerPoolsGPUMembers(t *testing.T) {
	f := newPoolFixture(t)
	now := time.Now().UTC()
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), &connections.Connection{ID: "runtime-gpu", OrgID: "org", Cloud: "server", Name: "runtime-gpu", Account: "runtime-gpu", Details: map[string]string{"docker_id": "runtime-gpu", "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	server := &cloudinfra.Server{ID: "server-gpu", OrgID: "org", CloudID: "cloud", ConnectionID: "runtime-gpu", RuntimeConnectionID: "runtime-gpu", Ownership: "adopted", Provider: "generic", AccountID: "runtime-gpu", Status: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now, Health: cloudinfra.RuntimeHealth{DockerID: "runtime-gpu", Architecture: "amd64", CPUs: 16, MemoryBytes: 64 << 30, EffectiveCPUMillis: 16000, EffectiveMemoryBytes: 64 << 30, GPURuntime: "ready", GPUs: []provider.GPUDevice{{Index: 0, UUID: "GPU-0", Kind: "nvidia-l4"}, {Index: 1, UUID: "GPU-1", Kind: "nvidia-l4"}}, CheckedAt: now}}
	if err := f.store.SaveServer(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	member := cloudinfra.PoolMember{ServerID: "server-gpu", CPUMillis: 8000, MemoryBytes: 32 << 30, GPUs: 3}
	gpuPool := &cloudinfra.Pool{ID: "gpu-pool", OrgID: "org", CloudID: "cloud", Name: "L4", Revision: 1, AcceleratorKind: "nvidia-l4", Members: []cloudinfra.PoolMember{member}}
	if err := f.store.SavePool(t.Context(), gpuPool, 0); !errors.Is(err, cloudinfra.ErrInvalid) {
		t.Fatalf("three L4s on a two-L4 server: %v", err)
	}
	gpuPool.Members[0].GPUs = 2
	if err := f.store.SavePool(t.Context(), gpuPool, 0); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.GetPool(t.Context(), "org", "cloud", "gpu-pool")
	if err != nil || saved.AcceleratorKind != "nvidia-l4" || saved.Members[0].GPUs != 2 {
		t.Fatalf("saved GPU pool: %+v %v", saved, err)
	}
	// The same server can't give a different GPU budget to another pool, GPU or CPU.
	other := &cloudinfra.Pool{ID: "gpu-pool-2", OrgID: "org", CloudID: "cloud", Name: "L4 half", Revision: 1, AcceleratorKind: "nvidia-l4", Members: []cloudinfra.PoolMember{{ServerID: "server-gpu", CPUMillis: 8000, MemoryBytes: 32 << 30, GPUs: 1}}}
	if err = f.store.SavePool(t.Context(), other, 0); err == nil || !strings.Contains(err.Error(), "same budget in every pool") {
		t.Fatalf("second GPU budget: %v", err)
	}
	cpu := &cloudinfra.Pool{ID: "cpu-pool", OrgID: "org", CloudID: "cloud", Name: "CPU", Revision: 1, Members: []cloudinfra.PoolMember{{ServerID: "server-gpu", CPUMillis: 8000, MemoryBytes: 32 << 30}}}
	if err = f.store.SavePool(t.Context(), cpu, 0); err == nil || !strings.Contains(err.Error(), "same budget in every pool") {
		t.Fatalf("GPU server in a CPU pool with no GPU budget: %v", err)
	}
	// A server whose container runtime can't use its GPUs can't join a GPU pool.
	server.Health.GPURuntime = "toolkit_missing"
	server.Revision = 2
	if err = f.store.SaveServer(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	gpuPool.Revision = 2
	if err = f.store.SavePool(t.Context(), gpuPool, 1); !errors.Is(err, cloudinfra.ErrConflict) || !strings.Contains(err.Error(), "GPU runtime") {
		t.Fatalf("toolkit missing: %v", err)
	}
}
