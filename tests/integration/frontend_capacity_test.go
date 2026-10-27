//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type frontendFixture struct {
	db    *pgdriver.PgDB
	store *cloudinfra.PgStore
	plans *planning.PgStore
}

func newFrontendFixture(t *testing.T, maximum *int64) *frontendFixture {
	t.Helper()
	_, sql, db := cloudSyncDatabase(t)
	for _, stmt := range []string{
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('preview','org','proj','preview','preview',now(),now())`,
	} {
		if _, err := sql.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	f := &frontendFixture{db: db, store: cloudinfra.NewPgStore(db), plans: planning.NewPgStore(db)}
	if err := f.store.SaveCapacityPolicy(t.Context(), &cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, FrontendLimits: []cloudinfra.FrontendLimit{{TargetID: "target", Maximum: maximum}}}, 0); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *frontendFixture) plan(t *testing.T, id, env, service string) cloudinfra.FrontendDemand {
	t.Helper()
	ctx := t.Context()
	d, err := f.store.ResolveFrontendDemand(ctx, cloudinfra.FrontendDemand{OrgID: "org", ProjectID: "proj", EnvironmentID: env, PlanID: id, Service: service, Provider: "vercel", ConnectionID: "old"})
	if err != nil || d == nil {
		t.Fatalf("demand: %+v %v", d, err)
	}
	cfg := &planning.ServiceConfig{Provider: "vercel", Target: "old", Workload: provider.WebApp, Execution: provider.Serverless, FrontendDemand: d}
	result := planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:" + service, Name: service, Subject: planning.ServiceSubject, Op: planning.Create, Provider: "vercel", ConnectionID: "old", Service: cfg}}}
	raw, _ := json.Marshal(result)
	if _, err = f.db.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org','proj',$2,'production','ready','review','manifest',1,$3,true,'usr',now())`, id, env, raw); err != nil {
		t.Fatal(err)
	}
	if ok, e := f.plans.Decide(security.WithPrincipal(ctx, security.Principal{OrgID: "org", UserID: "usr"}), "org", id, "approved", "usr", "", time.Now()); e != nil || !ok {
		t.Fatalf("approval: %v %v", ok, e)
	}
	return *d
}
func (f *frontendFixture) reserve(ctx context.Context, d cloudinfra.FrontendDemand) (*cloudinfra.CapacityBlock, error) {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	b, err := f.store.ReserveFrontendSlot(ctx, tx, d)
	if err != nil || b != nil {
		return b, err
	}
	return nil, tx.Commit()
}
func frontendTx(t *testing.T, f *frontendFixture, fn func(driver.Tx) error) {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = fn(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func TestFrontendCapacity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		maximum       *int64
		first, second bool
	}{{"one", frontendMax(1), true, false}, {"zero", frontendMax(0), false, false}, {"unlimited", nil, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFrontendFixture(t, tc.maximum)
			a := f.plan(t, "first", "env", "web")
			b := f.plan(t, "second", "env", "other")
			for i, d := range []cloudinfra.FrontendDemand{a, b} {
				block, err := f.reserve(t.Context(), d)
				want := tc.first
				if i == 1 {
					want = tc.second
				}
				if err != nil || (block == nil) != want {
					t.Fatalf("reserve %d: %+v %v", i, block, err)
				}
			}
			if tc.first {
				for i := range 10 {
					d := f.plan(t, fmt.Sprintf("revision-%d", i), "env", "web")
					if block, err := f.reserve(t.Context(), d); err != nil || block != nil {
						t.Fatalf("revision: %+v %v", block, err)
					}
				}
			}
			if tc.maximum != nil && *tc.maximum == 1 {
				d := f.plan(t, "preview-plan", "preview", "web")
				if block, err := f.reserve(t.Context(), d); err != nil || block == nil {
					t.Fatalf("preview reused serving slot: %+v %v", block, err)
				}
			}
		})
	}
}
func frontendMax(n int64) *int64 { return &n }
func TestFrontendCapacityConcurrentFirstDeploys(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(1))
	a := f.plan(t, "a", "env", "web")
	b := f.plan(t, "b", "stage", "web")
	start := make(chan struct{})
	results := make(chan *cloudinfra.CapacityBlock, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, d := range []cloudinfra.FrontendDemand{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			block, err := f.reserve(t.Context(), d)
			results <- block
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	admitted := 0
	for b := range results {
		if b == nil {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d", admitted)
	}
}

func TestFrontendCapacityCancellationAndUncertainEffects(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			f := newFrontendFixture(t, frontendMax(1))
			d := f.plan(t, "first", "env", "web")
			if block, err := f.reserve(t.Context(), d); err != nil || block != nil {
				t.Fatalf("reserve: %+v %v", block, err)
			}
			attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: "first", Deployment: "deployment"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if accepted {
				if err = f.store.MarkWorkloadEffects(t.Context(), *attempt); err != nil {
					t.Fatal(err)
				}
			}
			frontendTx(t, f, func(tx driver.Tx) error {
				return cloudinfra.CancelUnstartedCapacity(t.Context(), tx, "org", "first", "deployment", "env")
			})
			other := f.plan(t, "second", "stage", "other")
			block, err := f.reserve(t.Context(), other)
			if err != nil || (block != nil) != accepted {
				t.Fatalf("accepted=%v after cancellation: %+v %v", accepted, block, err)
			}
			if !accepted {
				if err = f.store.MarkWorkloadEffects(t.Context(), *attempt); err == nil {
					t.Fatal("cancelled attempt accepted effects")
				}
			}
		})
	}
}

