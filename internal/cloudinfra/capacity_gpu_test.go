package cloudinfra

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

var h100 = provider.ServerSize{ID: "gpu-h100x1-80gb", CPUs: 20, MemoryMiB: 245760, DiskGB: 720, HourlyUSD: 3.39, HourlyPriceKnown: true, Regions: []string{"tor1"},
	Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}}
var big = provider.ServerSize{ID: "c-32", CPUs: 32, MemoryMiB: 65536, DiskGB: 400, HourlyUSD: 1.0, HourlyPriceKnown: true, Regions: []string{"tor1"}}

func gpuOptions() provider.ServerOptions {
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "tor1"}}, Sizes: []provider.ServerSize{big, h100},
		Images: []provider.ServerImage{
			{ID: "ubuntu-24-04-x64", Architecture: "amd64", Bootstrap: true, Regions: []string{"tor1"}},
			{ID: "gpu-h100x1-base", Architecture: "amd64", Bootstrap: true, Regions: []string{"tor1"}, Accelerators: []string{"nvidia"}},
		}}
}

func TestSelectExpansionSizeNeverPicksGPUForCPUDemand(t *testing.T) {
	pool := PoolExpansionPolicy{PoolID: "pool", Region: "tor1", Image: "gpu-h100x1-base", Architecture: "amd64", AllowedSizes: []string{"c-32", "gpu-h100x1-80gb"}}
	got, err := SelectExpansionSize(gpuOptions(), pool, CapacityDemand{Resources: ResourceVector{CPUMillis: 16000, MemoryBytes: 32 << 30}})
	if err != nil || got != "c-32" {
		t.Fatalf("CPU demand picked %q %v", got, err)
	}
	// Even when only the GPU size is large enough, CPU work waits rather than renting a GPU.
	var placement *CapacityPlacementError
	if got, err = SelectExpansionSize(gpuOptions(), pool, CapacityDemand{Resources: ResourceVector{CPUMillis: 18000, MemoryBytes: 128 << 30}}); !errors.As(err, &placement) {
		t.Fatalf("CPU demand too big for c-32 picked %q %v", got, err)
	}
}

func TestSelectExpansionSizeForGPUDemand(t *testing.T) {
	pool := PoolExpansionPolicy{PoolID: "pool", AcceleratorKind: "nvidia-h100-80gb", Region: "tor1", Image: "gpu-h100x1-base", Architecture: "amd64", AllowedSizes: []string{"gpu-h100x1-80gb"}}
	if got, err := SelectExpansionSize(gpuOptions(), pool, CapacityDemand{Resources: ResourceVector{CPUMillis: 4000, MemoryBytes: 16 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 1}}); err != nil || got != "gpu-h100x1-80gb" {
		t.Fatalf("GPU demand: %q %v", got, err)
	}
	var placement *CapacityPlacementError
	if _, err := SelectExpansionSize(gpuOptions(), pool, CapacityDemand{Resources: ResourceVector{CPUMillis: 4000, MemoryBytes: 16 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 2}}); !errors.As(err, &placement) {
		t.Fatalf("two H100s on a one-GPU size: %v", err)
	}
	if _, err := SelectExpansionSize(gpuOptions(), pool, CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30}}); !errors.As(err, &placement) {
		t.Fatalf("GPU pool took CPU work: %v", err)
	}
}

