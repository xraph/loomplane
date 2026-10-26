//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/pkg/provider"
)

type gpuExpansionProvider struct{ *infraProvider }

func (p gpuExpansionProvider) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}},
		Images: []provider.ServerImage{
			{ID: "ubuntu", Bootstrap: true, Architecture: "amd64", Regions: []string{"region"}},
			{ID: "gpu-base", Bootstrap: true, Architecture: "amd64", Regions: []string{"region"}, Accelerators: []string{"nvidia"}},
		},
		Sizes: []provider.ServerSize{{ID: "gpu-l4", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Architectures: []string{"amd64"}, Regions: []string{"region"},
			Accelerator: &provider.Accelerator{Kind: "nvidia-l4", Vendor: "nvidia", Count: 1, MemoryGiB: 24}}}}, nil
}
func (p gpuExpansionProvider) CapacityInventory(ctx context.Context, c provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	return expansionProvider(p).CapacityInventory(ctx, c)
}

// gpuExpansionRuntime reports one L4 and whatever GPU runtime state the test sets.
type gpuExpansionRuntime struct {
	*infraRuntime
	gpuRuntime string
}

func (r *gpuExpansionRuntime) Inspect(ctx context.Context, raw []byte, settings map[string]string, vacant bool) (cloudinfra.RuntimeHealth, error) {
	h, err := r.infraRuntime.Inspect(ctx, raw, settings, vacant)
	h.EffectiveCPUMillis = 2000
	h.EffectiveMemoryBytes = 2 << 30
	h.GPURuntime = r.gpuRuntime
	h.GPUs = []provider.GPUDevice{{Index: 0, UUID: "GPU-0", Kind: "nvidia-l4", Product: "NVIDIA L4", MemoryMiB: 23034}}
	return h, err
}

func gpuExpansionFixture(t *testing.T) (*infraFixture, *gpuExpansionRuntime, cloudinfra.CapacityDemand) {
	t.Helper()
	f := newInfraFixture(t)
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", gpuExpansionProvider{f.provider}); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	f.svc.EnableAutomaticCapacity = true
	runtime := &gpuExpansionRuntime{infraRuntime: f.runtime, gpuRuntime: provider.GPUStatusToolkitMissing}
	f.svc.Runtime = runtime
	pool := &cloudinfra.Pool{ID: "pool", OrgID: "org", CloudID: "cloud", Name: "L4", Revision: 1, AcceleratorKind: "nvidia-l4", Members: []cloudinfra.PoolMember{}}
	if err := f.store.SavePool(t.Context(), pool, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.svc.Clouds.(*cloudprofiles.PgStore).CreateTarget(t.Context(), &cloudprofiles.Target{ID: "pool-target", OrgID: "org", CloudID: "cloud", ProjectID: "proj", Name: "pool", DisplayName: "Pool", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{Enabled: true, Limits: cloudinfra.ResourceVector{Servers: 4, CPUMillis: 8000, MemoryBytes: 8 << 30, StorageBytes: 80_000_000_000}, GPULimits: []cloudinfra.GPULimit{{Kind: "nvidia-l4", Maximum: 4}},
		Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "pool", AcceleratorKind: "nvidia-l4", ConnectionID: "infra-account", Region: "region", Architecture: "amd64", Image: "gpu-base", AllowedSizes: []string{"gpu-l4"}, MaxServers: 4, SystemReserve: cloudinfra.ResourceVector{CPUMillis: 500, MemoryBytes: 512 << 20}}}}
	if _, err := f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: policy}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RefreshCapacity(t.Context(), f.p, "cloud"); err != nil {
		t.Fatal(err)
	}
	f.svc.CapacityDemandAuthority = &expansionAuthority{db: f.sql}
	f.svc.CapacityDemandWakeup = &expansionWakeup{}
	d := cloudinfra.CapacityDemand{OrgID: "org", CloudID: "cloud", PoolID: "pool", TargetID: "pool-target", ProjectID: "proj", EnvironmentID: "env", PlanID: "capacity-plan", Service: "app", Architecture: "amd64", Resources: cloudinfra.ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-l4", GPUs: 1}, PolicyRevision: 1, IntentHash: "approved-demand"}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('capacity-manifest','org','proj',1,'version: 1','capacity','manager',now())`); err != nil {
		t.Fatal(err)
	}
	expansionPlan(t, f, d)
	return f, runtime, d
}

