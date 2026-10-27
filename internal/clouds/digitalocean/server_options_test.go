package digitalocean

import (
	"net/http"
	"slices"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestServerOptionsUsesAccountScopedProviderCatalog(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/regions", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"regions": []any{map[string]any{"slug": "nyc3", "name": "New York 3", "available": true}, map[string]any{"slug": "closed", "available": false}}})
	})
	f.on("GET /v2/sizes", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"sizes": []any{map[string]any{"slug": "s-1vcpu-2gb", "vcpus": 1, "memory": 2048, "disk": 50, "price_hourly": 0.01786, "price_monthly": 12, "regions": []string{"nyc3"}, "available": true}}})
	})
	f.on("GET /v2/images", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("type") != "distribution" {
			t.Error("unscoped image catalog")
		}
		reply(w, 200, map[string]any{"images": []any{map[string]any{"slug": "ubuntu-24-04-x64", "name": "Ubuntu 24.04", "distribution": "Ubuntu", "public": true, "regions": []string{"nyc3"}}, map[string]any{"slug": "unsupported", "distribution": "Other", "public": true, "regions": []string{"nyc3"}}}})
	})
	options, err := c.ServerOptions(t.Context(), infrastructureCredential())
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Regions) != 1 || len(options.Sizes) != 1 || len(options.Images) != 2 || !options.Images[0].Bootstrap || options.Images[1].Bootstrap || options.Sizes[0].HourlyUSD != 0.01786 || options.Sizes[0].MemoryMiB != 2048 || !options.Sizes[0].HourlyPriceKnown || options.Images[0].Architecture != "amd64" {
		t.Fatalf("catalog: %+v", options)
	}
}

func TestServerOptionsReadsGPUDropletsAndGPUImages(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/regions", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"regions": []any{map[string]any{"slug": "tor1", "name": "Toronto 1", "available": true}}})
	})
	f.on("GET /v2/sizes", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"sizes": []any{
			map[string]any{"slug": "s-1vcpu-2gb", "vcpus": 1, "memory": 2048, "disk": 50, "price_hourly": 0.01786, "price_monthly": 12, "regions": []string{"tor1"}, "available": true},
			map[string]any{"slug": "gpu-h100x1-80gb", "vcpus": 20, "memory": 245760, "disk": 720, "price_hourly": 3.39, "price_monthly": 2522.16, "regions": []string{"tor1"}, "available": true,
				"gpu_info": map[string]any{"count": 1, "model": "nvidia_h100", "vram": map[string]any{"amount": 80, "unit": "gib"}}},
			map[string]any{"slug": "gpu-mi300x1-192gb", "vcpus": 20, "memory": 245760, "disk": 720, "price_hourly": 1.99, "price_monthly": 1480, "regions": []string{"tor1"}, "available": true,
				"gpu_info": map[string]any{"count": 1, "model": "amd_mi300x", "vram": map[string]any{"amount": 192, "unit": "gib"}}},
			map[string]any{"slug": "gpu-h100x8-640gb", "vcpus": 160, "memory": 1966080, "disk": 2046, "price_hourly": 23.92, "price_monthly": 17796.48, "regions": []string{"tor1"}, "available": true,
				"gpu_info": map[string]any{"count": 8, "model": "nvidia_h100", "vram": map[string]any{"amount": 640, "unit": "gib"}}},
		}})
	})
	f.on("GET /v2/images", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"images": []any{
			map[string]any{"slug": "ubuntu-24-04-x64", "name": "Ubuntu 24.04", "distribution": "Ubuntu", "public": true, "regions": []string{"tor1"}},
			map[string]any{"slug": "gpu-h100x1-base", "name": "AI/ML Ready", "distribution": "Ubuntu", "public": true, "regions": []string{"tor1"}},
			map[string]any{"slug": "gpu-amd-base", "name": "AI/ML Ready (AMD)", "distribution": "Ubuntu", "public": true, "regions": []string{"tor1"}},
		}})
	})
	options, err := c.ServerOptions(t.Context(), infrastructureCredential())
	if err != nil {
		t.Fatal(err)
	}
	cpu, h100, amd := options.Sizes[0], options.Sizes[1], options.Sizes[2]
	if cpu.Accelerator != nil {
		t.Fatalf("CPU size has an accelerator: %+v", cpu)
	}
	if h100.Accelerator == nil || *h100.Accelerator != (provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}) || !slices.Equal(h100.Architectures, []string{"amd64"}) {
		t.Fatalf("H100: %+v", h100)
	}
	if amd.Accelerator == nil || amd.Accelerator.Vendor != "amd" || amd.Accelerator.Kind != "amd-mi300x" {
		t.Fatalf("MI300X: %+v", amd)
	}
	// 640 GiB across eight GPUs is 80 per GPU: an H100 80GB, not an NVL.
	if x8 := options.Sizes[3]; x8.Accelerator == nil || *x8.Accelerator != (provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 8, MemoryGiB: 80}) {
		t.Fatalf("H100x8: %+v", x8.Accelerator)
	}
	plain, gpu, amdImage := options.Images[0], options.Images[1], options.Images[2]
	if len(plain.Accelerators) != 0 || !slices.Equal(gpu.Accelerators, []string{"nvidia"}) || !gpu.Bootstrap || gpu.Architecture != "amd64" {
		t.Fatalf("images: %+v %+v", plain, gpu)
	}
	if !slices.Equal(amdImage.Accelerators, []string{"amd"}) || !amdImage.Bootstrap {
		t.Fatalf("AMD image: %+v", amdImage)
	}
}

