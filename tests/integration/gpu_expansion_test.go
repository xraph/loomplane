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

var (
	l4Size = provider.ServerSize{ID: "gpu-l4", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Architectures: []string{"amd64"}, Regions: []string{"region"},
		Accelerator: &provider.Accelerator{Kind: "nvidia-l4", Vendor: "nvidia", Count: 1, MemoryGiB: 24}}
	h100Size = provider.ServerSize{ID: "gpu-h100x1-80gb", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, HourlyUSD: 3.39, HourlyPriceKnown: true, Architectures: []string{"amd64"}, Regions: []string{"region"},
		Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}}
	h100x8Size = provider.ServerSize{ID: "gpu-h100x8-640gb", CPUs: 16, MemoryMiB: 16384, DiskGB: 160, HourlyUSD: 23.92, HourlyPriceKnown: true, Architectures: []string{"amd64"}, Regions: []string{"region"},
		Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 8, MemoryGiB: 80}}
)

// gpuExpansionProvider offers a plain image, a GPU-ready one and the test's sizes.
type gpuExpansionProvider struct {
	*infraProvider
	sizes        []provider.ServerSize
	optionsCalls int
}

func (p *gpuExpansionProvider) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	p.optionsCalls++
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}},
		Images: []provider.ServerImage{
			{ID: "ubuntu", Bootstrap: true, Architecture: "amd64", Regions: []string{"region"}},
			{ID: "gpu-base", Bootstrap: true, Architecture: "amd64", Regions: []string{"region"}, Accelerators: []string{"nvidia"}},
		},
		Sizes: append([]provider.ServerSize(nil), p.sizes...)}, nil
}
func (p *gpuExpansionProvider) CapacityInventory(ctx context.Context, c provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	return expansionProvider{p.infraProvider}.CapacityInventory(ctx, c)
}

// gpuExpansionRuntime reports one GPU of kind and whatever GPU runtime state the test sets.
type gpuExpansionRuntime struct {
	*infraRuntime
	gpuRuntime string
	kind       string
}

func (r *gpuExpansionRuntime) Inspect(ctx context.Context, raw []byte, settings map[string]string, vacant bool) (cloudinfra.RuntimeHealth, error) {
	h, err := r.infraRuntime.Inspect(ctx, raw, settings, vacant)
	h.EffectiveCPUMillis = 2000
	h.EffectiveMemoryBytes = 2 << 30
	h.GPURuntime = r.gpuRuntime
	if r.kind == "" {
		return h, err
	}
	title := r.kind
	if k, ok := provider.LookupAcceleratorKind(r.kind); ok {
		title = k.Title
	}
	h.GPUs = []provider.GPUDevice{{Index: 0, UUID: "GPU-0", Kind: r.kind, Product: title, MemoryMiB: 23034}}
	return h, err
}

// gpuServerFixture registers a provider that sells sizes and a runtime that reports gpuRuntime.
func gpuServerFixture(t *testing.T, gpuRuntime string, sizes ...provider.ServerSize) (*infraFixture, *gpuExpansionProvider, *gpuExpansionRuntime) {
	t.Helper()
	f := newInfraFixture(t)
	p := &gpuExpansionProvider{infraProvider: f.provider, sizes: sizes}
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", p); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	runtime := &gpuExpansionRuntime{infraRuntime: f.runtime, gpuRuntime: gpuRuntime}
	if sizes[0].Accelerator != nil {
		runtime.kind = sizes[0].Accelerator.Kind
	}
	f.svc.Runtime = runtime
	return f, p, runtime
}

func gpuExpansionFixture(t *testing.T) (*infraFixture, *gpuExpansionRuntime, cloudinfra.CapacityDemand) {
	t.Helper()
	return gpuExpansionFixtureFor(t, l4Size, provider.GPUStatusToolkitMissing, false)
}