func TestFrontendCapacityBindingRetirementAndMove(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(1))
	ctx := t.Context()
	source := f.plan(t, "source", "env", "web")
	if b, err := f.reserve(ctx, source); err != nil || b != nil {
		t.Fatalf("reserve: %+v %v", b, err)
	}
	cfg, _ := json.Marshal(planning.ServiceConfig{Provider: "vercel", Workload: provider.WebApp, Execution: provider.Serverless, FrontendDemand: &source, CloudPlacement: &planning.CloudPlacement{CloudID: "cloud", TargetID: "target", AccountIdentity: source.AccountID}})
	binding := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ConnectionID: "old", ExternalID: "project-old", PlanID: "source", Config: cfg, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.plans.PutBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	usage, err := f.store.FrontendUsage(ctx, "org", "cloud")
	if err != nil || len(usage) != 1 || usage[0].Counted != 1 || usage[0].Active != 1 || usage[0].Pending != 0 {
		t.Fatalf("active usage: %+v %v", usage, err)
	}
	// Forgetting a binding is never provider absence.
	if err = f.plans.DeleteBinding(ctx, "org", "binding"); err != nil {
		t.Fatal(err)
	}
	usage, err = f.store.FrontendUsage(ctx, "org", "cloud")
	if err != nil || usage[0].Counted != 1 || usage[0].Retained != 1 {
		t.Fatalf("retained usage: %+v %v", usage, err)
	}
	other := f.plan(t, "other", "stage", "other")
	if b, reserveErr := f.reserve(ctx, other); reserveErr != nil || b == nil {
		t.Fatalf("forgotten binding freed capacity: %+v %v", b, reserveErr)
	}
	// A target connection change keeps the existing logical slot and its source use.
	if _, err = f.db.Exec(ctx, `UPDATE loomplane_cloud_targets SET connection_id='new',revision=revision+1 WHERE id='target'`); err != nil {
		t.Fatal(err)
	}
	dest := source
	dest.PlanID = "destination"
	dest.ConnectionID = "new"
	dest.AccountID = ""
	dest.TargetRevision = 0
	dest.ConnectionGeneration = 0
	dest.GrantRevision = 0
	resolved, err := f.store.ResolveFrontendDemand(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	dest = *resolved
	result := planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:web", Name: "web", Subject: planning.ServiceSubject, Op: planning.Create, Provider: "vercel", ConnectionID: "new", Service: &planning.ServiceConfig{Provider: "vercel", Workload: provider.WebApp, Execution: provider.Serverless, FrontendDemand: &dest}}}}
	raw, _ := json.Marshal(result)
	if _, err = f.db.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES('destination','org','proj','env','production','ready','move','manifest',1,$1,true,'usr',now())`, raw); err != nil {
		t.Fatal(err)
	}
	if ok, e := f.plans.Decide(security.WithPrincipal(ctx, security.Principal{OrgID: "org", UserID: "usr"}), "org", "destination", "approved", "usr", "", time.Now()); e != nil || !ok {
		t.Fatalf("approve: %v %v", ok, e)
	}
	if b, e := f.reserve(ctx, dest); e != nil || b != nil {
		t.Fatalf("replacement blocked at ceiling: %+v %v", b, e)
	}
	// Exact absence of the old resource cannot release the destination reservation.
	evidence := cloudinfra.FrontendAbsence{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Service: "web", Provider: "vercel", ConnectionID: "old", AccountID: source.AccountID, ExternalID: "wrong-project", PlanID: "source"}
	if err = f.plans.RecordFrontendAbsence(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = f.db.QueryRow(ctx, `SELECT status FROM loomplane_frontend_uses WHERE plan_id='source'`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("wrong project released use: %s %v", status, err)
	}
	evidence.ExternalID = "project-old"
	if err = f.plans.RecordFrontendAbsence(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(ctx, `SELECT status FROM loomplane_frontend_slots`).Scan(&status); err != nil || status == "released" {
		t.Fatalf("source cleanup released destination: %s %v", status, err)
	}
	frontendTx(t, f, func(tx driver.Tx) error {
		return cloudinfra.CancelUnstartedCapacity(ctx, tx, "org", "destination", "deployment", "env")
	})
	if err = f.db.QueryRow(ctx, `SELECT status FROM loomplane_frontend_slots`).Scan(&status); err != nil || status != "released" {
		t.Fatalf("retired source and unused destination: %s %v", status, err)
	}
}

func TestFrontendCapacityGovernance(t *testing.T) {
	f := newFrontendFixture(t, nil)
	ctx := t.Context()
	base := cloudinfra.FrontendDemand{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", PlanID: "plan", Service: "web", Provider: "vercel", ConnectionID: "old"}
	if d, err := f.store.ResolveFrontendDemand(ctx, base); err != nil || d == nil || d.TargetID != "target" {
		t.Fatalf("explicit: %+v %v", d, err)
	}
	base.ConnectionID = "new"
	if d, err := f.store.ResolveFrontendDemand(ctx, base); err != nil || d != nil {
		t.Fatalf("direct: %+v %v", d, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE loomplane_cloud_connections SET details='{"team_id":"old"}' WHERE id='new'`); err != nil {
		t.Fatal(err)
	}
	if d, err := f.store.ResolveFrontendDemand(ctx, base); err != nil || d == nil || d.TargetID != "target" {
		t.Fatalf("account alias: %+v %v", d, err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO loomplane_cloud_targets(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,revision,created_at,updated_at,runtime) VALUES('other-target','org','proj','cloud','other','Other','vercel','old',1,now(),now(),'managed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ResolveFrontendDemand(ctx, base); err == nil {
		t.Fatal("ambiguous explicit account bypassed governance")
	}
	base.TargetID = "target"
	if d, err := f.store.ResolveFrontendDemand(ctx, base); err != nil || d == nil {
		t.Fatalf("explicit resolution: %+v %v", d, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ResolveFrontendDemand(ctx, base); err == nil {
		t.Fatal("revoked grant bypassed governance")
	}
}

func TestFrontendCapacityPolicyBackfill(t *testing.T) {
	f := newFrontendFixture(t, nil)
	ctx := t.Context()
	d := f.plan(t, "serving", "env", "web")
	cfg, _ := json.Marshal(planning.ServiceConfig{Provider: "vercel", Workload: provider.WebApp, Execution: provider.Serverless})
	binding := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ConnectionID: "old", ExternalID: "existing-project", PlanID: d.PlanID, Config: cfg, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.plans.PutBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	policy := &cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 2, FrontendLimits: []cloudinfra.FrontendLimit{{TargetID: "target", Maximum: frontendMax(1)}}}
	if err := f.store.SaveCapacityPolicy(ctx, policy, 1); err != nil {
		t.Fatal(err)
	}
	usage, err := f.store.FrontendUsage(ctx, "org", "cloud")
	if err != nil || usage[0].Counted != 1 || usage[0].Active != 1 {
		t.Fatalf("retained usage: %+v %v", usage, err)
	}
	other := f.plan(t, "other", "stage", "other")
	if b, err := f.reserve(ctx, other); err != nil || b == nil {
		t.Fatalf("backfill failed: %+v %v", b, err)
	}
	// Repeating policy saves merges pending and serving uses into one slot.
	policy.Revision++
	if err := f.store.SaveCapacityPolicy(ctx, policy, 2); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_frontend_slots`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("double counted: %d %v", n, err)
	}
}

func frontendRunner(f *frontendFixture, executor *poolExecutor) *deployments.Runner {
	resolver := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}})
	return deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, Capacity: resolver, CloudProfiles: cloudprofiles.NewPgStore(f.db), Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"vercel": executor}})
}
func TestFrontendCapacityRunner(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(1))
	ctx := orchestration.CapacityContext(t.Context())
	f.plan(t, "first", "env", "web")
	f.plan(t, "second", "stage", "other")
	executor := &poolExecutor{}
	runner := frontendRunner(f, executor)
	first, _, err := runner.Start(ctx, "org", "first")
	if err != nil || first == "" {
		t.Fatalf("start: %s %v", first, err)
	}
	if err = runner.Run(t.Context(), "org", first, "service:web", nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("legacy runner bypassed v3")
	}
	if err = runner.Run(ctx, "org", first, "service:web", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	second, _, err := runner.Start(ctx, "org", "second")
	if err != nil || second == "" {
		t.Fatalf("start second: %s %v", second, err)
	}
	if err = runner.Run(ctx, "org", second, "service:other", nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("direct runner bypassed full slot")
	}
	if executor.calls != 1 {
		t.Fatalf("provider calls: %d", executor.calls)
	}
	var status string
	if err = f.db.QueryRow(ctx, `SELECT status FROM loomplane_deployments WHERE id=$1`, second).Scan(&status); err != nil || status != deployments.StatusPaused {
		t.Fatalf("status: %s %v", status, err)
	}
}
func TestFrontendCapacityIndependentOfDroplets(t *testing.T) {
	infra := ledgerFixture(t)
	ctx := t.Context()
	first := ledgerPreview(t, infra)
	if err := infra.store.Accept(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := ledgerPreview(t, infra)
	if err := infra.store.Accept(ctx, second); err == nil {
		t.Fatal("Droplet ceiling was not reached")
	}
	if _, err := infra.sql.Exec(ctx, `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`); err != nil {
		t.Fatal(err)
	}
	db := ledgerDB(t, infra)
	f := &frontendFixture{db: db, store: infra.store, plans: planning.NewPgStore(db)}
	f.plan(t, "frontend", "env", "web")
	executor := &poolExecutor{}
	runner := frontendRunner(f, executor)
	ctx = orchestration.CapacityContext(ctx)
	id, _, err := runner.Start(ctx, "org", "frontend")
	if err != nil || id == "" {
		t.Fatalf("start: %s %v", id, err)
	}
	if err = runner.Run(ctx, "org", id, "service:web", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("independent frontend did not deploy: %d", executor.calls)
	}
}
func TestFrontendCapacityFirstEffectRechecksPolicy(t *testing.T) {
	f := newFrontendFixture(t, nil)
	ctx := t.Context()
	d := f.plan(t, "first", "env", "web")
	attempt, err := f.store.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "web", Plan: d.PlanID, Deployment: "deployment"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveCapacityPolicy(ctx, &cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 2, FrontendLimits: []cloudinfra.FrontendLimit{{TargetID: "target", Maximum: frontendMax(0)}}}, 1); err != nil {
		t.Fatal(err)
	}
	if err = f.store.MarkWorkloadEffects(ctx, *attempt); err == nil {
		t.Fatal("first effect ignored new ceiling")
	}
	var accepted bool
	if err = f.db.QueryRow(ctx, `SELECT effects_started FROM loomplane_workload_attempts`).Scan(&accepted); err != nil || accepted {
		t.Fatalf("rejected effect accepted: %v %v", accepted, err)
	}
}

func TestFrontendCapacityPausedResume(t *testing.T) {
	f := newFrontendFixture(t, frontendMax(0))
	ctx := orchestration.CapacityContext(t.Context())
	f.plan(t, "resume", "env", "web")
	executor := &poolExecutor{}
	runner := frontendRunner(f, executor)
	id, _, err := runner.Start(ctx, "org", "resume")
	if err != nil {
		t.Fatal(err)
	}
	if wait, e := runner.AssessCapacity(ctx, "org", id); e != nil || wait == nil {
		t.Fatalf("pause: %+v %v", wait, e)
	}
	if err = f.store.SaveCapacityPolicy(ctx, &cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 2, FrontendLimits: []cloudinfra.FrontendLimit{{TargetID: "target", Maximum: frontendMax(1)}}}, 1); err != nil {
		t.Fatal(err)
	}
	if wait, e := runner.AssessCapacity(ctx, "org", id); e != nil || wait != nil {
		t.Fatalf("resume: %+v %v", wait, e)
	}
	if err = runner.Run(ctx, "org", id, "service:web", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("calls: %d", executor.calls)
	}
}

func TestFrontendCapacityCompensationRejectsUnresolvedPredecessor(t *testing.T) {
	for _, kind := range []string{"active", "failed", "missing-binding"} {
		t.Run(kind, func(t *testing.T) {
			f := newSyncFixture(t)
			ctx := orchestration.CapacityContext(t.Context())
			actor := security.Principal{OrgID: "org", UserID: "usr"}
			preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
			if err != nil {
				t.Fatal(err)
			}
			forward, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "negative-transfer")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.ReconcileCloudSync(ctx, "org", "proj", forward.ID, func([]byte) error { return nil }); err == nil {
				t.Fatal("expected fixture failure")
			}
			revert, err := f.service.PreviewCloudRevert(ctx, actor, "proj", forward.ID)
			if err != nil {
				t.Fatal(err)
			}
			next, err := f.service.ApplyCloudRevert(ctx, actor, "proj", revert.ID, revert.Revision, "negative-compensation")
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "active":
				_, err = f.db.Exec(ctx, `UPDATE loomplane_workload_attempts SET active=true WHERE environment_id='env' AND service='api'`)
			case "failed":
				_, err = f.db.Exec(ctx, `UPDATE loomplane_deployments SET steps=(SELECT jsonb_agg(CASE WHEN s->>'action_id'='service:api' THEN jsonb_set(s,'{status}','"failed"') ELSE s END) FROM jsonb_array_elements(steps) s) WHERE environment_id='env' AND plan_id=$1`, forward.ID+".env")
			case "missing-binding":
				_, err = f.db.Exec(ctx, `UPDATE loomplane_bindings SET plan_id='original-env' WHERE environment_id='env' AND name='api'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.ReconcileCloudSync(ctx, "org", "proj", next.ID, func([]byte) error { return nil }); err == nil {
				t.Fatal("unresolved predecessor transferred")
			}
			var owner string
			if err = f.db.QueryRow(ctx, `SELECT plan_id FROM loomplane_workload_attempts WHERE environment_id='env' AND service='api'`).Scan(&owner); err != nil || owner != forward.ID+".env" {
				t.Fatalf("lost original authority: %s %v", owner, err)
			}
		})
	}
}