// vram may be per GPU or the size's total. It only tells variants apart,
// and the kind table supplies the memory, so a total never becomes the
// memory of one GPU.
func TestDropletAcceleratorReadsVRAMEitherWay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		count int
		vram  int
		unit  string
		want  provider.Accelerator
	}{
		{"one H100", "nvidia_h100", 1, 80, "gib", provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}},
		{"eight H100, total", "nvidia_h100", 8, 640, "gib", provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 8, MemoryGiB: 80}},
		{"eight H100, per GPU", "nvidia_h100", 8, 80, "gib", provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 8, MemoryGiB: 80}},
		{"two H100 NVL, total", "nvidia_h100", 2, 188, "gb", provider.Accelerator{Kind: "nvidia-h100-nvl-94gb", Vendor: "nvidia", Count: 2, MemoryGiB: 94}},
		{"eight MI300X, total", "amd_mi300x", 8, 1536, "gib", provider.Accelerator{Kind: "amd-mi300x", Vendor: "amd", Count: 8, MemoryGiB: 192}},
		{"eight A100, total", "nvidia_a100", 8, 640, "gib", provider.Accelerator{Kind: "nvidia-a100-80gb", Vendor: "nvidia", Count: 8, MemoryGiB: 80}},
		{"eight A100 40GB, total", "nvidia_a100", 8, 320, "gib", provider.Accelerator{Kind: "nvidia-a100-40gb", Vendor: "nvidia", Count: 8, MemoryGiB: 40}},
		{"unknown unit", "nvidia_h100", 8, 655360, "mib", provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 8, MemoryGiB: 80}},
		{"unknown model, one GPU", "nvidia_b300", 1, 288, "gib", provider.Accelerator{Kind: dropletKind("nvidia_b300", 1, 288), Vendor: "nvidia", Count: 1, MemoryGiB: 288}},
		{"unknown model, eight GPUs", "nvidia_b300", 8, 2304, "gib", provider.Accelerator{Kind: dropletKind("nvidia_b300", 8, 2304), Vendor: "nvidia", Count: 8}},
	} {
		if got := dropletAccelerator(tc.model, tc.count, tc.vram, tc.unit); got == nil || *got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