func TestSelectExpansionSizeGPUEdges(t *testing.T) {
	gpuPool := PoolExpansionPolicy{PoolID: "pool", AcceleratorKind: "nvidia-h100-80gb", Region: "tor1", Image: "gpu-h100x1-base", Architecture: "amd64", AllowedSizes: []string{"gpu-h100x1-80gb"}}
	cpuPool := PoolExpansionPolicy{PoolID: "pool", Region: "tor1", Image: "ubuntu-24-04-x64", Architecture: "amd64", AllowedSizes: []string{"c-32"}}
	var placement *CapacityPlacementError
	// A pool minimum and a catalog recheck pass an empty demand; it names no workload.
	if got, err := SelectExpansionSize(gpuOptions(), gpuPool, CapacityDemand{}); err != nil || got != "gpu-h100x1-80gb" {
		t.Fatalf("empty demand on a GPU pool: %q %v", got, err)
	}
	if _, err := SelectExpansionSize(gpuOptions(), cpuPool, CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 1}}); !errors.As(err, &placement) {
		t.Fatalf("CPU pool took GPU work: %v", err)
	}
	if _, err := SelectExpansionSize(gpuOptions(), gpuPool, CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-l4", GPUs: 1}}); !errors.As(err, &placement) {
		t.Fatalf("H100 pool took L4 work: %v", err)
	}
	plain := gpuPool
	plain.Image = "ubuntu-24-04-x64"
	if _, err := SelectExpansionSize(gpuOptions(), plain, CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 1}}); !errors.As(err, &placement) {
		t.Fatalf("GPU size chosen with a plain image: %v", err)
	}
	if _, err := SelectExpansionSize(gpuOptions(), gpuPool, CapacityDemand{Resources: ResourceVector{GPUs: 1}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("GPU count without a kind: %v", err)
	}

	h100x8 := provider.ServerSize{ID: "gpu-h100x8-640gb", CPUs: 160, MemoryMiB: 1966080, DiskGB: 2046, HourlyUSD: 23.92, HourlyPriceKnown: true, Regions: []string{"tor1"},
		Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 8, MemoryGiB: 80}}
	options := gpuOptions()
	options.Sizes = []provider.ServerSize{h100x8, big, h100}
	wide := gpuPool
	wide.AllowedSizes = []string{"gpu-h100x8-640gb", "gpu-h100x1-80gb"}
	if got, err := SelectExpansionSize(options, wide, CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 1}}); err != nil || got != "gpu-h100x1-80gb" {
		t.Fatalf("one H100 picked %q %v", got, err)
	}
	if got, err := SelectExpansionSize(options, wide, CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 2}}); err != nil || got != "gpu-h100x8-640gb" {
		t.Fatalf("two H100s picked %q %v", got, err)
	}
}

