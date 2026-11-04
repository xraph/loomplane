package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// ErrUnsupportedProbe is the error of a host probe this OS doesn't answer.
// Loomplane then reports the capacity as unknown, as it does when an SSH
// probe fails.
var ErrUnsupportedProbe = errors.New("this probe is not supported on " + runtime.GOOS)

// gpuTimeout bounds nvidia-smi, as the SSH GPU probe bounds it.
const gpuTimeout = 10 * time.Second

// ProbeHandler answers the probe endpoint. Target ProbeInventory replies with
// the JSON Inventory from Detect and returns, which closes the stream, as in
// phase 1. The cgroup, gpu, filesystem and listeners targets reply with the
// JSON the SSH probes print, as nodeproto's CgroupLimits, GPUProbe,
// Filesystem and Listeners. A probe that fails, or an unknown target,
// refuses the stream with the reason. opts is called per request so it sees
// the current config; the filesystem probe finds the daemon socket from it
// the way registerDocker does.
func ProbeHandler(opts func() DetectOptions) Handler {
	return probeHandler(opts, newHostProbes(func(ctx context.Context) string {
		o := opts()
		return findSocket(ctx, o.Config.DockerSocket, o)
	}))
}

func probeHandler(opts func() DetectOptions, p *hostProbes) Handler {
	return func(ctx context.Context, hdr nodeproto.StreamHeader, stream net.Conn) error {
		var reply any
		var err error
		switch hdr.Target {
		case nodeproto.ProbeInventory:
			// Detect takes a few seconds, so the verdict goes first, as
			// it always has.
			if aerr := accept(stream); aerr != nil {
				return aerr
			}
			return json.NewEncoder(stream).Encode(Detect(ctx, opts()))
		case nodeproto.ProbeCgroup:
			reply, err = p.cgroup()
		case nodeproto.ProbeGPU:
			reply, err = p.gpu(ctx)
		case nodeproto.ProbeFilesystem:
			reply, err = p.filesystem(ctx, hdr.Arg)
		case nodeproto.ProbeListeners:
			reply, err = p.listeners()
		default:
			return refusal(fmt.Sprintf("unknown probe target %q", sanitize(hdr.Target)))
		}
		if err != nil {
			return refusal("probe " + hdr.Target + ": " + err.Error())
		}
		return json.NewEncoder(stream).Encode(reply)
	}
}

// hostProbes reads this machine for the probe targets. The /sys and /proc
// roots, the node's PID and the command runner are fields so tests can point
// them at fixtures.
type hostProbes struct {
	sys, proc string
	// pid is this process, whose cgroup the cgroup probe reads as well as
	// dockerd's, as the SSH probe reads its own.
	pid int
	// socket finds the daemon socket, or "" when none answers.
	socket     func(ctx context.Context) string
	lookPath   func(name string) (string, error)
	run        func(ctx context.Context, name string, args ...string) ([]byte, error)
	gpuTimeout time.Duration
}

func newHostProbes(socket func(ctx context.Context) string) *hostProbes {
	return &hostProbes{sys: "/sys", proc: "/proc", pid: os.Getpid(), socket: socket,
		lookPath: exec.LookPath, run: runOutput, gpuTimeout: gpuTimeout}
}

// runOutput runs name and returns its standard output.
func runOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name comes from LookPath for a fixed name
	// A child that keeps the pipe open must not outlive the timeout.
	cmd.WaitDelay = time.Second
	return cmd.Output()
}