func TestGPUExpansionEnrollsIntoGPUPool(t *testing.T) {
	f, runtime, d := gpuExpansionFixture(t)
	e := ensureExpansion(t, f, d)
	if e.Size != "gpu-l4" || e.Resources.GPUKind != "nvidia-l4" || e.Resources.GPUs != 1 {
		t.Fatalf("GPU expansion: %+v", e)
	}
	reconcileExpansion(t, f)
	o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	o.Enrolled = true
	o.HostKey = infraFixtureHostKey(t)
	if err = f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}

	// The Droplet exists but its container runtime can't use the GPU yet. The
	// pass succeeds, the expansion waits in enrolling with the reason on it.
	reconcileExpansion(t, f)
	e, err = f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
	if err != nil || e.Status != "enrolling" || e.Block == nil || e.Block.Reason != "provisioning" || e.Block.Resource != "gpu_runtime" || !e.Block.Retryable {
		t.Fatalf("waiting on the GPU runtime: %+v %+v %v", e, e.Block, err)
	}
	revision := e.Revision
	reconcileExpansion(t, f)
	if e, err = f.store.GetExpansion(t.Context(), "org", "cloud", e.ID); err != nil || e.Revision != revision {
		t.Fatalf("an unchanged wait rewrote the expansion: %d to %d %v", revision, e.Revision, err)
	}
	pool, err := f.store.GetPool(t.Context(), "org", "cloud", "pool")
	if err != nil || len(pool.Members) != 0 {
		t.Fatalf("joined before the GPU runtime was ready: %+v %v", pool, err)
	}

	// A server check that reports the runtime ready lets the next pass enrol it.
	runtime.gpuRuntime = provider.GPUStatusReady
	if _, err = f.svc.CheckServer(t.Context(), f.p, "cloud", o.ServerID); err != nil {
		t.Fatal(err)
	}
	reconcileExpansion(t, f)
	e, err = f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
	if err != nil || e.Status != "ready" || e.Block != nil {
		t.Fatalf("enrolled expansion: %+v %v", e, err)
	}
	pool, err = f.store.GetPool(t.Context(), "org", "cloud", "pool")
	if err != nil || len(pool.Members) != 1 || pool.Members[0].GPUs != 1 || pool.Members[0].CPUMillis != 1500 {
		t.Fatalf("GPU member: %+v %v", pool, err)
	}
}

func TestPoolAndTemplateGPUKindsStayEqual(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	if _, err := f.sql.Exec(ctx, `INSERT INTO loomplane_server_pools(id,org_id,cloud_id,revision,body) VALUES('pool','org','cloud',1,'{"id":"pool","org_id":"org","cloud_id":"cloud","name":"CPU","revision":1,"members":[]}')`); err != nil {
		t.Fatal(err)
	}
	template := cloudinfra.PoolExpansionPolicy{PoolID: "pool", ConnectionID: "infra-account", Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"small"}, MaxServers: 1}
	gpuTemplate := template
	gpuTemplate.AcceleratorKind = "nvidia-l4"
	// A policy can't point a GPU template at a CPU pool.
	_, err := f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{Pools: []cloudinfra.PoolExpansionPolicy{gpuTemplate}}})
	if !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), "pool pool holds CPU servers but its expansion template buys nvidia-l4 servers") {
		t.Fatalf("policy drift: %v", err)
	}
	if _, err = f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{Pools: []cloudinfra.PoolExpansionPolicy{template}}}); err != nil {
		t.Fatal(err)
	}
	// Nor can a pool with members turn into a GPU pool under its CPU template.
	_, err = f.svc.SavePool(ctx, f.p, "cloud", "pool", cloudinfra.PoolInput{Name: "GPU", Revision: 1, AcceleratorKind: "nvidia-l4", Members: []cloudinfra.PoolMember{{ServerID: "server", CPUMillis: 1000, MemoryBytes: 1 << 30, GPUs: 1}}})
	if !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), "pool pool holds nvidia-l4 servers but its expansion template buys CPU servers") {
		t.Fatalf("pool drift: %v", err)
	}
	// The store checks again inside its transaction, under the Cloud lock policy saves take.
	err = f.store.SavePool(ctx, &cloudinfra.Pool{ID: "pool", OrgID: "org", CloudID: "cloud", Name: "GPU", Revision: 2, AcceleratorKind: "nvidia-l4", Members: []cloudinfra.PoolMember{}}, 1)
	if !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), "expansion template buys CPU servers") {
		t.Fatalf("store drift: %v", err)
	}
}
