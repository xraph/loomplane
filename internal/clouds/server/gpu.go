package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

const gpuUnreadable = "Loomplane couldn't read this server's GPUs."

// GPUs reads the GPUs the daemon's host has and whether Docker can hand them to containers.
// It never installs anything. A probe failure returns GPUStatusUnknown with a short reason, not an error.
func (c *Client) GPUs(ctx context.Context) (provider.GPUInventory, error) {
	unknown := provider.GPUInventory{Status: provider.GPUStatusUnknown, Message: gpuUnreadable, CheckedAt: time.Now().UTC()}
	session, err := c.ssh.NewSession()
	if err != nil {
		return unknown, nil
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	out, err := session.Output(gpuProbeCommand)
	if err != nil {
		return unknown, nil
	}
	var info struct {
		Runtimes map[string]json.RawMessage `json:"Runtimes"`
	}
	if infoErr := c.getJSON(ctx, "/info", &info); infoErr != nil {
		return unknown, nil
	}
	inv, err := parseGPUProbe(out, info.Runtimes)
	if err != nil {
		return unknown, nil
	}
	inv.CheckedAt = time.Now().UTC()
	return inv, nil
}

// parseGPUProbe classifies the probe output and the daemon's runtimes.
func parseGPUProbe(raw []byte, runtimes map[string]json.RawMessage) (provider.GPUInventory, error) {
	var probe struct {
		SmiExit   int    `json:"smi_exit"`
		Smi       string `json:"smi"`
		NvidiaPCI bool   `json:"nvidia_pci"`
		Toolkit   bool   `json:"toolkit"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return provider.GPUInventory{}, fmt.Errorf("decode GPU probe: %w", err)
	}
	if probe.SmiExit == 0 {
		var devices []provider.GPUDevice
		for _, line := range strings.Split(probe.Smi, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, ",")
			if len(parts) < 4 {
				return provider.GPUInventory{}, fmt.Errorf("malformed nvidia-smi line %q", line)
			}
			product := strings.TrimSpace(strings.Join(parts[2:len(parts)-1], ","))
			index, indexErr := strconv.Atoi(strings.TrimSpace(parts[0]))
			memory, memoryErr := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
			uuid := strings.TrimSpace(parts[1])
			if indexErr != nil || memoryErr != nil || uuid == "" || product == "" {
				return provider.GPUInventory{}, fmt.Errorf("malformed nvidia-smi line %q", line)
			}
			devices = append(devices, provider.GPUDevice{
				Index: index, UUID: uuid, Kind: provider.NormalizeAcceleratorKind(product, memory), Product: product, MemoryMiB: memory,
			})
		}
		if len(devices) > 0 {
			_, hasRuntime := runtimes["nvidia"]
			if probe.Toolkit || hasRuntime {
				return provider.GPUInventory{Status: provider.GPUStatusReady, Devices: devices}, nil
			}
			title := devices[0].Product
			if k, ok := provider.LookupAcceleratorKind(devices[0].Kind); ok {
				title = k.Title
			}
			noun := "GPU"
			if len(devices) > 1 {
				noun = "GPUs"
			}
			return provider.GPUInventory{
				Status: provider.GPUStatusToolkitMissing,
				Message: fmt.Sprintf("This server has %d %s %s but Docker can't use them. Install the NVIDIA Container Toolkit, run nvidia-ctk runtime configure --runtime=docker, restart Docker, then check the server again.",
					len(devices), title, noun),
				Devices: devices,
			}, nil
		}
	}
	if probe.SmiExit != 0 && probe.NvidiaPCI {
		return provider.GPUInventory{
			Status:  provider.GPUStatusDriverMissing,
			Message: "This server has an NVIDIA GPU but nvidia-smi doesn't answer. Install the NVIDIA driver, then check the server again.",
		}, nil
	}
	return provider.GPUInventory{Status: provider.GPUStatusNone}, nil
}

const gpuProbeCommand = `python3 - <<'PY'
import glob,json,shutil,subprocess
code=127;smi=''
if shutil.which('nvidia-smi'):
 try:
  r=subprocess.run(['nvidia-smi','--query-gpu=index,uuid,name,memory.total','--format=csv,noheader,nounits'],capture_output=True,text=True,timeout=10)
  code=r.returncode;smi=r.stdout
 except subprocess.TimeoutExpired:
  code=124
 except OSError:
  code=127
pci=False
for f in glob.glob('/sys/bus/pci/devices/*/vendor'):
 try:
  if open(f).read().strip().lower()=='0x10de': pci=True
 except OSError: pass
toolkit=bool(shutil.which('nvidia-container-cli') or shutil.which('nvidia-ctk'))
print(json.dumps({'smi_exit':code,'smi':smi,'nvidia_pci':pci,'toolkit':toolkit}))
PY`
