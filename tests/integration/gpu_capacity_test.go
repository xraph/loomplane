//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

const gpuCapacityMigration = "20261116000100"

// newGPUPoolFixture adds an L4 pool to the shared pool fixture: one ready
// server with two L4s, a ready GPU runtime and a member budget of two GPUs.
func newGPUPoolFixture(t *testing.T) *poolFixture {
	t.Helper()
	f := newPoolFixture(t)
	now := time.Now().UTC()
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), &connections.Connection{ID: "runtime-gpu", OrgID: "org", Cloud: "server", Name: "runtime-gpu", Account: "runtime-gpu", Details: map[string]string{"docker_id": "runtime-gpu", "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	server := &cloudinfra.Server{ID: "server-gpu", OrgID: "org", CloudID: "cloud", ConnectionID: "runtime-gpu", RuntimeConnectionID: "runtime-gpu", Ownership: "adopted", Provider: "generic", AccountID: "runtime-gpu", Status: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now, Health: cloudinfra.RuntimeHealth{DockerID: "runtime-gpu", Architecture: "amd64", CPUs: 16, MemoryBytes: 64 << 30, EffectiveCPUMillis: 16000, EffectiveMemoryBytes: 64 << 30, GPURuntime: provider.GPUStatusReady, GPUs: []provider.GPUDevice{{Index: 0, UUID: "GPU-0", Kind: "nvidia-l4"}, {Index: 1, UUID: "GPU-1", Kind: "nvidia-l4"}}, CheckedAt: now}}
	if err := f.store.SaveServer(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	gpuPool := &cloudinfra.Pool{ID: "gpu-pool", OrgID: "org", CloudID: "cloud", Name: "L4", Revision: 1, AcceleratorKind: "nvidia-l4", Members: []cloudinfra.PoolMember{{ServerID: "server-gpu", CPUMillis: 8000, MemoryBytes: 32 << 30, GPUs: 2}}}
	if err := f.store.SavePool(t.Context(), gpuPool, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.clouds.CreateTarget(t.Context(), &cloudprofiles.Target{ID: "gpu-target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "gpu", DisplayName: "GPU", Provider: "server", Runtime: "server", PoolID: "gpu-pool", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return f
}

func gpuRequest(f *poolFixture, name string) cloudinfra.PlacementRequest {
	r := f.request("env", name)
	r.TargetID, r.PoolID, r.GPUKind, r.GPUs = "gpu-target", "gpu-pool", "nvidia-l4", 1
	return r
}

// gpuPlan writes a plan for the GPU target. The planner doesn't carry GPU
// fields yet, so the pin gets them, and the target, patched in as JSON.
func gpuPlan(t *testing.T, f *poolFixture, id string, r cloudinfra.PlacementRequest, p *cloudinfra.Placement, gpus bool) *planning.CloudPlacement {
	t.Helper()
	pin := f.plan(t, id, r, p)
	patch := map[string]any{"target_id": "gpu-target", "target_name": "gpu"}
	if gpus {
		patch["gpu_kind"], patch["gpus"] = p.GPUKind, p.GPUs
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,cloud_placement}',(result#>'{actions,0,service,cloud_placement}')||$2::jsonb) WHERE id=$1`, id, raw); err != nil {
		t.Fatal(err)
	}
	return pin
}

func decideError(t *testing.T, f *poolFixture, id, want string) {
	t.Helper()
	_, err := f.plans.Decide(t.Context(), "org", id, "approved", "usr", "", time.Now())
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.Message, want) {
		t.Fatalf("plan %s: want SQLSTATE 23514 %q, got %v", id, want, err)
	}
}

// closePlan rejects a refused plan, since an environment has one open plan.
func closePlan(t *testing.T, f *poolFixture, id string) {
	t.Helper()
	if ok, err := f.plans.Decide(t.Context(), "org", id, "rejected", "usr", "", time.Now()); err != nil || !ok {
		t.Fatalf("reject %s: %v %v", id, ok, err)
	}
}

func setGPURuntime(t *testing.T, f *poolFixture, status string) {
	t.Helper()
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_servers SET body=jsonb_set(body,'{health,gpu_runtime}',to_jsonb($1::text)) WHERE id='server-gpu'`, status); err != nil {
		t.Fatal(err)
	}
}

func TestGPUPoolReservations(t *testing.T) {
	f := newGPUPoolFixture(t)
	ctx := t.Context()
	usage := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &infraAuth{}})
	manager := security.Principal{OrgID: "org", UserID: "manager"}

	// 1. Two plans each reserve one L4 on the server.
	var first *cloudinfra.Placement
	var firstPin *planning.CloudPlacement
	for _, tc := range []struct{ id, service string }{{"g1", "llm-a"}, {"g2", "llm-b"}} {
		r := gpuRequest(f, tc.service)
		p, err := f.store.PreviewPlacement(ctx, r)
		if err != nil || p.ServerID != "server-gpu" || p.GPUKind != "nvidia-l4" || p.GPUs != 1 {
			t.Fatalf("GPU placement %s: %+v %v", tc.id, p, err)
		}
		pin := gpuPlan(t, f, tc.id, r, p, true)
		f.accept(t, tc.id)
		if err = f.store.ValidatePlacement(ctx, "org", "proj", "env", tc.id, tc.service, *p); err != nil {
			t.Fatalf("validate %s: %v", tc.id, err)
		}
		if first == nil {
			first, firstPin = p, pin
		}
	}
	var kind string
	var gpus int64
	if err := f.sql.QueryRow(ctx, `SELECT gpu_kind,gpus FROM loomplane_capacity_reservations WHERE plan_id='g1'`).Scan(&kind, &gpus); err != nil || kind != "nvidia-l4" || gpus != 1 {
		t.Fatalf("reservation columns: %q %d %v", kind, gpus, err)
	}
	report, err := usage.ServerUsage(ctx, manager, "cloud", "server-gpu")
	if err != nil || report.ReservedGPUs != 2 || len(report.Workloads) != 2 || report.Workloads[0].GPUs != 1 || report.Workloads[1].GPUs != 1 {
		t.Fatalf("server usage: %+v %v", report, err)
	}
	// The pinned GPU fields are part of the reviewed placement.
	changed := *first
	changed.GPUs = 2
	if err = f.store.ValidatePlacement(ctx, "org", "proj", "env", "g1", "llm-a", changed); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("validate changed GPUs: %v", err)
	}
	// The budget can't drop below what's reserved.
	pool, err := f.store.GetPool(ctx, "org", "cloud", "gpu-pool")
	if err != nil {
		t.Fatal(err)
	}
	pool.Revision = 2
	pool.Members[0].GPUs = 1
	if err = f.store.SavePool(ctx, pool, 1); !errors.Is(err, cloudinfra.ErrConflict) || !strings.Contains(err.Error(), "below reserved") {
		t.Fatalf("GPU budget below reservations: %v", err)
	}
	if _, err = f.store.PreviewPlacement(ctx, gpuRequest(f, "llm-c")); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("preview on a full GPU budget: %v", err)
	}

	// 2. A third L4 on the same server is refused by the database.
	gpuPlan(t, f, "g3", gpuRequest(f, "llm-c"), first, true)
	decideError(t, f, "g3", "insufficient GPUs")
	closePlan(t, f, "g3")

	// 3. CPU work can't take a reservation in the GPU pool.
	cpu := gpuRequest(f, "web")
	cpu.GPUKind, cpu.GPUs = "", 0
	cpuPlacement := *first
	cpuPlacement.GPUKind, cpuPlacement.GPUs = "", 0
	gpuPlan(t, f, "c1", cpu, &cpuPlacement, false)
	decideError(t, f, "c1", "a GPU pool takes only GPU workloads")
	closePlan(t, f, "c1")

	// R8: once the toolkit goes missing, recording the deployed binding
	// still works. Only a new or moved GPU reservation is checked.
	setGPURuntime(t, f, provider.GPUStatusToolkitMissing)
	cfg, err := json.Marshal(planning.ServiceConfig{Provider: "server", Target: first.ConnectionID, CloudPlacement: firstPin})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = f.plans.PutBinding(ctx, &planning.Binding{ID: "binding-g1", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "llm-a", Provider: "server", ConnectionID: first.ConnectionID, Config: cfg, PlanID: "g1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("binding update after the toolkit went missing: %v", err)
	}
	var binding string
	if err = f.sql.QueryRow(ctx, `SELECT binding_id FROM loomplane_capacity_reservations WHERE plan_id='g1'`).Scan(&binding); err != nil || binding != "binding-g1" {
		t.Fatalf("binding recorded: %q %v", binding, err)
	}

	// 4. With room again, a new GPU reservation still needs a ready runtime.
	if _, err = f.sql.Exec(ctx, `DELETE FROM loomplane_capacity_reservations WHERE plan_id='g2'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PreviewPlacement(ctx, gpuRequest(f, "llm-d")); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("preview with the toolkit missing: %v", err)
	}
	gpuPlan(t, f, "g4", gpuRequest(f, "llm-d"), first, true)
	decideError(t, f, "g4", "GPU runtime")
	setGPURuntime(t, f, provider.GPUStatusReady)
	f.accept(t, "g4")
	if report, err = usage.ServerUsage(ctx, manager, "cloud", "server-gpu"); err != nil || report.ReservedGPUs != 2 {
		t.Fatalf("usage after recovery: %+v %v", report, err)
	}
}

func TestGPUPoolRefusesCPUPlacement(t *testing.T) {
	f := newGPUPoolFixture(t)
	ctx := t.Context()
	cpu := gpuRequest(f, "web")
	cpu.GPUKind, cpu.GPUs = "", 0
	if _, err := f.store.PreviewPlacement(ctx, cpu); !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), "a GPU pool takes only GPU workloads") {
		t.Fatalf("CPU work in a GPU pool: %v", err)
	}
	t4 := gpuRequest(f, "llm")
	t4.GPUKind = "nvidia-t4"
	if _, err := f.store.PreviewPlacement(ctx, t4); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("T4 work in an L4 pool: %v", err)
	}
	gpuInCPU := f.request("env", "llm")
	gpuInCPU.GPUKind, gpuInCPU.GPUs = "nvidia-l4", 1
	if _, err := f.store.PreviewPlacement(ctx, gpuInCPU); !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), "a CPU pool takes no GPU workloads") {
		t.Fatalf("GPU work in a CPU pool: %v", err)
	}
	tooMany := gpuRequest(f, "llm")
	tooMany.GPUs = 3
	if _, err := f.store.PreviewPlacement(ctx, tooMany); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("three L4s on a two-L4 budget: %v", err)
	}
	// Another service planned in the same review counts against the budget.
	planned := gpuRequest(f, "llm")
	planned.GPUs = 2
	planned.Planned = []cloudinfra.PlannedPlacement{{Service: "other", Placement: cloudinfra.Placement{ServerID: "server-gpu", CPUMillis: 1000, MemoryBytes: 512 << 20, GPUKind: "nvidia-l4", GPUs: 1}}}
	if _, err := f.store.PreviewPlacement(ctx, planned); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("planned GPUs ignored: %v", err)
	}
	p, err := f.store.PreviewPlacement(ctx, gpuRequest(f, "llm"))
	if err != nil || p.ServerID != "server-gpu" || p.GPUKind != "nvidia-l4" || p.GPUs != 1 {
		t.Fatalf("L4 work in the L4 pool: %+v %v", p, err)
	}
	// The CPU pool keeps taking CPU work as before, with no GPU fields.
	p, err = f.store.PreviewPlacement(ctx, f.request("env", "web"))
	if err != nil || p.PoolID != "pool" || p.GPUKind != "" || p.GPUs != 0 {
		t.Fatalf("CPU work in the CPU pool: %+v %v", p, err)
	}
}

