package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// DetectOptions carries what Detect needs. Zero fields use the real machine,
// and tests inject the rest. Detect never reads a kubeconfig.
type DetectOptions struct {
	// Config is a snapshot (Live.Load) that supplies the informational
	// endpoint list.
	Config        Config
	DockerSockets []string
	PodmanSockets []string
	// NvidiaSMI runs nvidia-smi and returns its CSV output.
	NvidiaSMI func(ctx context.Context) ([]byte, error)
	Hostname  func() (string, error)
	// Ports returns "free" or "busy" for ports 80 and 443.
	Ports func() string
	// Addresses lists the machine's interface addresses.
	Addresses func() ([]net.Addr, error)
}

func dockerSockets() []string {
	out := []string{"/var/run/docker.sock"}
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		if p := strings.TrimPrefix(h, "unix://"); p != "" && p != out[0] {
			out = append(out, p)
		}
	}
	return out
}

func podmanSockets() []string {
	out := []string{"/run/podman/podman.sock"}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		out = append(out, filepath.Join(x, "podman", "podman.sock"))
	}
	return out
}

func runNvidiaSMI(ctx context.Context) ([]byte, error) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, //nolint:gosec // path comes from LookPath for a fixed name
		"--query-gpu=name,memory.total", "--format=csv,noheader,nounits").Output()
}

// Detect reports what the machine offers. Missing runtimes and GPUs are just
// absent from the result.
func Detect(ctx context.Context, o DetectOptions) nodeproto.Inventory {
	if o.DockerSockets == nil {
		o.DockerSockets = dockerSockets()
	}
	if o.PodmanSockets == nil {
		o.PodmanSockets = podmanSockets()
	}
	if o.NvidiaSMI == nil {
		o.NvidiaSMI = runNvidiaSMI
	}
	if o.Hostname == nil {
		o.Hostname = os.Hostname
	}
	if o.Ports == nil {
		o.Ports = checkPorts
	}
	if o.Addresses == nil {
		o.Addresses = net.InterfaceAddrs
	}
	inv := nodeproto.Inventory{
		OS:   osName(ctx),
		Arch: runtime.GOARCH,
		CPUs: runtime.NumCPU(),
	}
	inv.Kernel = kernelVersion(ctx)
	inv.MemoryMiB = memoryMiB(ctx)
	inv.DiskGiB = diskGiB()
	if h, err := o.Hostname(); err == nil {
		inv.Hostname = h
	}
	inv.Docker = pingRuntime(ctx, o.DockerSockets)
	inv.Podman = pingRuntime(ctx, o.PodmanSockets)
	if out, err := o.NvidiaSMI(ctx); err == nil {
		inv.GPUs = parseNvidia(out)
	}
	inv.Ports80443 = o.Ports()
	inv.Endpoints = slices.Clone(o.Config.Endpoints)
	inv.PublicAddresses = publicAddresses(o.Addresses)
	return inv
}

// publicAddresses are the interface addresses Loomplane may confirm as
// this machine's public address, IPv4 first. Private, loopback,
// link-local, CGNAT and unique local addresses are left out. The control
// plane fetches a nonce from each before it routes to one, so a candidate
// that doesn't answer from outside does no harm.
func publicAddresses(list func() ([]net.Addr, error)) []string {
	addrs, err := list()
	if err != nil {
		return nil
	}
	ips := make([]string, 0, len(addrs))
	for _, a := range addrs {
		switch v := a.(type) {
		case *net.IPNet:
			ips = append(ips, v.IP.String())
		case *net.IPAddr:
			ips = append(ips, v.IP.String())
		}
	}
	return nodeproto.PublicAddresses(ips)
}

// parseNvidia turns "name, memory" CSV lines into GPUs and skips bad lines.
func parseNvidia(out []byte) []nodeproto.GPU {
	var gpus []nodeproto.GPU
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.LastIndex(line, ",")
		if i < 0 {
			continue
		}
		mem, err := strconv.ParseInt(strings.TrimSpace(line[i+1:]), 10, 64)
		name := strings.TrimSpace(line[:i])
		if err != nil || name == "" {
			continue
		}
		gpus = append(gpus, nodeproto.GPU{Vendor: "nvidia", Model: name, MemoryMiB: mem})
	}
	return gpus
}

// pingRuntime returns the first socket that answers GET /_ping.
func pingRuntime(ctx context.Context, sockets []string) *nodeproto.Runtime {
	for _, sock := range sockets {
		if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		client := &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}},
		}
		if _, err := socketGet(ctx, client, "/_ping"); err != nil {
			client.CloseIdleConnections()
			continue
		}
		rt := &nodeproto.Runtime{Socket: sock}
		if body, err := socketGet(ctx, client, "/version"); err == nil {
			var v struct{ Version string }
			if json.Unmarshal(body, &v) == nil {
				rt.Version = v.Version
			}
		}
		client.CloseIdleConnections()
		return rt
	}
	return nil
}

func socketGet(ctx context.Context, c *http.Client, path string) ([]byte, error) {
	u := url.URL{Scheme: "http", Host: "unix", Path: path}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// checkPorts reports "busy" if port 80 or 443 is taken.
func checkPorts() string {
	return portsState(tryBind, dialLocal)
}

// portsState decides free or busy for ports 80 and 443. A bind that fails with
// permission denied says nothing about the port: an unprivileged user cannot
// bind below 1024 even when it is free. In that case it falls back to looking
// for a listener, so a non-root run still reports "busy" correctly. Any other
// bind failure (address in use) means busy.
func portsState(bind func(port int) error, listening func(port int) bool) string {
	for _, p := range []int{80, 443} {
		err := bind(p)
		switch {
		case err == nil:
		case errors.Is(err, syscall.EACCES) || errors.Is(err, os.ErrPermission):
			if listening(p) {
				return "busy"
			}
		default:
			return "busy"
		}
	}
	return "free"
}

func tryBind(port int) error {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	return l.Close()
}

func dialLocal(port int) bool {
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	c, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func osName(ctx context.Context) string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(out))
		}
		return "macOS"
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				return strings.Trim(v, `"`)
			}
		}
	}
	return runtime.GOOS
}

func kernelVersion(ctx context.Context) string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return string(bytes.TrimSpace(b))
	}
	if out, err := exec.CommandContext(ctx, "uname", "-r").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func memoryMiB(ctx context.Context) int64 {
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
				f := strings.Fields(rest)
				if len(f) > 0 {
					if kb, err := strconv.ParseInt(f[0], 10, 64); err == nil {
						return kb / 1024
					}
				}
			}
		}
		return 0
	}
	if out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output(); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			return n / (1024 * 1024)
		}
	}
	return 0
}
