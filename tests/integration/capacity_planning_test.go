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
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type capacityPlanAuth struct{ denied bool }

func (a *capacityPlanAuth) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	if a.denied || p.ReadOnly || p.OrgID != "org" || p.UserID != "usr" {
		return security.ErrForbidden
	}
	return nil
}

type capacityPlanCandidate struct{ placement *cloudinfra.Placement }

func (c capacityPlanCandidate) EnsureCapacity(context.Context, cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error) {
	return &cloudinfra.CapacityDecision{Placement: c.placement}, nil
}

func futurePlan(t *testing.T, f *poolFixture, id, env string) *planning.Plan {
	t.Helper()
	return futurePlanWith(t, f, id, env, nil)
}

// futurePlanWith writes an approved-ready plan whose api service waits for
// pool capacity. change edits the intent before it's hashed, and the
// policy offers the intent's template.
func futurePlanWith(t *testing.T, f *poolFixture, id, env string, change func(*planning.CapacityIntent)) *planning.Plan {
	t.Helper()
	req := f.request(env, "api")
	template := cloudinfra.PoolExpansionPolicy{PoolID: "pool", ConnectionID: "runtime-1", Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"small"}}
	intent := &planning.CapacityIntent{ProviderPin: cloudinfra.CapacityTemplatePin{ConnectionGeneration: 1, AccountID: "runtime-1"}, Template: template, CommitSHA: "commit", AssignmentRevision: 1, CloudRevision: 1, TargetRevision: 1, GrantRevision: 1, TargetName: "shared", Demand: cloudinfra.CapacityDemand{OrgID: "org", CloudID: "cloud", PoolID: "pool", TargetID: "pool-target", ProjectID: req.ProjectID, EnvironmentID: env, PlanID: id, Service: "api", Architecture: "amd64", PolicyRevision: 1, Resources: cloudinfra.ResourceVector{CPUMillis: 2000, MemoryBytes: 512 << 20}}}
	if change != nil {
		change(intent)
	}
	intent.TemplateHash, _ = planning.Digest(intent.Template)
	intent.Demand.IntentHash, _ = planning.Digest(intent)
	policy := cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Enabled: true, Limits: cloudinfra.ResourceVector{Servers: 10, CPUMillis: 20000, MemoryBytes: 20 << 30, StorageBytes: 100 << 30}, Pools: []cloudinfra.PoolExpansionPolicy{intent.Template}}
	raw, _ := json.Marshal(policy)
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,enabled,servers,cpu_millis,memory_bytes,storage_bytes,body) VALUES('org','cloud',1,true,10,20000,21474836480,107374182400,$1) ON CONFLICT DO NOTHING`, raw); err != nil {
		t.Fatal(err)
	}
	size := &manifest.Size{CPU: float64(intent.Demand.Resources.CPUMillis) / 1000, Memory: int(intent.Demand.Resources.MemoryBytes >> 20)}
	if r := intent.Demand.Resources; r.GPUs > 0 {
		size.GPU = &manifest.GPU{Kind: r.GPUKind, Count: int(r.GPUs)}
	}
	cfg := &planning.ServiceConfig{Provider: "server", CommitSHA: "commit", CapacityIntent: intent, Size: size}
	result := planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: planning.Create, Provider: "server", Service: cfg}}}
	raw, _ = json.Marshal(result)
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org',$2,$3,'production','ready','future-review','pool-manifest-'||$2,1,$4,true,'usr',now())`, id, req.ProjectID, env, raw); err != nil {
		t.Fatal(err)
	}
	plan, err := f.plans.GetPlan(t.Context(), "org", id)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func acceptFuture(t *testing.T, f *poolFixture, id string) {
	t.Helper()
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "usr"})
	ok, err := f.plans.Decide(ctx, "org", id, "approved", "usr", "", time.Now())
	if err != nil || !ok {
		t.Fatalf("approve future: %v %v", ok, err)
	}
}