func TestPolicyRefusesGPUSizesInCPUPools(t *testing.T) {
	policy := CapacityPolicy{Enabled: true, Limits: ResourceVector{Servers: 2, CPUMillis: 64000, MemoryBytes: 1 << 40, StorageBytes: 1 << 40},
		Pools: []PoolExpansionPolicy{{PoolID: "pool", ConnectionID: "do", Region: "tor1", Image: "gpu-h100x1-base", Architecture: "amd64", AllowedSizes: []string{"gpu-h100x1-80gb"}, MaxServers: 1}}}
	if err := validateCapacityPolicy(policy, map[string]provider.ServerOptions{"pool": gpuOptions()}); err == nil || !strings.Contains(err.Error(), "GPU sizes need a GPU pool") {
		t.Fatalf("CPU pool with a GPU size: %v", err)
	}
	policy.Pools[0].AcceleratorKind = "nvidia-h100-80gb"
	if err := validateCapacityPolicy(policy, map[string]provider.ServerOptions{"pool": gpuOptions()}); err == nil || !strings.Contains(err.Error(), "GPU ceiling") {
		t.Fatalf("GPU pool without a GPU ceiling: %v", err)
	}
	policy.GPULimits = []GPULimit{{Kind: "nvidia-h100-80gb", Maximum: 2}}
	if err := validateCapacityPolicy(policy, map[string]provider.ServerOptions{"pool": gpuOptions()}); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyGPUPoolRules(t *testing.T) {
	base := func() CapacityPolicy {
		return CapacityPolicy{Enabled: true, Limits: ResourceVector{Servers: 2, CPUMillis: 64000, MemoryBytes: 1 << 40, StorageBytes: 1 << 40},
			GPULimits: []GPULimit{{Kind: "nvidia-h100-80gb", Maximum: 2}},
			Pools:     []PoolExpansionPolicy{{PoolID: "pool", AcceleratorKind: "nvidia-h100-80gb", ConnectionID: "do", Region: "tor1", Image: "gpu-h100x1-base", Architecture: "amd64", AllowedSizes: []string{"gpu-h100x1-80gb"}, MaxServers: 1}}}
	}
	options := map[string]provider.ServerOptions{"pool": gpuOptions()}
	check := func(name string, policy CapacityPolicy, want string) {
		t.Helper()
		err := validateCapacityPolicy(policy, options)
		if want == "" {
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return
		}
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v, want %q", name, err, want)
		}
	}

	p := base()
	p.GPULimits[0].Maximum = 0
	check("zero ceiling", p, "A GPU pool needs a GPU ceiling for nvidia-h100-80gb.")
	if err := validateCapacityPolicy(p, options); err.Error() != "A GPU pool needs a GPU ceiling for nvidia-h100-80gb." {
		t.Fatalf("ceiling message: %q", err)
	}

	p = base()
	p.GPULimits = []GPULimit{{Kind: "nvidia-l4", Maximum: 2}}
	check("ceiling for another kind", p, "A GPU pool needs a GPU ceiling for nvidia-h100-80gb.")

	p = base()
	p.Pools[0].Image = "ubuntu-24-04-x64"
	check("plain image", p, "GPU-ready image")

	p = base()
	p.Pools[0].AllowedSizes = []string{"c-32", "gpu-h100x1-80gb"}
	check("CPU size in a GPU pool", p, "nvidia-h100-80gb")

	p = base()
	p.GPULimits = append(p.GPULimits, GPULimit{Kind: "nvidia-h100-80gb", Maximum: 3})
	if validateCapacityPolicyShape(p) == nil {
		t.Fatal("duplicate GPU ceiling accepted")
	}

	for _, bad := range []GPULimit{{Kind: "nvidia-made-up", Maximum: 1}, {Kind: "amd-mi300x", Maximum: 1}, {Kind: "nvidia-l4", Maximum: -1}, {Kind: "", Maximum: 1}} {
		p = base()
		p.GPULimits = append(p.GPULimits, bad)
		if validateCapacityPolicyShape(p) == nil {
			t.Fatalf("bad GPU ceiling accepted: %+v", bad)
		}
	}

	p = base()
	p.Pools[0].AcceleratorKind = "amd-mi300x"
	if validateCapacityPolicyShape(p) == nil {
		t.Fatal("AMD pool accepted")
	}
	p.Pools[0].AcceleratorKind = "nvidia-made-up"
	if validateCapacityPolicyShape(p) == nil {
		t.Fatal("unknown pool kind accepted")
	}

	p = base()
	p.Pools[0].Limits = &ResourceVector{Servers: 1, CPUMillis: 64000, MemoryBytes: 1 << 40, StorageBytes: 1 << 40, GPUKind: "nvidia-l4", GPUs: 1}
	if validateCapacityPolicyShape(p) == nil {
		t.Fatal("pool ceiling for another GPU kind accepted")
	}
	p.Pools[0].Limits.GPUKind = "nvidia-h100-80gb"
	p.Pools[0].Limits.GPUs = 3
	if validateCapacityPolicyShape(p) == nil {
		t.Fatal("pool GPU ceiling above the Cloud's accepted")
	}
	p.Pools[0].Limits.GPUs = 2
	check("pool GPU ceiling within the Cloud's", p, "")

	p = base()
	p.Limits.GPUKind, p.Limits.GPUs = "nvidia-h100-80gb", 1
	if validateCapacityPolicyShape(p) == nil {
		t.Fatal("GPUs in Cloud ceilings, which belong in gpu_limits")
	}

	// The bootstrap minimum counts GPUs against the per-kind ceiling.
	p = base()
	p.Pools[0].MinServers = 1
	check("minimum within the GPU ceiling", p, "")
	if p.Pools[0].BootstrapSize != "gpu-h100x1-80gb" {
		t.Fatalf("bootstrap size: %q", p.Pools[0].BootstrapSize)
	}
	p.GPULimits[0].Maximum = 1
	p.Pools[0].MaxServers, p.Limits.Servers, p.Limits.StorageBytes = 2, 2, 1<<42
	p.Pools = append(p.Pools, p.Pools[0])
	p.Pools[1].PoolID = "pool-2"
	options["pool-2"] = gpuOptions()
	check("two GPU minima over a one-GPU ceiling", p, "within Cloud ceilings")
	p.GPULimits[0].Maximum = 2
	check("two GPU minima within a two-GPU ceiling", p, "")

	// A disabled policy keeps its GPU pools without ceilings, like CPU pools.
	p = base()
	p.Enabled = false
	p.GPULimits = nil
	check("disabled", p, "")
}

