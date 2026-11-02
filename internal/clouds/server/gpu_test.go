package server

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestParseGPUProbe(t *testing.T) {
	nvidia := map[string]json.RawMessage{"nvidia": json.RawMessage(`{}`), "runc": json.RawMessage(`{}`)}
	twoL4 := "0, GPU-aaa, NVIDIA L4, 23034\n1, GPU-bbb, NVIDIA L4, 23034\n"
	for _, tc := range []struct {
		name, raw, status, message string
		runtimes                   map[string]json.RawMessage
		devices                    int
	}{
		{"ready through runtime", `{"smi_exit":0,"smi":` + strconv.Quote(twoL4) + `,"nvidia_pci":true,"toolkit":false}`, "ready", "", nvidia, 2},
		{"ready through toolkit", `{"smi_exit":0,"smi":` + strconv.Quote(twoL4) + `,"nvidia_pci":true,"toolkit":true}`, "ready", "", nil, 2},
		{"toolkit missing", `{"smi_exit":0,"smi":` + strconv.Quote(twoL4) + `,"nvidia_pci":true,"toolkit":false}`, "toolkit_missing", "NVIDIA Container Toolkit", nil, 2},
		{"driver missing", `{"smi_exit":9,"smi":"","nvidia_pci":true,"toolkit":false}`, "driver_missing", "nvidia-smi", nil, 0},
		{"no GPU", `{"smi_exit":127,"smi":"","nvidia_pci":false,"toolkit":false}`, "none", "", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseGPUProbe([]byte(tc.raw), tc.runtimes)
			if err != nil || inv.Status != tc.status || len(inv.Devices) != tc.devices || !strings.Contains(inv.Message, tc.message) {
				t.Fatalf("%+v %v", inv, err)
			}
			if tc.devices > 0 && inv.Devices[1] != (provider.GPUDevice{Index: 1, UUID: "GPU-bbb", Kind: "nvidia-l4", Product: "NVIDIA L4", MemoryMiB: 23034}) {
				t.Fatalf("device: %+v", inv.Devices[1])
			}
		})
	}
	if _, err := parseGPUProbe([]byte(`{"smi_exit":0,"smi":"0, GPU-aaa\n"}`), nil); err == nil {
		t.Fatal("a short CSV line must not parse")
	}
	inv, err := parseGPUProbe([]byte(`{"smi_exit":0,"smi":"0, GPU-aaa, NVIDIA L4, 23034\n"}`), nil)
	if err != nil || !strings.Contains(inv.Message, "1 NVIDIA L4 GPU but") {
		t.Fatalf("%+v %v", inv, err)
	}
}