func TestCapacityPlanningApprovalAndResolution(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "future", "env")
	acceptFuture(t, f, plan.ID)
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations WHERE plan_id=$1`, plan.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("premature reservation: %d %v", count, err)
	}
	req := f.request("env", "api")
	req.CPUMillis = 2000
	req.PlanID = plan.ID
	candidate, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	auth := &capacityPlanAuth{}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: auth, Capacity: capacityPlanCandidate{candidate}})
	if _, err = svc.ResolveApprovedCapacityDemand(t.Context(), "org", plan.ID, "api"); err != nil {
		t.Fatal(err)
	}
	candidate.ConnectionGeneration++
	var wait *cloudinfra.CapacityAdmissionError
	if staleErr := svc.ResolveCapacityIntents(t.Context(), "org", plan.ID); !errors.As(staleErr, &wait) || wait.Block.Reason != "provisioning" || !wait.Block.Retryable {
		t.Fatalf("stale candidate did not wait: %v", staleErr)
	}
	candidate.ConnectionGeneration--
	if err = svc.ResolveCapacityIntents(t.Context(), "org", plan.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.ResolveCapacityIntents(t.Context(), "org", plan.ID); err != nil {
		t.Fatal(err)
	}
	pins, err := f.plans.ResolvedCapacityPlacements(t.Context(), "org", plan.ID)
	if err != nil || pins["api"].ServerID != candidate.ServerID {
		t.Fatalf("pin %+v %v", pins, err)
	}
	after, err := f.plans.GetPlan(t.Context(), "org", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Actions[0].Service.CloudPlacement != nil || after.Actions[0].Service.CapacityIntent == nil {
		t.Fatal("approved plan rewritten")
	}
	pin := pins["api"]
	if err = f.store.ValidatePlacement(t.Context(), "org", "proj", "env", plan.ID, "api", pin.PoolPlacement()); err != nil {
		t.Fatal(err)
	}
	auth.denied = true
	if _, err = svc.ResolveApprovedCapacityDemand(t.Context(), "org", plan.ID, "api"); err == nil {
		t.Fatal("revoked actor accepted")
	}
}

// TestCapacityIntentCarriesGPUs approves a service that waits for an L4 in
// the GPU pool. The approved demand asks for the L4, a candidate without it
// isn't what was approved, and the admitted pin reserves it on the server.
func TestCapacityIntentCarriesGPUs(t *testing.T) {
	f := newGPUPoolFixture(t)
	ctx := t.Context()
	plan := futurePlanWith(t, f, "future-gpu", "env", func(i *planning.CapacityIntent) {
		i.Template.PoolID, i.Template.ConnectionID, i.Template.AcceleratorKind = "gpu-pool", "runtime-gpu", "nvidia-l4"
		i.ProviderPin.AccountID = "runtime-gpu"
		i.TargetName = "gpu"
		i.Demand.PoolID, i.Demand.TargetID = "gpu-pool", "gpu-target"
		i.Demand.Resources.GPUKind, i.Demand.Resources.GPUs = "nvidia-l4", 1
	})
	acceptFuture(t, f, plan.ID)
	auth := &capacityPlanAuth{}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: auth})
	demand, err := svc.ResolveApprovedCapacityDemand(ctx, "org", plan.ID, "api")
	if err != nil || demand.Resources.GPUKind != "nvidia-l4" || demand.Resources.GPUs != 1 {
		t.Fatalf("approved demand: %+v %v", demand, err)
	}
	req := gpuRequest(f, "api")
	req.CPUMillis, req.PlanID = 2000, plan.ID
	candidate, err := f.store.PreviewPlacement(ctx, req)
	if err != nil || candidate.GPUKind != "nvidia-l4" || candidate.GPUs != 1 {
		t.Fatalf("GPU candidate: %+v %v", candidate, err)
	}

	cpuOnly := *candidate
	cpuOnly.GPUKind, cpuOnly.GPUs = "", 0
	svc = planning.NewService(planning.Deps{Store: f.plans, Authz: auth, Capacity: capacityPlanCandidate{&cpuOnly}})
	var review *cloudinfra.CapacityAdmissionError
	if err = svc.ResolveCapacityIntents(ctx, "org", plan.ID); !errors.As(err, &review) || review.Block.Reason != "review_required" {
		t.Fatalf("candidate without GPUs: %v", err)
	}

	svc = planning.NewService(planning.Deps{Store: f.plans, Authz: auth, Capacity: capacityPlanCandidate{candidate}})
	if err = svc.ResolveCapacityIntents(ctx, "org", plan.ID); err != nil {
		t.Fatal(err)
	}
	pins, err := f.plans.ResolvedCapacityPlacements(ctx, "org", plan.ID)
	if err != nil || pins["api"].GPUKind != "nvidia-l4" || pins["api"].GPUs != 1 || pins["api"].ServerID != "server-gpu" {
		t.Fatalf("admitted pin: %+v %v", pins, err)
	}
	var kind string
	var gpus int64
	if err = f.sql.QueryRow(ctx, `SELECT gpu_kind,gpus FROM loomplane_capacity_reservations WHERE plan_id=$1 AND service='api'`, plan.ID).Scan(&kind, &gpus); err != nil || kind != "nvidia-l4" || gpus != 1 {
		t.Fatalf("reservation: %q %d %v", kind, gpus, err)
	}
	pin := pins["api"]
	if err = f.store.ValidatePlacement(ctx, "org", "proj", "env", plan.ID, "api", pin.PoolPlacement()); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityPlanningRaceAndChangedReview(t *testing.T) {
	f := newPoolFixture(t)
	first := futurePlan(t, f, "first", "env")
	second := futurePlan(t, f, "second", "second-env")
	acceptFuture(t, f, first.ID)
	acceptFuture(t, f, second.ID)
	req := f.request("env", "api")
	req.CPUMillis = 2000
	candidate, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: capacityPlanCandidate{candidate}})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{first.ID, second.ID} {
		wg.Add(1)
		go func() { defer wg.Done(); results <- svc.ResolveCapacityIntents(t.Context(), "org", id) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			var block *cloudinfra.CapacityAdmissionError
			if !errors.As(err, &block) || block.Block.Reason != "provisioning" {
				t.Fatal(err)
			}
		}
	}
	if successes != 1 {
		t.Fatalf("admitted %d competitors", successes)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET revision='changed' WHERE id='first'`); err == nil {
		t.Fatal("approved revision mutable")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_targets SET revision=revision+1 WHERE id='pool-target'`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolveApprovedCapacityDemand(t.Context(), "org", first.ID, "api"); err == nil {
		t.Fatal("changed target accepted")
	}
}
func TestCapacityPlanningRequiresActor(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "future", "env")
	if _, err := f.plans.Decide(t.Context(), "org", plan.ID, "approved", "usr", "", time.Now()); err == nil {
		t.Fatal("missing persisted actor accepted")
	}
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "usr", ReadOnly: true})
	if _, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "usr", "", time.Now()); err == nil {
		t.Fatal("read-only actor accepted")
	}
}

type deployOnlyTemplateAuth struct{}

func (deployOnlyTemplateAuth) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	if p.UserID != "deployer" || perm != security.DeploymentApprove {
		return security.ErrForbidden
	}
	return nil
}
func TestCapacityPlanningTemplateFit(t *testing.T) {
	f, _, d := expansionFixture(t, false, 0)
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	template := policy.Pools[0]
	template.MinServers, template.MaxServers, template.Limits = 0, 0, nil
	f.svc.Authz = deployOnlyTemplateAuth{}
	p := security.Principal{OrgID: "org", UserID: "deployer"}
	pin, err := f.svc.ValidateCapacityTemplate(t.Context(), p, d, template)
	if err != nil || pin.ConnectionGeneration < 1 {
		t.Fatalf("deployer fit: %+v %v", pin, err)
	}
	d.Resources.CPUMillis = 3000
	if _, err = f.svc.ValidateCapacityTemplate(t.Context(), p, d, template); err == nil {
		t.Fatal("oversized workload approved")
	}
	d.Resources.CPUMillis = 1000
	d.Architecture = "arm64"
	if _, err = f.svc.ValidateCapacityTemplate(t.Context(), p, d, template); err == nil {
		t.Fatal("incompatible architecture approved")
	}
	d.Architecture = "amd64"
	template.Image = "changed"
	if _, err = f.svc.ValidateCapacityTemplate(t.Context(), p, d, template); err == nil {
		t.Fatal("changed template accepted")
	}
	template.Image = "ubuntu"
	adapters := cloudinfra.NewAdapters()
	if registerErr := adapters.RegisterServer("digitalocean", unavailableTemplateProvider{expansionProvider{f.provider}}); registerErr != nil {
		t.Fatal(registerErr)
	}
	f.svc.Adapters = adapters
	if _, err = f.svc.ValidateCapacityTemplate(t.Context(), p, d, template); err == nil {
		t.Fatal("unavailable catalog asserted fit")
	}
	if f.provider.creates != 0 {
		t.Fatal("read-only validation provisioned")
	}
}

type capacityPlanningTemplate struct{}

func (capacityPlanningTemplate) ValidateCapacityTemplate(context.Context, security.Principal, cloudinfra.CapacityDemand, cloudinfra.PoolExpansionPolicy) (*cloudinfra.CapacityTemplatePin, error) {
	return &cloudinfra.CapacityTemplatePin{ConnectionGeneration: 1, AccountID: "runtime-1"}, nil
}
func TestCapacityPlanningCloudSync(t *testing.T) {
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
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: &syncAuth{}, CapacityTemplates: capacityPlanningTemplate{}})
	svc.BindCloudSyncRunner(nil, acceptanceInputs{})
	preview := reviewPoolChange(t, svc)
	accepted, err := svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "future")
	if err != nil {
		t.Fatal(err)
	}
	replay, replayErr := svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, "", preview.ID, preview.Revision, "retry")
	if replayErr != nil || replay.ID != accepted.ID {
		t.Fatalf("replay changed sync: %+v %v", replay, replayErr)
	}
	if _, foreignErr := svc.ApplyCloudChange(t.Context(), security.Principal{OrgID: "foreign", UserID: "usr"}, "", preview.ID, preview.Revision, "foreign"); foreignErr == nil {
		t.Fatal("foreign preview accepted")
	}
	if _, mutateErr := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_syncs SET snapshot=jsonb_set(snapshot,'{environments,0,plan,actions}','[]') WHERE id=$1`, accepted.ID); mutateErr == nil {
		t.Fatal("accepted snapshot mutable")
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_intents WHERE plan_id LIKE $1`, accepted.ID+".%").Scan(&count); err != nil || count != 2 {
		t.Fatalf("sync intents %d %v", count, err)
	}
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations WHERE plan_id LIKE $1`, accepted.ID+".%").Scan(&count); err != nil || count != 0 {
		t.Fatalf("premature sync reservation %d %v", count, err)
	}
	for _, env := range []string{"env", "second-env"} {
		if _, err = svc.ResolveApprovedCapacityDemand(t.Context(), "org", accepted.ID+"."+env, "api"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_server_pool_members(org_id,pool_id,server_id,cpu_millis,memory_bytes) VALUES('org','next-pool','server-2',2000,2147483648)`); err != nil {
		t.Fatal(err)
	}
	next.Members = []cloudinfra.PoolMember{{ServerID: "server-2", CPUMillis: 2000, MemoryBytes: 2 << 30}}
	raw, _ = json.Marshal(next)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_server_pools SET body=$1 WHERE id='next-pool'`, raw); err != nil {
		t.Fatal(err)
	}
	req := f.request("env", "api")
	req.PoolID = "next-pool"
	req.TargetRevision = 2
	req.PlanID = accepted.ID + ".env"
	candidate, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	resolver := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, Capacity: capacityPlanCandidate{candidate}})
	for _, env := range []string{"env", "second-env"} {
		if err = resolver.ResolveCapacityIntents(t.Context(), "org", accepted.ID+"."+env); err != nil {
			t.Fatal(err)
		}
	}
}