func TestFrontendCapacityLegacyPlanCompatibility(t *testing.T) {
	for _, finite := range []bool{false, true} {
		t.Run(fmt.Sprint(finite), func(t *testing.T) {
			maximum := (*int64)(nil)
			if finite {
				maximum = frontendMax(1)
			}
			f := newFrontendFixture(t, maximum)
			ctx := t.Context()
			// Persist an accepted v2 review without a frontend demand, as before this migration.
			result := planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:web", Name: "web", Subject: planning.ServiceSubject, Op: planning.Create, Provider: "vercel", ConnectionID: "old", Service: &planning.ServiceConfig{Provider: "vercel", Target: "old", Workload: provider.WebApp, Execution: provider.Serverless}}}}
			raw, _ := json.Marshal(result)
			if _, err := f.db.Exec(ctx, `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES('legacy','org','proj','env','production','approved','legacy','manifest',1,$1,true,'usr',now())`, raw); err != nil {
				t.Fatal(err)
			}
			executor := &poolExecutor{}
			runner := frontendRunner(f, executor)
			id, _, err := runner.Start(ctx, "org", "legacy")
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(ctx, "org", id, "service:web", nil, func([]byte) error { return nil })
			if finite {
				if !errors.Is(err, deployments.ErrCapacityPaused) {
					t.Fatalf("legacy finite needs new review: %v", err)
				}
				saved, readErr := deployments.NewPgStore(f.db).GetDeployment(ctx, "org", id)
				if readErr != nil || saved.CapacityWait == nil || saved.CapacityWait.Blocks[0].Reason != "review_required" {
					t.Fatalf("missing review reason: %+v %v", saved, readErr)
				}
				if wait, assessErr := runner.AssessCapacity(ctx, "org", id); assessErr != nil || wait == nil || wait.Blocks[0].Reason != "review_required" {
					t.Fatalf("legacy review resumed without replan: %+v %v", wait, assessErr)
				}
				if executor.calls != 0 {
					t.Fatal("legacy bypass")
				}
			} else if err != nil || executor.calls != 1 {
				t.Fatalf("legacy unlimited changed: %d %v", executor.calls, err)
			}
		})
	}
}