type gpuCeilingProvider struct{ *infraProvider }

func (p gpuCeilingProvider) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}},
		Images: []provider.ServerImage{{ID: "gpu-base", Bootstrap: true, Architecture: "amd64", Regions: []string{"region"}, Accelerators: []string{"nvidia"}}},
		Sizes: []provider.ServerSize{{ID: "gpu-h100x1-80gb", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Architectures: []string{"amd64"}, Regions: []string{"region"},
			Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}}}}, nil
}

// gpuCeilingFixture is an enabled policy with an H100 pool, a Cloud ceiling of
// one H100 and one H100 server already in inventory.
func gpuCeilingFixture(t *testing.T) *infraFixture {
	t.Helper()
	return gpuCeilingFixtureWith(t, 1, nil)
}

// gpuCeilingFixtureWith sets the Cloud's H100 ceiling and the pool's own limits.
func gpuCeilingFixtureWith(t *testing.T, cloudH100 int64, poolLimits *cloudinfra.ResourceVector) *infraFixture {
	t.Helper()
	f := newInfraFixture(t)
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", gpuCeilingProvider{f.provider}); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	f.svc.EnableAutomaticCapacity = true
	if err := f.store.SavePool(t.Context(), &cloudinfra.Pool{ID: "pool", OrgID: "org", CloudID: "cloud", Name: "H100", Revision: 1, AcceleratorKind: "nvidia-h100-80gb", Members: []cloudinfra.PoolMember{}}, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.svc.Clouds.(*cloudprofiles.PgStore).CreateTarget(t.Context(), &cloudprofiles.Target{ID: "pool-target", OrgID: "org", CloudID: "cloud", ProjectID: "proj", Name: "pool", DisplayName: "Pool", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{Enabled: true, Limits: cloudinfra.ResourceVector{Servers: 4, CPUMillis: 8000, MemoryBytes: 8 << 30, StorageBytes: 80_000_000_000}, GPULimits: []cloudinfra.GPULimit{{Kind: "nvidia-h100-80gb", Maximum: cloudH100}},
		Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "pool", AcceleratorKind: "nvidia-h100-80gb", ConnectionID: "infra-account", Region: "region", Architecture: "amd64", Image: "gpu-base", AllowedSizes: []string{"gpu-h100x1-80gb"}, MaxServers: 4, Limits: poolLimits, SystemReserve: cloudinfra.ResourceVector{CPUMillis: 500, MemoryBytes: 512 << 20}}}}
	if _, err := f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: policy}); err != nil {
		t.Fatal(err)
	}
	_, record := ledgerHost(t, f, "h100-host", 2000)
	record.GPUKind, record.GPUs = "nvidia-h100-80gb", inventoryNumber(1)
	zero := cloudinfra.CapacityResources{Servers: inventoryNumber(0), CPUMillis: inventoryNumber(0), MemoryBytes: inventoryNumber(0), StorageBytes: inventoryNumber(0)}
	ledgerSave(t, f, []cloudinfra.CapacityRecord{record}, []cloudinfra.CapacityAccountSnapshot{{Provider: "digitalocean", AccountID: "account", Provisioned: zero, CheckedAt: time.Now()}})
	f.svc.CapacityDemandAuthority = &expansionAuthority{db: f.sql}
	f.svc.CapacityDemandWakeup = &expansionWakeup{}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('capacity-manifest','org','proj',1,'version: 1','capacity','manager',now())`); err != nil {
		t.Fatal(err)
	}
	return f
}

func h100Demand(f *infraFixture, t *testing.T, n string) cloudinfra.CapacityDemand {
	t.Helper()
	d := cloudinfra.CapacityDemand{OrgID: "org", CloudID: "cloud", PoolID: "pool", TargetID: "pool-target", ProjectID: "proj", EnvironmentID: "env", PlanID: "capacity-plan-" + n, Service: "llm-" + n, Architecture: "amd64", Resources: cloudinfra.ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 1}, PolicyRevision: 1, IntentHash: "approved-demand-" + n}
	expansionPlan(t, f, d)
	return d
}

func ensureBlock(t *testing.T, f *infraFixture, d cloudinfra.CapacityDemand) *cloudinfra.CapacityBlock {
	t.Helper()
	decision, err := f.svc.EnsureCapacity(t.Context(), d)
	if err != nil || decision == nil || decision.Block == nil || decision.Placement != nil {
		t.Fatalf("ensure %s: %+v %v", d.Service, decision, err)
	}
	return decision.Block
}

func TestGPUCeilingBlocksExpansion(t *testing.T) {
	f := gpuCeilingFixture(t)
	ctx := t.Context()
	usage, err := f.store.GetCapacityUsage(ctx, "org", "cloud")
	if err != nil || len(usage.GPUs) != 1 {
		t.Fatalf("GPU usage: %+v %v", usage, err)
	}
	if g := usage.GPUs[0]; g.Kind != "nvidia-h100-80gb" || g.Provisioned == nil || *g.Provisioned != 1 || g.Limit == nil || *g.Limit != 1 || g.Reserved != 0 || g.Pending != 0 {
		t.Fatalf("H100 usage: %+v", g)
	}

	b := ensureBlock(t, f, h100Demand(f, t, "a"))
	if b.Reason != "hard_limit" || b.Resource != "gpus:nvidia-h100-80gb" || b.Limit != 1 || b.Counted != 1 || b.Requested != 1 {
		t.Fatalf("H100 ceiling: %+v", b)
	}
	var reserved int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_capacity_reservations`).Scan(&reserved); err != nil || reserved != 0 || f.provider.creates != 0 {
		t.Fatalf("blocked expansion reserved %d or created %d servers: %v", reserved, f.provider.creates, err)
	}
	var kind string
	var gpus int64
	if err = f.sql.QueryRow(ctx, `SELECT gpu_kind,gpus FROM loomplane_cloud_expansions WHERE pool_id='pool'`).Scan(&kind, &gpus); err != nil || kind != "nvidia-h100-80gb" || gpus != 1 {
		t.Fatalf("expansion columns: %q %d %v", kind, gpus, err)
	}

	// A stale snapshot can't admit anything.
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_cloud_capacity_inventory SET body=jsonb_set(body,'{checked_at}',to_jsonb(now()-interval '1 hour'))`); err != nil {
		t.Fatal(err)
	}
	if b = ensureBlock(t, f, h100Demand(f, t, "b")); b.Reason != "stale_inventory" {
		t.Fatalf("stale inventory: %+v", b)
	}

	// A GPU count Loomplane couldn't read is unknown, never zero in use.
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_cloud_capacity_inventory SET gpus=NULL,gpu_kind='',body=(body-'gpus'-'gpu_kind')||jsonb_build_object('checked_at',now())`); err != nil {
		t.Fatal(err)
	}
	if b = ensureBlock(t, f, h100Demand(f, t, "c")); b.Reason != "stale_inventory" || b.Resource != "gpus:nvidia-h100-80gb" {
		t.Fatalf("unknown GPU count: %+v", b)
	}
	if usage, err = f.store.GetCapacityUsage(ctx, "org", "cloud"); err != nil || len(usage.GPUs) != 1 || usage.GPUs[0].Provisioned != nil {
		t.Fatalf("unknown GPU usage: %+v %v", usage, err)
	}
	if f.provider.creates != 0 {
		t.Fatalf("created %d servers past the GPU ceiling", f.provider.creates)
	}
}