type savedKeyVerifier struct{ revoked bool }

func (v *savedKeyVerifier) ValidatePrincipal(_ context.Context, p security.Principal) error {
	if v.revoked || p.KeyID != "key" {
		return security.ErrForbidden
	}
	return nil
}
func TestCapacityPlanningKeyTemplateAndLockPins(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "key-plan", "env")
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "usr", KeyID: "key"})
	if ok, err := f.plans.Decide(ctx, "org", plan.ID, "approved", "usr", "", time.Now()); err != nil || !ok {
		t.Fatalf("approval %v %v", ok, err)
	}
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}})
	if _, err := svc.ResolveApprovedCapacityDemand(t.Context(), "org", plan.ID, "api"); err == nil {
		t.Fatal("key accepted without verifier")
	}
	key := &savedKeyVerifier{}
	svc = planning.NewService(planning.Deps{Store: f.plans, Authz: &capacityPlanAuth{}, CapacityPrincipal: key})
	// The resolver must complete while Task4 holds its mutation gates.
	tx, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('cloud-organization/org',0)); SELECT id FROM loomplane_cloud_profiles WHERE id='cloud' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err = svc.ResolveApprovedCapacityDemand(readCtx, "org", plan.ID, "api"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	key.revoked = true
	if _, err = svc.ResolveApprovedCapacityDemand(t.Context(), "org", plan.ID, "api"); err == nil {
		t.Fatal("revoked key accepted")
	}
	key.revoked = false
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_capacity_policies SET revision=2,body=jsonb_set(jsonb_set(body,'{revision}','2'),'{pools,0,max_servers}','100') WHERE org_id='org' AND cloud_id='cloud'`); err != nil {
		t.Fatal(err)
	}
	demand, err := svc.ResolveApprovedCapacityDemand(t.Context(), "org", plan.ID, "api")
	if err != nil || demand.PolicyRevision != 1 {
		t.Fatalf("ceiling changed approved demand: %+v %v", demand, err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET generation=generation+1 WHERE id='runtime-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolveApprovedCapacityDemand(t.Context(), "org", plan.ID, "api"); err == nil {
		t.Fatal("changed provider credential accepted")
	}
}

type unavailableTemplateProvider struct{ expansionProvider }

func (unavailableTemplateProvider) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	return provider.ServerOptions{}, errors.New("catalog unavailable")
}
