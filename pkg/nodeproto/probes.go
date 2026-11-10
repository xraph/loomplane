package nodeproto

import (
	"errors"
	"fmt"
)

// Targets of the docker endpoint.
const (
	// DockerTargetSocket is a raw byte stream to the daemon socket.
	DockerTargetSocket = ""
	// DockerTargetFenced carries exactly one HTTP request and response
	// through the fence journal, with StreamHeader.Fence set.
	DockerTargetFenced = "fenced"
)

// Targets of the probe endpoint. The filesystem probe takes the Docker root
// in StreamHeader.Arg.
const (
	ProbeInventory  = ""
	ProbeCgroup     = "cgroup"
	ProbeGPU        = "gpu"
	ProbeFilesystem = "filesystem"
	ProbeListeners  = "listeners"
)

// Fence modes, the values of Fence.Mode.
const (
	FenceAdvance     = "advance"
	FenceRequest     = "request"
	FenceExec        = "exec"
	FenceIngressExec = "ingress-exec"
	// FenceRepairCheck reviews a dirty journal and FenceRepair clears it,
	// once an operator has reviewed it. Neither carries a resource or a
	// generation, only the daemon.
	FenceRepairCheck = "repair-check"
	FenceRepair      = "repair"
)

// MaxArgSize is the longest StreamHeader.Arg, in bytes.
const MaxArgSize = 4096

// CgroupLimits are the CPU (millicpus) and memory (bytes) limits the node's
// cgroup allows, the same shape and units the SSH effective capacity command
// prints.
type CgroupLimits struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
}

// GPUProbe is the same shape the SSH GPU probe command prints.
type GPUProbe struct {
	SmiExit   int    `json:"smi_exit"`
	Smi       string `json:"smi"`
	NvidiaPCI bool   `json:"nvidia_pci"`
	Toolkit   bool   `json:"toolkit"`
}

// Filesystem is the capacity of the filesystem holding the Docker root.
type Filesystem struct {
	TotalBytes int64 `json:"total_bytes"`
	FreeBytes  int64 `json:"free_bytes"`
}

// Listeners lists which of ports 80, 443 and 2019 something already holds.
type Listeners struct {
	Ports []int `json:"ports"`
}

// Validate checks the rules every handler shares. A fenced docker header must
// carry a complete fence, and Arg is bounded. It leaves other headers alone.
func (h StreamHeader) Validate() error {
	if len(h.Arg) > MaxArgSize {
		return fmt.Errorf("nodeproto: arg is %d bytes, over the %d byte limit", len(h.Arg), MaxArgSize)
	}
	if h.Endpoint != EndpointDocker || h.Target != DockerTargetFenced {
		return nil
	}
	f := h.Fence
	switch {
	case f == nil:
		return errors.New("nodeproto: a fenced docker stream needs a fence")
	case f.Daemon == "":
		return errors.New("nodeproto: fence daemon is empty")
	case f.Mode == FenceRepairCheck, f.Mode == FenceRepair:
		return nil
	case f.Generation <= 0:
		return errors.New("nodeproto: fence generation must be positive")
	case f.Resource == "":
		return errors.New("nodeproto: fence resource is empty")
	}
	switch f.Mode {
	case FenceAdvance, FenceRequest, FenceExec, FenceIngressExec:
		return nil
	}
	return fmt.Errorf("nodeproto: unknown fence mode %q", f.Mode)
}
