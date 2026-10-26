package provider

import (
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func TestNormalizeAcceleratorKind(t *testing.T) {
	for _, tc := range []struct {
		product string
		mib     int
		want    string
	}{
		{"NVIDIA H100 80GB HBM3", 81559, "nvidia-h100-80gb"},
		{"NVIDIA H100 NVL", 95830, "nvidia-h100-nvl-94gb"},
		{"nvidia-h100-mega-80gb", 0, "nvidia-h100-80gb"},
		{"nvidia_h100", 80 * 1024, "nvidia-h100-80gb"},
		{"h100", 0, "nvidia-h100-80gb"},
		{"NVIDIA H200", 143771, "nvidia-h200-141gb"},
		{"NVIDIA A100-SXM4-40GB", 40960, "nvidia-a100-40gb"},
		{"NVIDIA A100 80GB PCIe", 81920, "nvidia-a100-80gb"},
		{"NVIDIA-A100-SXM4-80GB", 0, "nvidia-a100-80gb"},
		{"nvidia-tesla-a100", 0, "nvidia-a100-40gb"},
		{"Tesla T4", 15360, "nvidia-t4"},
		{"nvidia-tesla-t4", 0, "nvidia-t4"},
		{"NVIDIA L4", 23034, "nvidia-l4"},
		{"nvidia-l4", 0, "nvidia-l4"},
		{"NVIDIA L40S", 46068, "nvidia-l40s"},
		{"nvidia_l40s", 0, "nvidia-l40s"},
		{"NVIDIA A10G", 23028, "nvidia-a10g"},
		{"NVIDIA A10", 23028, "nvidia-a10"},
		{"NVIDIA RTX 6000 Ada Generation", 49140, "nvidia-rtx-6000-ada"},
		{"nvidia_rtx_4000_ada", 0, "nvidia-rtx-4000-ada"},
		{"Tesla V100-SXM2-16GB", 16384, "nvidia-v100-16gb"},
		{"Tesla V100-SXM2-32GB", 32768, "nvidia-v100-32gb"},
		{"AMD Instinct MI300X", 196608, "amd-mi300x"},
		{"NVIDIA GeForce RTX 4090", 24564, "nvidia-geforce-rtx-4090"},
		{"", 0, ""},
	} {
		if got := NormalizeAcceleratorKind(tc.product, tc.mib); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.product, got, tc.want)
		}
	}
}

func TestAcceleratorKindsAreCanonical(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range AcceleratorKinds() {
		if !regexp.MustCompile(`^(nvidia|amd)-[a-z0-9-]+$`).MatchString(k.ID) || k.MemoryGiB <= 0 || k.Title == "" || seen[k.ID] {
			t.Fatalf("bad kind %+v", k)
		}
		seen[k.ID] = true
		if got, ok := LookupAcceleratorKind(k.ID); !ok || got != k {
			t.Fatalf("lookup %s", k.ID)
		}
	}
}