// gpuExpansionFixtureFor makes an empty GPU pool that grows by size, with an approved GPU demand.
func gpuExpansionFixtureFor(t *testing.T, size provider.ServerSize, gpuRuntime string, approval bool) (*infraFixture, *gpuExpansionRuntime, cloudinfra.CapacityDemand) {
	t.Helper()
	f, _, runtime := gpuServerFixture(t, gpuRuntime, size)
	f.svc.EnableAutomaticCapacity = true
	kind := size.Accelerator.Kind
	pool := &cloudinfra.Pool{ID: "pool", OrgID: "org", CloudID: "cloud", Name: "GPU", Revision: 1, AcceleratorKind: kind, Members: []cloudinfra.PoolMember{}}
	if err := f.store.SavePool(t.Context(), pool, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.svc.Clouds.(*cloudprofiles.PgStore).CreateTarget(t.Context(), &cloudprofiles.Target{ID: "pool-target", OrgID: "org", CloudID: "cloud", ProjectID: "proj", Name: "pool", DisplayName: "Pool", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{Enabled: true, RequireExpansionApproval: approval, Limits: cloudinfra.ResourceVector{Servers: 4, CPUMillis: 8000, MemoryBytes: 8 << 30, StorageBytes: 80_000_000_000}, GPULimits: []cloudinfra.GPULimit{{Kind: kind, Maximum: 4}},
		Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "pool", AcceleratorKind: kind, ConnectionID: "infra-account", Region: "region", Architecture: "amd64", Image: "gpu-base", AllowedSizes: []string{size.ID}, MaxServers: 4, SystemReserve: cloudinfra.ResourceVector{CPUMillis: 500, MemoryBytes: 512 << 20}}}}
	if _, err := f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: policy}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RefreshCapacity(t.Context(), f.p, "cloud"); err != nil {
		t.Fatal(err)
	}
	f.svc.CapacityDemandAuthority = &expansionAuthority{db: f.sql}
	f.svc.CapacityDemandWakeup = &expansionWakeup{}
	d := cloudinfra.CapacityDemand{OrgID: "org", CloudID: "cloud", PoolID: "pool", TargetID: "pool-target", ProjectID: "proj", EnvironmentID: "env", PlanID: "capacity-plan", Service: "app", Architecture: "amd64", Resources: cloudinfra.ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: kind, GPUs: 1}, PolicyRevision: 1, IntentHash: "approved-demand"}
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