func TestCapacityVectorGPUArithmetic(t *testing.T) {
	a := ResourceVector{CPUMillis: 1, GPUKind: "nvidia-l4", GPUs: 1}
	if sum, err := addCapacity(a, a); err != nil || sum.GPUs != 2 || sum.GPUKind != "nvidia-l4" {
		t.Fatalf("same kind: %+v %v", sum, err)
	}
	if _, err := addCapacity(a, ResourceVector{GPUKind: "nvidia-t4", GPUs: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed kinds: %v", err)
	}
	if nonnegativeCapacity(ResourceVector{GPUs: 1}) || nonnegativeCapacity(ResourceVector{GPUKind: "nvidia-l4"}) {
		t.Fatal("a GPU count needs exactly one kind")
	}
	if !capacityFits(a, ResourceVector{CPUMillis: 1, GPUKind: "nvidia-l4", GPUs: 1}) || capacityFits(a, ResourceVector{CPUMillis: 1, GPUKind: "nvidia-t4", GPUs: 8}) {
		t.Fatal("fit compares kind and count")
	}
}

func TestCapacityVectorGPUArithmeticEdges(t *testing.T) {
	a := ResourceVector{CPUMillis: 1, GPUKind: "nvidia-l4", GPUs: 1}
	if sum, err := addCapacity(a, ResourceVector{CPUMillis: 2}); err != nil || sum != (ResourceVector{CPUMillis: 3, GPUKind: "nvidia-l4", GPUs: 1}) {
		t.Fatalf("one side without GPUs: %+v %v", sum, err)
	}
	if sum, err := addCapacity(ResourceVector{CPUMillis: 2}, a); err != nil || sum.GPUKind != "nvidia-l4" || sum.GPUs != 1 {
		t.Fatalf("other side without GPUs: %+v %v", sum, err)
	}
	if _, err := addCapacity(ResourceVector{GPUs: 1}, ResourceVector{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("kindless GPUs added: %v", err)
	}
	if _, err := addCapacity(ResourceVector{GPUKind: "nvidia-l4", GPUs: 1 << 61}, ResourceVector{GPUKind: "nvidia-l4", GPUs: 1 << 61}); err != nil {
		t.Fatalf("large sum: %v", err)
	}
	if _, err := addCapacity(ResourceVector{GPUKind: "nvidia-l4", GPUs: 1<<63 - 1}, ResourceVector{GPUKind: "nvidia-l4", GPUs: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow: %v", err)
	}
	if nonnegativeCapacity(ResourceVector{GPUKind: "nvidia-l4", GPUs: -1}) {
		t.Fatal("negative GPUs")
	}
	if !nonnegativeCapacity(a) || !nonnegativeCapacity(ResourceVector{}) {
		t.Fatal("valid vectors refused")
	}
	if got, err := multiplyCapacity(a, 3); err != nil || got != (ResourceVector{CPUMillis: 3, GPUKind: "nvidia-l4", GPUs: 3}) {
		t.Fatalf("scale: %+v %v", got, err)
	}
	if got, err := multiplyCapacity(a, 0); err != nil || got != (ResourceVector{}) {
		t.Fatalf("scale to zero keeps no kind: %+v %v", got, err)
	}
	// A vector without GPUs fits any host; one with GPUs needs that kind.
	if !capacityFits(ResourceVector{CPUMillis: 1}, a) || capacityFits(a, ResourceVector{CPUMillis: 1}) || capacityFits(ResourceVector{GPUKind: "nvidia-l4", GPUs: 2}, a) {
		t.Fatal("fit")
	}
}

func TestSizeGPUs(t *testing.T) {
	if kind, count := sizeGPUs(h100); kind != "nvidia-h100-80gb" || count != 1 {
		t.Fatalf("h100: %q %d", kind, count)
	}
	if kind, count := sizeGPUs(big); kind != "" || count != 0 {
		t.Fatalf("CPU size: %q %d", kind, count)
	}
	host, err := sizeResources(h100)
	if err != nil || host.GPUKind != "nvidia-h100-80gb" || host.GPUs != 1 || host.CPUMillis != 20000 {
		t.Fatalf("GPU host: %+v %v", host, err)
	}
	amd := provider.ServerSize{ID: "gpu-mi300x1-192gb", CPUs: 20, MemoryMiB: 245760, DiskGB: 720, Regions: []string{"tor1"},
		Accelerator: &provider.Accelerator{Kind: "amd-mi300x", Vendor: "amd", Count: 1, MemoryGiB: 192}}
	if _, err := sizeResources(amd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("AMD size: %v", err)
	}
	// SelectExpansionSize leaves an AMD size out rather than failing the whole pool.
	options := gpuOptions()
	options.Sizes = append(options.Sizes, amd)
	pool := PoolExpansionPolicy{PoolID: "pool", Region: "tor1", Image: "ubuntu-24-04-x64", Architecture: "amd64", AllowedSizes: []string{"c-32", amd.ID}}
	if got, err := SelectExpansionSize(options, pool, CapacityDemand{Resources: ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30}}); err != nil || got != "c-32" {
		t.Fatalf("AMD size in the catalog: %q %v", got, err)
	}
}

func TestCheckGPULimit(t *testing.T) {
	p := &CapacityPolicy{CloudID: "cloud", Revision: 4, GPULimits: []GPULimit{{Kind: "nvidia-l4", Maximum: 2}}}
	d := CapacityDemand{CloudID: "cloud", PoolID: "pool", TargetID: "target"}
	one, two := int64(1), int64(2)
	if b := checkGPULimit(p, d, "nvidia-l4", &one, 1); b != nil {
		t.Fatalf("within ceiling: %+v", b)
	}
	b := checkGPULimit(p, d, "nvidia-l4", &two, 1)
	if b == nil || b.Reason != "hard_limit" || b.Resource != "gpus:nvidia-l4" || b.Limit != 2 || b.Counted != 2 || b.Requested != 1 || b.PoolID != "pool" || b.PolicyRevision != 4 || b.Retryable {
		t.Fatalf("over ceiling: %+v", b)
	}
	// Unknown capacity is not zero usage.
	b = checkGPULimit(p, d, "nvidia-l4", nil, 1)
	if b == nil || b.Reason != "stale_inventory" || b.Resource != "gpus:nvidia-l4" || !b.Retryable {
		t.Fatalf("unknown count: %+v", b)
	}
	b = checkGPULimit(p, d, "nvidia-t4", &one, 1)
	if b == nil || b.Reason != "hard_limit" || b.Resource != "gpus:nvidia-t4" || b.Limit != 0 {
		t.Fatalf("no ceiling for the kind: %+v", b)
	}
	if b = checkGPULimit(p, d, "", nil, 0); b != nil {
		t.Fatalf("no GPUs requested: %+v", b)
	}
	huge := int64(1<<63 - 1)
	if b = checkGPULimit(p, d, "nvidia-l4", &huge, 1); b == nil || b.Reason != "hard_limit" {
		t.Fatalf("overflow: %+v", b)
	}
}

func TestGPUFieldsLeaveCPUJSONUnchanged(t *testing.T) {
	for _, v := range []any{
		ResourceVector{Servers: 1, CPUMillis: 2, MemoryBytes: 3, StorageBytes: 4},
		CapacityPolicy{Pools: []PoolExpansionPolicy{{PoolID: "pool"}}},
		PoolMember{ServerID: "server", CPUMillis: 1, MemoryBytes: 2},
		Pool{ID: "pool"},
		Placement{ServerID: "server"},
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "gpu") || strings.Contains(string(raw), "accelerator") {
			t.Fatalf("CPU-only JSON grew a GPU field: %s", raw)
		}
	}
}

func TestExpansionDemandFitsGPUHost(t *testing.T) {
	host := ResourceVector{Servers: 1, CPUMillis: 20000, MemoryBytes: 240 << 30, StorageBytes: 720e9, GPUKind: "nvidia-h100-80gb", GPUs: 1}
	one := ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30, GPUKind: "nvidia-h100-80gb", GPUs: 1}
	if !expansionDemandFits(host, ResourceVector{}, ResourceVector{}, one) {
		t.Fatal("one H100 on an H100 host")
	}
	if expansionDemandFits(host, ResourceVector{}, one, one) {
		t.Fatal("a second H100 on a one-H100 host")
	}
	if expansionDemandFits(host, ResourceVector{}, ResourceVector{}, ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30}) {
		t.Fatal("CPU work on a GPU host")
	}
	if expansionDemandFits(ResourceVector{Servers: 1, CPUMillis: 20000, MemoryBytes: 240 << 30}, ResourceVector{}, ResourceVector{}, one) {
		t.Fatal("GPU work on a CPU host")
	}
}