func TestGPUCapacityMigrationRoundTrip(t *testing.T) {
	f := newGPUPoolFixture(t)
	ctx := t.Context()
	// An existing CPU reservation and an inventory row from before GPUs.
	r := f.request("env", "api")
	p, err := f.store.PreviewPlacement(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	f.plan(t, "cpu-plan", r, p)
	f.accept(t, "cpu-plan")
	g, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	host, err := f.store.GetServer(ctx, "org", "cloud", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	record := cloudinfra.CapacityRecord{Kind: "server", Provider: "digitalocean", AccountID: "runtime-1", ResourceID: "server:runtime-1", ServerID: "server-1", ServerStateRevision: host.StateRevision, CheckedAt: time.Now(), Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(2 << 30), StorageBytes: inventoryNumber(0)}}
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", g, []cloudinfra.CapacityRecord{record}, nil); err != nil {
		t.Fatal(err)
	}

	m := federationMigration(t, gpuCapacityMigration)
	if err = m.Down(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE column_name IN ('gpus','gpu_kind') AND table_name IN ('loomplane_server_pool_members','loomplane_capacity_reservations','loomplane_cloud_expansions','loomplane_cloud_capacity_inventory')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("down left %d GPU columns: %v", columns, err)
	}
	if err = m.Up(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal(err)
	}
	if err = m.Up(ctx, pgmigrate.New(f.db)); err != nil {
		t.Fatal("upgrade cannot replay", err)
	}
	var kind string
	var gpus int64
	if err = f.sql.QueryRow(ctx, `SELECT gpu_kind,gpus FROM loomplane_capacity_reservations WHERE plan_id='cpu-plan'`).Scan(&kind, &gpus); err != nil || kind != "" || gpus != 0 {
		t.Fatalf("existing reservation: %q %d %v", kind, gpus, err)
	}
	if err = f.sql.QueryRow(ctx, `SELECT gpus FROM loomplane_server_pool_members WHERE pool_id='pool' AND server_id='server-1'`).Scan(&gpus); err != nil || gpus != 0 {
		t.Fatalf("existing member: %d %v", gpus, err)
	}
	// An inventory row from before GPUs has no count: unknown, not zero.
	var unknown bool
	if err = f.sql.QueryRow(ctx, `SELECT gpus IS NULL AND gpu_kind='' FROM loomplane_cloud_capacity_inventory WHERE server_id='server-1'`).Scan(&unknown); err != nil || !unknown {
		t.Fatalf("existing inventory row: %v %v", unknown, err)
	}
	// A GPU pool saved before the column existed keeps its budget in the pool
	// body, and the upgrade copies it onto the member row.
	if err = f.sql.QueryRow(ctx, `SELECT gpus FROM loomplane_server_pool_members WHERE pool_id='gpu-pool'`).Scan(&gpus); err != nil || gpus != 2 {
		t.Fatalf("GPU member after upgrade: %d %v", gpus, err)
	}

	// A downgrade would forget GPU reservations, so it's refused while any exist.
	gr := gpuRequest(f, "llm")
	gp, err := f.store.PreviewPlacement(ctx, gr)
	if err != nil {
		t.Fatal(err)
	}
	gpuPlan(t, f, "gpu-plan", gr, gp, true)
	f.accept(t, "gpu-plan")
	if err = m.Down(ctx, pgmigrate.New(f.db)); err == nil || !strings.Contains(err.Error(), "GPU reservations") {
		t.Fatalf("downgrade with GPU reservations: %v", err)
	}
}