func TestFrontendCapacityMixedPlanPreflight(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "mixed", "env")
	demand, err := f.store.ResolveFrontendDemand(t.Context(), cloudinfra.FrontendDemand{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", PlanID: plan.ID, Service: "web", Provider: "vercel", ConnectionID: "old"})
	if err != nil || demand == nil {
		t.Fatalf("frontend demand: %+v %v", demand, err)
	}
	plan.Actions = append([]planning.Action{{ID: "service:web", Name: "web", Subject: planning.ServiceSubject, Op: planning.Create, Provider: "vercel", ConnectionID: "old", Service: &planning.ServiceConfig{Provider: "vercel", Target: "old", Workload: provider.WebApp, Execution: provider.Serverless, FrontendDemand: demand}}}, plan.Actions...)
	raw, _ := json.Marshal(planning.Result{Executable: true, Connections: plan.Connections, Actions: plan.Actions})
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_plans SET result=$1 WHERE id=$2`, raw, plan.ID); err != nil {
		t.Fatal(err)
	}
	acceptFuture(t, f, plan.ID)
	resolver := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: &workflowCandidate{}})
	executor := &poolExecutor{}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, Capacity: resolver, CloudProfiles: f.clouds, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"vercel": executor, "server": executor}})
	ctx := orchestration.CapacityContext(t.Context())
	id, _, err := runner.Start(ctx, "org", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(ctx, "org", id, "service:web", nil, func([]byte) error { return nil })
	if !errors.Is(err, deployments.ErrCapacityPaused) || executor.calls != 0 {
		t.Fatalf("mixed preflight: calls=%d err=%v", executor.calls, err)
	}
}

func TestFrontendCapacityCloudSyncSelectsV3(t *testing.T) {
	f := newSyncFixture(t)
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := f.service.PreviewCloudChange(t.Context(), actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.service.ApplyCloudChange(t.Context(), actor, "proj", preview.ID, preview.Revision, "frontend-v3")
	if err != nil {
		t.Fatal(err)
	}
	needs, err := f.service.CloudSyncNeedsCapacity(t.Context(), "org", "proj", accepted.ID)
	if err != nil || !needs {
		t.Fatalf("frontend sync selector: %v %v", needs, err)
	}
	prepared, err := f.service.PrepareCapacityCloudSync(orchestration.CapacityContext(t.Context()), "org", "proj", accepted.ID)
	if err != nil || prepared.Stopped || len(prepared.Deployments) == 0 {
		t.Fatalf("v3 preparation: %+v %v", prepared, err)
	}
	if err = f.service.ReconcileCloudSync(t.Context(), "org", "proj", accepted.ID, func([]byte) error { return nil }); err == nil {
		t.Fatal("ordinary sync bypassed frontend capacity protocol")
	}
	if len(f.executor.calls) != 0 {
		t.Fatal("legacy sync had effects")
	}
}