func TestGPUInventoryPlace(t *testing.T) {
	server := GPUInventory{Status: GPUStatusReady, Devices: []GPUDevice{{Index: 0, UUID: "GPU-a", Kind: "nvidia-l4", MemoryMiB: 23034}, {Index: 1, UUID: "GPU-b", Kind: "nvidia-l4", MemoryMiB: 23034}}}
	if p, err := server.Place(GPURequest{Kind: "nvidia-l4", Count: 2}); err != nil || p.Count != 2 || p.NodeSelector != nil {
		t.Fatalf("server fit: %+v %v", p, err)
	}
	cluster := GPUInventory{Status: GPUStatusReady, Nodes: []GPUNodeGroup{{Kind: "nvidia-l4", MemoryGiB: 24, Nodes: 4, PerNode: 1, Total: 4,
		NodeSelector: map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4"},
		Tolerations:  []GPUToleration{{Key: "nvidia.com/gpu", Operator: "Exists", Effect: "NoSchedule"}}}}}
	p, err := cluster.Place(GPURequest{Kind: "nvidia-l4", Count: 1})
	if err != nil || p.NodeSelector["cloud.google.com/gke-accelerator"] != "nvidia-l4" || len(p.Tolerations) != 1 {
		t.Fatalf("cluster fit: %+v %v", p, err)
	}
	for _, tc := range []struct {
		inv  GPUInventory
		req  GPURequest
		code string
	}{
		{server, GPURequest{Kind: "nvidia-l4", Count: 3}, GPUCountUnavailable},
		{server, GPURequest{Kind: "nvidia-a100-80gb", Count: 1}, GPUKindUnavailable},
		{cluster, GPURequest{Kind: "nvidia-l4", Count: 2}, GPUCountUnavailable}, // 4 in total, 1 per node
		{GPUInventory{Status: GPUStatusToolkitMissing, Message: "install the toolkit"}, GPURequest{Kind: "nvidia-l4", Count: 1}, GPUUnavailable},
		{GPUInventory{Status: GPUStatusUnknown}, GPURequest{Kind: "nvidia-l4", Count: 1}, GPUUnavailable},
		{GPUInventory{Status: GPUStatusReady, Devices: []GPUDevice{{Kind: "amd-mi300x"}}}, GPURequest{Kind: "amd-mi300x", Count: 1}, GPUVendorUnsupported},
	} {
		var unavailable *GPUUnavailableError
		if _, err := tc.inv.Place(tc.req); !errors.As(err, &unavailable) || unavailable.Code != tc.code {
			t.Errorf("%+v: want %s, got %v", tc.req, tc.code, err)
		}
	}
	if _, err := server.Place(GPURequest{Kind: "nvidia-l4", Count: 9}); !errors.Is(err, ErrGPURequestInvalid) {
		t.Fatalf("nine devices: %v", err)
	}
}

func TestGPUInventoryDetailsRoundTrip(t *testing.T) {
	in := GPUInventory{Status: GPUStatusReady, Devices: []GPUDevice{{Index: 0, UUID: "GPU-a", Kind: "nvidia-l4", Product: "NVIDIA L4", MemoryMiB: 23034}}, CheckedAt: time.Unix(1, 0).UTC()}
	d := in.Details()
	if d[GPUDetailStatus] != "ready" {
		t.Fatalf("status detail: %v", d)
	}
	out, ok := ParseGPUInventory(d)
	if !ok || !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %+v", out)
	}
	if _, ok := ParseGPUInventory(map[string]string{}); ok {
		t.Fatal("absent inventory parsed")
	}
}

// An inventory without a message of its own still says why it can't run
// GPU work, by its status. A CPU server says it has no GPUs, not that
// its GPUs couldn't be read.
func TestGPUInventoryPlaceExplainsEachStatus(t *testing.T) {
	req := GPURequest{Kind: "nvidia-l4", Count: 1}
	for status, want := range map[string]string{
		GPUStatusNone:           "This target has no GPUs.",
		GPUStatusDriverMissing:  "This target has an NVIDIA GPU but its driver doesn't answer. Install the NVIDIA driver, then check the connection again.",
		GPUStatusToolkitMissing: "This target has GPUs but Docker can't use them. Install the NVIDIA Container Toolkit, then check the connection again.",
		GPUStatusNoDevicePlugin: "This cluster's nodes have GPUs but nothing advertises nvidia.com/gpu. Install the NVIDIA device plugin or GPU Operator, then check the connection again.",
		GPUStatusUnlisted:       "Loomplane can't list this cluster's nodes, so it can't see its GPUs. Let the connection list nodes, then check it again.",
		GPUStatusUnknown:        "Loomplane couldn't read this target's GPUs. Check the connection again.",
		"":                      "Loomplane couldn't read this target's GPUs. Check the connection again.",
	} {
		_, err := GPUInventory{Status: status}.Place(req)
		var unavailable *GPUUnavailableError
		if !errors.As(err, &unavailable) || unavailable.Code != GPUUnavailable || unavailable.Message != want {
			t.Errorf("status %q: %v", status, err)
		}
	}
	// The inventory's own message still wins.
	_, err := GPUInventory{Status: GPUStatusToolkitMissing, Message: "This server has 2 NVIDIA L4 GPUs but Docker can't use them."}.Place(req)
	if err == nil || err.Error() != "This server has 2 NVIDIA L4 GPUs but Docker can't use them." {
		t.Fatalf("own message: %v", err)
	}
}