func TestGPUDisconnectedServerKeepsGPUCount(t *testing.T) {
	f := gpuCeilingFixture(t)
	ctx := t.Context()
	// A disconnected H100 server whose inventory row predates GPU counts.
	if _, err := f.sql.Exec(ctx, `UPDATE loomplane_cloud_servers SET status='disconnected',body=jsonb_set(jsonb_set(body,'{status}','"disconnected"'),'{health}',coalesce(body->'health','{}')||'{"gpus":[{"index":0,"uuid":"GPU-0","kind":"nvidia-h100-80gb"}]}') WHERE id='h100-host'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(ctx, `UPDATE loomplane_cloud_capacity_inventory SET gpus=NULL,gpu_kind='',body=body-'gpus'-'gpu_kind'`); err != nil {
		t.Fatal(err)
	}
	zero := cloudinfra.CapacityResources{Servers: inventoryNumber(0), CPUMillis: inventoryNumber(0), MemoryBytes: inventoryNumber(0), StorageBytes: inventoryNumber(0)}
	ledgerSave(t, f, nil, []cloudinfra.CapacityAccountSnapshot{{Provider: "digitalocean", AccountID: "account", Provisioned: zero, CheckedAt: time.Now()}})
	usage, err := f.store.GetCapacityUsage(ctx, "org", "cloud")
	if err != nil || len(usage.GPUs) != 1 || usage.GPUs[0].Provisioned == nil || *usage.GPUs[0].Provisioned != 1 {
		t.Fatalf("retained H100: %+v %v", usage, err)
	}
	var gpus int64
	if err = f.sql.QueryRow(ctx, `SELECT gpus FROM loomplane_cloud_capacity_inventory WHERE server_id='h100-host'`).Scan(&gpus); err != nil || gpus != 1 {
		t.Fatalf("retained row: %d %v", gpus, err)
	}
}

func TestGPUPoolCeilingUnderCloudCeiling(t *testing.T) {
	// The pool may hold 2 H100s while the Cloud may hold 8.
	f := gpuCeilingFixtureWith(t, 8, &cloudinfra.ResourceVector{Servers: 4, CPUMillis: 8000, MemoryBytes: 8 << 30, StorageBytes: 80_000_000_000, GPUKind: "nvidia-h100-80gb", GPUs: 2})
	ctx := t.Context()
	for _, n := range []string{"a", "b"} {
		decision, err := f.svc.EnsureCapacity(ctx, h100Demand(f, t, n))
		if err != nil || decision == nil || decision.ExpansionID == "" {
			t.Fatalf("expansion %s: %+v %v", n, decision, err)
		}
		e, err := f.store.GetExpansion(ctx, "org", "cloud", decision.ExpansionID)
		if err != nil || e.OperationID == "" || e.Block != nil {
			t.Fatalf("expansion %s was not admitted: %+v %v", n, e, err)
		}
	}
	// The H100 server outside the pool counts toward the Cloud, not the pool,
	// so the Cloud has 3 of 8 and the pool 2 of 2.
	b := ensureBlock(t, f, h100Demand(f, t, "c"))
	if b.Reason != "hard_limit" || b.Resource != "gpus:nvidia-h100-80gb" || b.PoolID != "pool" || b.Limit != 2 || b.Counted != 2 || b.Requested != 1 {
		t.Fatalf("third H100 in a 2-H100 pool: %+v", b)
	}
	var reserved int
	if err := f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_capacity_reservations WHERE status='reserved'`).Scan(&reserved); err != nil || reserved != 2 {
		t.Fatalf("reservations: %d %v", reserved, err)
	}

	// Adding an H100 server to the full pool is refused the same way.
	now := time.Now().UTC()
	if err := f.connections.CreateConnection(ctx, &connections.Connection{ID: "h100-runtime", OrgID: "org", Cloud: "server", Name: "H100 host", Account: "h100", Details: map[string]string{"docker_id": "h100", "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	v, err := f.store.GetServer(ctx, "org", "cloud", "h100-host")
	if err != nil {
		t.Fatal(err)
	}
	v.RuntimeConnectionID = "h100-runtime"
	v.Health = cloudinfra.RuntimeHealth{DockerID: "h100", Architecture: "amd64", EffectiveCPUMillis: 2000, EffectiveMemoryBytes: 2 << 30, GPURuntime: provider.GPUStatusReady, GPUs: []provider.GPUDevice{{Index: 0, UUID: "GPU-0", Kind: "nvidia-h100-80gb"}}, CheckedAt: now}
	if err = f.store.SaveServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	if v, err = f.store.GetServer(ctx, "org", "cloud", "h100-host"); err != nil {
		t.Fatal(err)
	}
	record := cloudinfra.CapacityRecord{Kind: "server", Provider: "digitalocean", AccountID: "account", ResourceID: "server:h100-host", ServerID: "h100-host", ServerStateRevision: v.StateRevision, CheckedAt: time.Now(), GPUKind: "nvidia-h100-80gb", GPUs: inventoryNumber(1), Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(1 << 30), StorageBytes: inventoryNumber(10_000_000_000)}}
	zero := cloudinfra.CapacityResources{Servers: inventoryNumber(0), CPUMillis: inventoryNumber(0), MemoryBytes: inventoryNumber(0), StorageBytes: inventoryNumber(0)}
	ledgerSave(t, f, []cloudinfra.CapacityRecord{record}, []cloudinfra.CapacityAccountSnapshot{{Provider: "digitalocean", AccountID: "account", Provisioned: zero, CheckedAt: time.Now()}})
	pool, err := f.store.GetPool(ctx, "org", "cloud", "pool")
	if err != nil {
		t.Fatal(err)
	}
	previous := pool.Revision
	pool.Revision++
	pool.Members = append(pool.Members, cloudinfra.PoolMember{ServerID: "h100-host", CPUMillis: 2000, MemoryBytes: 2 << 30, GPUs: 1})
	err = f.store.SavePool(ctx, pool, previous)
	var admission *cloudinfra.CapacityAdmissionError
	if !errors.As(err, &admission) || admission.Block.Reason != "hard_limit" || admission.Block.Resource != "gpus:nvidia-h100-80gb" || admission.Block.Limit != 2 || admission.Block.Counted != 2 || admission.Block.Requested != 1 {
		t.Fatalf("H100 member past the pool ceiling: %v", err)
	}
}