// A GPU Droplet whose NVIDIA toolkit never came up costs money while it can't
// take GPU work. It still enrolls, shows its hourly price and says why.
func TestEnrollmentRecordsGPURuntime(t *testing.T) {
	f, p, _ := gpuServerFixture(t, provider.GPUStatusToolkitMissing, h100Size, h100x8Size)
	ctx := t.Context()
	in := cloudinfra.ServerInput{Kind: "create", Name: "gpu", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: h100Size.ID, Image: "ubuntu"}
	if _, err := f.svc.PreviewServer(ctx, f.p, "cloud", in); !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), "A GPU server needs a GPU-ready image such as gpu-base.") {
		t.Fatalf("GPU size on a plain image: %v", err)
	}
	in.Image = "gpu-base"
	preview, err := f.svc.PreviewServer(ctx, f.p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	// The approval shows the hourly price and the GPUs before anything is bought.
	if preview.EstimatedHourlyUSD != 3.39 || !preview.HourlyPriceKnown || preview.Accelerator == nil || preview.Accelerator.Kind != "nvidia-h100-80gb" || preview.Accelerator.Count != 1 {
		t.Fatalf("GPU preview: %v %v %+v", preview.EstimatedHourlyUSD, preview.HourlyPriceKnown, preview.Accelerator)
	}
	var userData string
	p.beforeCreate = func(spec provider.ServerSpec) {
		if !spec.ReconcileOnly {
			userData = spec.UserData
		}
	}
	o, err := f.svc.ApplyServerOperation(ctx, f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review})
	if err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	// The Droplet's user data sets up the NVIDIA toolkit before it enrolls.
	if o.Status != "waiting" || !strings.Contains(userData, `["nvidia-ctk","runtime","configure","--runtime=docker"]`) {
		t.Fatalf("GPU create: %s, toolkit configured: %v", o.Status, strings.Contains(userData, "nvidia-ctk"))
	}
	// The Droplet is running and billing before it enrolls, so its price is already on it.
	server, err := f.store.GetServer(ctx, "org", "cloud", o.ServerID)
	if err != nil || server.Status != "provisioning" || server.HourlyUSD != 3.39 || !server.HourlyPriceKnown || server.Accelerator == nil {
		t.Fatalf("provisioning GPU server: %+v %v", server, err)
	}

	o.HostKey = infraFixtureHostKey(t)
	o.Enrolled = true
	if err = f.store.SaveOperation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if o = f.reconcile(t, o); o.Status != "succeeded" {
		t.Fatalf("enrolment: %+v", o)
	}
	server, err = f.store.GetServer(ctx, "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if server.Status != "ready" || server.Health.GPURuntime != provider.GPUStatusToolkitMissing || server.Health.GPUMessage == "" {
		t.Fatalf("unready GPU server: %s %+v", server.Status, server.Health)
	}
	if server.Accelerator == nil || server.Accelerator.Kind != "nvidia-h100-80gb" || server.HourlyUSD != 3.39 || !server.HourlyPriceKnown {
		t.Fatalf("GPU server cost: %+v %v %v", server.Accelerator, server.HourlyUSD, server.HourlyPriceKnown)
	}
	// Planning reads the runtime connection, which carries the same reason.
	conn, err := f.connections.GetConnection(ctx, "org", server.RuntimeConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	inv, ok := provider.ParseGPUInventory(conn.Details)
	if !ok || inv.Status != provider.GPUStatusToolkitMissing || inv.Message != server.Health.GPUMessage || len(inv.Devices) != 1 {
		t.Fatalf("runtime connection GPU inventory: %+v %v", inv, conn.Details)
	}
	if _, placeErr := inv.Place(provider.GPURequest{Kind: "nvidia-h100-80gb", Count: 1}); placeErr == nil || placeErr.Error() != server.Health.GPUMessage {
		t.Fatalf("an unready GPU server fits GPU work: %v", placeErr)
	}

	// A resize review and the resized server carry the new size's price and GPUs.
	resize, err := f.svc.PreviewServer(ctx, f.p, "cloud", cloudinfra.ServerInput{Kind: "resize", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision, Size: h100x8Size.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resize.EstimatedHourlyUSD != 23.92 || !resize.HourlyPriceKnown || resize.Accelerator == nil || resize.Accelerator.Count != 8 {
		t.Fatalf("GPU resize preview: %v %+v", resize.EstimatedHourlyUSD, resize.Accelerator)
	}
	if _, err = f.svc.ApplyServerOperation(ctx, f.p, "cloud", resize.ID, cloudinfra.ApplyInput{Review: resize.Review}); err != nil {
		t.Fatal(err)
	}
	if resize = f.reconcile(t, resize); resize.Status != "succeeded" {
		t.Fatalf("resize: %+v", resize)
	}
	server, err = f.store.GetServer(ctx, "org", "cloud", server.ID)
	if err != nil || server.Size != h100x8Size.ID || server.HourlyUSD != 23.92 || server.Accelerator == nil || server.Accelerator.Count != 8 {
		t.Fatalf("resized GPU server: %+v %v", server, err)
	}

	if server.PricedSize != h100x8Size.ID {
		t.Fatalf("resized server priced for %q", server.PricedSize)
	}
	// Inventory refresh prices a server whose price is unknown from the catalog.
	server.HourlyUSD, server.HourlyPriceKnown, server.Accelerator = 0, false, nil
	if err = f.store.SaveServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RefreshCapacity(ctx, f.p, "cloud"); err != nil {
		t.Fatal(err)
	}
	server, err = f.store.GetServer(ctx, "org", "cloud", server.ID)
	if err != nil || server.HourlyUSD != 23.92 || !server.HourlyPriceKnown || server.Accelerator == nil || server.Accelerator.Count != 8 {
		t.Fatalf("refreshed GPU server price: %+v %v", server, err)
	}
}

func TestExpansionCarriesHourlyCost(t *testing.T) {
	t.Run("known", func(t *testing.T) {
		f, _, d := gpuExpansionFixtureFor(t, h100Size, provider.GPUStatusReady, true)
		e := ensureExpansion(t, f, d)
		// The expansion waits for approval with its hourly price and GPUs on it.
		if e.Status != "awaiting_approval" || e.EstimatedHourlyUSD != 3.39 || !e.HourlyPriceKnown || e.Accelerator == nil || e.Accelerator.Kind != "nvidia-h100-80gb" || e.Accelerator.Count != 1 {
			t.Fatalf("GPU expansion cost: %s %v %v %+v", e.Status, e.EstimatedHourlyUSD, e.HourlyPriceKnown, e.Accelerator)
		}
		approved, err := f.svc.ApproveExpansion(t.Context(), f.p, "cloud", e.ID, e.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if approved.EstimatedHourlyUSD != 3.39 || !approved.HourlyPriceKnown || approved.Accelerator == nil {
			t.Fatalf("approved expansion lost its cost: %+v", approved)
		}
		o, err := f.store.GetOperation(t.Context(), "org", "cloud", approved.OperationID)
		if err != nil || o.EstimatedHourlyUSD != 3.39 || !o.HourlyPriceKnown || o.Accelerator == nil || o.Accelerator.Kind != "nvidia-h100-80gb" {
			t.Fatalf("expansion operation cost: %+v %v", o, err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		// The catalog has no price for this size, and the expansion says so
		// rather than showing a free server.
		f, _, d := gpuExpansionFixtureFor(t, l4Size, provider.GPUStatusReady, true)
		e := ensureExpansion(t, f, d)
		if e.Status != "awaiting_approval" || e.HourlyPriceKnown || e.EstimatedHourlyUSD != 0 || e.Accelerator == nil || e.Accelerator.Kind != "nvidia-l4" {
			t.Fatalf("unpriced GPU expansion: %s %v %v %+v", e.Status, e.EstimatedHourlyUSD, e.HourlyPriceKnown, e.Accelerator)
		}
	})
}

// With the GPU runtime known at enrolment, a ready GPU Droplet joins its pool on
// the next pass, without anyone checking the server.
func TestGPUExpansionJoinsWhenReadyAtEnrollment(t *testing.T) {
	f, runtime, d := gpuExpansionFixtureFor(t, l4Size, provider.GPUStatusReady, false)
	e := ensureExpansion(t, f, d)
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
	calls := runtime.calls
	reconcileExpansion(t, f)
	e, err = f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
	if err != nil || e.Status != "ready" || e.Block != nil {
		t.Fatalf("ready GPU expansion: %+v %v", e, err)
	}
	pool, err := f.store.GetPool(t.Context(), "org", "cloud", "pool")
	if err != nil || len(pool.Members) != 1 || pool.Members[0].GPUs != 1 {
		t.Fatalf("GPU member: %+v %v", pool, err)
	}
	server, err := f.store.GetServer(t.Context(), "org", "cloud", pool.Members[0].ServerID)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := f.connections.GetConnection(t.Context(), "org", server.RuntimeConnectionID)
	if err != nil || conn.Details[provider.GPUDetailStatus] != provider.GPUStatusReady {
		t.Fatalf("runtime connection GPU status: %+v %v", conn, err)
	}
	if runtime.calls == calls {
		t.Fatal("the pass never inspected the enrolled server")
	}
}

// pricingConflictStore fails the next priced save of a server, as if another
// writer changed the server during the refresh.
type pricingConflictStore struct {
	*cloudinfra.PgStore
	conflicts int
}

func (s *pricingConflictStore) SaveServer(ctx context.Context, v *cloudinfra.Server) error {
	if s.conflicts > 0 && v.HourlyPriceKnown {
		s.conflicts--
		return cloudinfra.ErrConflict
	}
	return s.PgStore.SaveServer(ctx, v)
}

// Inventory refresh runs every 30 seconds per Cloud. A Cloud whose servers are
// priced never reads the provider catalog, and a price that can't be saved
// never fails the refresh.
func TestRefreshReadsCatalogOnlyForUnpricedServers(t *testing.T) {
	cpu := provider.ServerSize{ID: "s-2vcpu-2gb", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, HourlyUSD: 0.03, HourlyPriceKnown: true, Architectures: []string{"amd64"}, Regions: []string{"region"}}
	f, p, _ := gpuServerFixture(t, provider.GPUStatusNone, cpu)
	ctx := t.Context()
	preview, err := f.svc.PreviewServer(ctx, f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "cpu", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: cpu.ID, Image: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.svc.ApplyServerOperation(ctx, f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review})
	if err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	o.HostKey, o.Enrolled = infraFixtureHostKey(t), true
	if err = f.store.SaveOperation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if o = f.reconcile(t, o); o.Status != "succeeded" {
		t.Fatalf("create: %+v", o)
	}
	calls := p.optionsCalls
	for range 3 {
		if _, err = f.svc.RefreshCapacity(ctx, f.p, "cloud"); err != nil {
			t.Fatal(err)
		}
	}
	if p.optionsCalls != calls {
		t.Fatalf("priced CPU Cloud read the catalog %d times", p.optionsCalls-calls)
	}

	// A server without a price (one priced before prices were recorded, say)
	// costs one catalog read, then none.
	unprice := func() {
		t.Helper()
		v, getErr := f.store.GetServer(ctx, "org", "cloud", o.ServerID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		v.HourlyUSD, v.HourlyPriceKnown, v.PricedSize = 0, false, ""
		if getErr = f.store.SaveServer(ctx, v); getErr != nil {
			t.Fatal(getErr)
		}
	}
	price := func() *cloudinfra.Server {
		t.Helper()
		v, getErr := f.store.GetServer(ctx, "org", "cloud", o.ServerID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		return v
	}
	unprice()
	for range 2 {
		if _, err = f.svc.RefreshCapacity(ctx, f.p, "cloud"); err != nil {
			t.Fatal(err)
		}
	}
	if v := price(); p.optionsCalls != calls+1 || v.HourlyUSD != 0.03 || !v.HourlyPriceKnown || v.PricedSize != cpu.ID {
		t.Fatalf("unpriced server: %d catalog reads, %+v", p.optionsCalls-calls, v)
	}

	// A price save that loses a race is skipped. The refresh succeeds and the
	// next one prices the server from the catalog it already read this hour.
	unprice()
	f.svc.Store = &pricingConflictStore{PgStore: f.store, conflicts: 1}
	if _, err = f.svc.RefreshCapacity(ctx, f.p, "cloud"); err != nil {
		t.Fatalf("a price conflict failed the refresh: %v", err)
	}
	if v := price(); v.HourlyPriceKnown {
		t.Fatalf("conflicted price was saved: %+v", v)
	}
	if _, err = f.svc.RefreshCapacity(ctx, f.p, "cloud"); err != nil {
		t.Fatal(err)
	}
	if v := price(); !v.HourlyPriceKnown || v.HourlyUSD != 0.03 || p.optionsCalls != calls+1 {
		t.Fatalf("price after the conflict: %d catalog reads, %+v", p.optionsCalls-calls, v)
	}
}