func TestPoolGPUMembers(t *testing.T) {
	for name, tc := range map[string]struct {
		in   PoolInput
		fail bool
	}{
		"CPU pool":                      {PoolInput{Members: []PoolMember{{ServerID: "s"}}}, false},
		"CPU pool with a GPU budget":    {PoolInput{Members: []PoolMember{{ServerID: "s", GPUs: 1}}}, true},
		"GPU pool":                      {PoolInput{AcceleratorKind: "nvidia-l4", Members: []PoolMember{{ServerID: "s", GPUs: 2}}}, false},
		"GPU pool member without GPUs":  {PoolInput{AcceleratorKind: "nvidia-l4", Members: []PoolMember{{ServerID: "s"}}}, true},
		"AMD pool":                      {PoolInput{AcceleratorKind: "amd-mi300x"}, true},
		"unknown kind":                  {PoolInput{AcceleratorKind: "nvidia-made-up"}, true},
		"empty GPU pool for expansions": {PoolInput{AcceleratorKind: "nvidia-l4"}, false},
	} {
		if err := validatePoolGPUs(tc.in); (err != nil) != tc.fail {
			t.Errorf("%s: %v", name, err)
		}
	}

	twoL4 := &Server{Health: RuntimeHealth{GPURuntime: provider.GPUStatusReady, GPUs: []provider.GPUDevice{{Index: 0, Kind: "nvidia-l4"}, {Index: 1, Kind: "nvidia-l4"}}}}
	if err := usableGPUs(twoL4, "nvidia-l4", PoolMember{GPUs: 2}); err != nil {
		t.Fatal(err)
	}
	if err := usableGPUs(twoL4, "nvidia-l4", PoolMember{GPUs: 3}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("budget above devices: %v", err)
	}
	if err := usableGPUs(twoL4, "nvidia-t4", PoolMember{GPUs: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other kind: %v", err)
	}
	if err := usableGPUs(twoL4, "", PoolMember{}); err != nil {
		t.Fatalf("GPU server in a CPU pool without a GPU budget: %v", err)
	}
	if err := usableGPUs(twoL4, "", PoolMember{GPUs: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("GPU budget in a CPU pool: %v", err)
	}
	missing := &Server{Health: RuntimeHealth{GPURuntime: provider.GPUStatusToolkitMissing, GPUs: twoL4.Health.GPUs}}
	if err := usableGPUs(missing, "nvidia-l4", PoolMember{GPUs: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("toolkit missing: %v", err)
	}
}
