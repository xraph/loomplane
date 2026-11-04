package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// cgroupUnlimited is where the SSH probe starts both limits, 2**60.
const cgroupUnlimited = 1 << 60

var cpuSetPart = regexp.MustCompile(`^\d+(?:-\d+)?$`)

// cgroup reads the CPU (millicpus) and memory (bytes) limits on dockerd and
// on this process, the way effectiveCapacityCommand does: every cgroup v2
// directory from each one's cgroup up to the root, and the tightest cpu.max,
// memory.max and cpuset.cpus.effective among them. Each of the Python's
// asserts is an error here, with its message. cgroup v1 and hybrid hosts have
// no cgroup.controllers at the root, which the Python can't read either, so
// they fail closed too.
func (p *hostProbes) cgroup() (nodeproto.CgroupLimits, error) {
	root := filepath.Join(p.sys, "fs", "cgroup")
	raw, err := os.ReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		return nodeproto.CgroupLimits{}, fmt.Errorf("this host has no cgroup v2 hierarchy, and cgroup v1 limits are not read: %w", err)
	}
	available := strings.Fields(string(raw))
	for _, c := range []string{"cpu", "memory", "cpuset"} {
		if !slices.Contains(available, c) {
			return nodeproto.CgroupLimits{}, errors.New("required cgroup v2 controllers unavailable")
		}
	}
	dockerd, err := p.dockerdPID()
	if err != nil {
		return nodeproto.CgroupLimits{}, err
	}
	var dirs []string
	for _, pid := range []string{dockerd, strconv.Itoa(p.pid)} {
		chain, err := cgroupChain(root, filepath.Join(p.proc, pid, "cgroup"))
		if err != nil {
			return nodeproto.CgroupLimits{}, err
		}
		dirs = append(dirs, chain...)
	}
	cpu, memory := big.NewInt(cgroupUnlimited), big.NewInt(cgroupUnlimited)
	lower := func(limit, v *big.Int) {
		if v.Cmp(limit) < 0 {
			limit.Set(v)
		}
	}
	for _, dir := range dirs {
		value, ok, err := limitFile(root, available, dir, "cpu", "cpu.max")
		if err != nil {
			return nodeproto.CgroupLimits{}, err
		}
		if ok {
			quotaPeriod := strings.Fields(value)
			if len(quotaPeriod) != 2 {
				return nodeproto.CgroupLimits{}, errors.New("invalid CPU limit")
			}
			quota, period := quotaPeriod[0], quotaPeriod[1]
			per, valid := positive(period)
			if !valid {
				return nodeproto.CgroupLimits{}, errors.New("invalid CPU period")
			}
			if quota != "max" {
				q, valid := positive(quota)
				if !valid {
					return nodeproto.CgroupLimits{}, errors.New("invalid CPU quota")
				}
				lower(cpu, q.Quo(q.Mul(q, big.NewInt(1000)), per))
			}
		}
		value, ok, err = limitFile(root, available, dir, "memory", "memory.max")
		if err != nil {
			return nodeproto.CgroupLimits{}, err
		}
		if ok && value != "max" {
			m, valid := positive(value)
			if !valid {
				return nodeproto.CgroupLimits{}, errors.New("invalid memory limit")
			}
			lower(memory, m)
		}
		value, ok, err = limitFile(root, available, dir, "cpuset", "cpuset.cpus.effective")
		if err != nil {
			return nodeproto.CgroupLimits{}, err
		}
		if ok {
			count, err := cpuCount(value)
			if err != nil {
				return nodeproto.CgroupLimits{}, err
			}
			lower(cpu, count.Mul(count, big.NewInt(1000)))
		}
	}
	// Both are at most 2**60 now.
	return nodeproto.CgroupLimits{CPU: cpu.Int64(), Memory: memory.Int64()}, nil
}

// dockerdPID is the one process named dockerd. Like the Python, it skips
// processes it can't read and refuses none or several.
func (p *hostProbes) dockerdPID() (string, error) {
	entries, _ := os.ReadDir(p.proc) //nolint:errcheck // an unreadable /proc finds no dockerd, which is refused below
	var pids []string
	for _, e := range entries {
		if name := e.Name(); name[0] >= '0' && name[0] <= '9' {
			comm, err := os.ReadFile(filepath.Join(p.proc, name, "comm"))
			if err == nil && strings.TrimSpace(string(comm)) == "dockerd" {
				pids = append(pids, name)
			}
		}
	}
	if len(pids) != 1 {
		return "", errors.New("exact local Docker daemon required")
	}
	return pids[0], nil
}

// cgroupChain is the process's cgroup v2 directory, read from its
// /proc/<pid>/cgroup, and each parent up to root, leaf first.
func cgroupChain(root, file string) ([]string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	// Python's splitlines breaks on these too. Empty lines never match.
	lines := strings.FieldsFunc(string(raw), func(r rune) bool {
		return strings.ContainsRune("\n\r\v\f\x1c\x1d\x1e\u0085  ", r)
	})
	i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "0::") })
	if i < 0 {
		return nil, fmt.Errorf("%s has no cgroup v2 entry", file)
	}
	entry := lines[i][3:]
	if !strings.HasPrefix(entry, "/") || slices.Contains(strings.Split(entry, "/"), "..") {
		return nil, fmt.Errorf("cgroup path %q is not absolute and inside the hierarchy", entry)
	}
	current := filepath.Join(root, strings.TrimLeft(entry, "/"))
	// entry has no "..", so current stays under root.
	if fi, err := os.Stat(current); err != nil || !fi.IsDir() { //nolint:gosec // G703: see above
		return nil, errors.New("hidden cgroup hierarchy")
	}
	var chain []string
	for current != root {
		chain = append(chain, current)
		parent := filepath.Dir(current)
		if parent == current {
			return nil, errors.New("hidden cgroup hierarchy")
		}
		current = parent
	}
	return append(chain, root), nil
}

// limitFile is the Python's limit_file: the trimmed contents of dir/name and
// true, or false when the file is missing and the controller is provably
// off for dir, which then inherits its parent's limit.
func limitFile(root string, available []string, dir, controller, name string) (value string, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err == nil {
		return strings.TrimSpace(string(raw)), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	if dir == root {
		if controller == "cpuset" {
			return "", false, errors.New("root effective CPU set unavailable")
		}
		return "", false, nil
	}
	raw, err = os.ReadFile(filepath.Join(filepath.Dir(dir), "cgroup.subtree_control"))
	if err != nil {
		return "", false, err
	}
	enabled := strings.Fields(string(raw))
	for _, c := range enabled {
		if !slices.Contains(available, c) {
			return "", false, errors.New("unknown enabled controller")
		}
	}
	if slices.Contains(enabled, controller) {
		return "", false, errors.New("enabled controller limit unavailable")
	}
	return "", false, nil
}

// cpuCount counts the CPUs in a cpuset list such as 0-3,6, whose ranges must
// ascend without overlapping.
func cpuCount(set string) (*big.Int, error) {
	if set == "" {
		return nil, errors.New("unknown effective CPU set")
	}
	count, previous := new(big.Int), big.NewInt(-1)
	for part := range strings.SplitSeq(set, ",") {
		if !cpuSetPart.MatchString(part) {
			return nil, errors.New("invalid CPU set")
		}
		lowStr, highStr, ranged := strings.Cut(part, "-")
		if !ranged {
			highStr = lowStr
		}
		low, _ := new(big.Int).SetString(lowStr, 10)
		high, _ := new(big.Int).SetString(highStr, 10)
		if high.Cmp(low) < 0 || low.Cmp(previous) <= 0 {
			return nil, errors.New("invalid CPU range")
		}
		count.Add(count, new(big.Int).Sub(high, low))
		count.Add(count, big.NewInt(1))
		previous = high
	}
	return count, nil
}

// positive parses s as the Python's isdigit and int(s) > 0 accept it. Only
// ASCII digits pass; cgroupfs writes nothing else.
func positive(s string) (*big.Int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return nil, false
	}
	v, ok := new(big.Int).SetString(s, 10)
	return v, ok && v.Sign() > 0
}

// gpu is gpuProbeCommand: nvidia-smi's exit code (124 on timeout, 127 when
// it is missing or won't start) and output, whether any PCI device is
// NVIDIA's, and whether the container toolkit is on PATH. It never fails.
func (p *hostProbes) gpu(ctx context.Context) (nodeproto.GPUProbe, error) {
	out := nodeproto.GPUProbe{SmiExit: 127}
	if smi, err := p.lookPath("nvidia-smi"); err == nil {
		runCtx, cancel := context.WithTimeout(ctx, p.gpuTimeout)
		stdout, err := p.run(runCtx, smi, "--query-gpu=index,uuid,name,memory.total", "--format=csv,noheader,nounits")
		timedOut := runCtx.Err() != nil
		cancel()
		var exit *exec.ExitError
		switch {
		case err == nil:
			out.SmiExit, out.Smi = 0, string(stdout)
		case timedOut:
			out.SmiExit = 124
		case errors.As(err, &exit):
			out.SmiExit, out.Smi = exit.ExitCode(), string(stdout)
		}
	}
	vendors, _ := filepath.Glob(filepath.Join(p.sys, "bus", "pci", "devices", "*", "vendor")) //nolint:errcheck // the pattern is fixed and valid
	for _, f := range vendors {
		if v, err := os.ReadFile(f); err == nil && strings.ToLower(strings.TrimSpace(string(v))) == "0x10de" {
			out.NvidiaPCI = true
		}
	}
	_, cli := p.lookPath("nvidia-container-cli")
	_, ctk := p.lookPath("nvidia-ctk")
	out.Toolkit = cli == nil || ctk == nil
	return out, nil
}

// filesystem is the capacity of the filesystem holding the Docker root,
// which root must name exactly as the daemon's /info does. That is the
// check storageCapacityCommand makes before it reads the filesystem, so the
// probe can't be pointed at any other path.
func (p *hostProbes) filesystem(ctx context.Context, root string) (nodeproto.Filesystem, error) {
	if !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsRune(root, 0) {
		return nodeproto.Filesystem{}, errors.New("the filesystem probe takes the Docker root as an absolute, clean path")
	}
	socket := p.socket(ctx)
	if socket == "" {
		return nodeproto.Filesystem{}, errors.New("no Docker or Podman socket answered on this node")
	}
	api := dockerAPI(socket)
	defer api.CloseIdleConnections()
	var info struct{ DockerRootDir string }
	if err := dockerGetJSON(ctx, api, "/info", &info); err != nil {
		return nodeproto.Filesystem{}, fmt.Errorf("reading the daemon's /info: %w", err)
	}
	if info.DockerRootDir == "" {
		return nodeproto.Filesystem{}, errors.New("the daemon's /info has no DockerRootDir")
	}
	if info.DockerRootDir != root {
		return nodeproto.Filesystem{}, fmt.Errorf("%s is not the daemon's Docker root", root)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return nodeproto.Filesystem{}, err
	}
	// statvfs reports f_frsize, which Linux leaves zero on some filesystems
	// and glibc then fills from f_bsize.
	frsize := uint64(st.Frsize) //nolint:gosec // a block size is never negative
	if frsize == 0 {
		frsize = uint64(st.Bsize) //nolint:gosec // as above
	}
	if frsize == 0 || st.Blocks > math.MaxInt64/frsize {
		return nodeproto.Filesystem{}, errors.New("the Docker root's filesystem reports an impossible size")
	}
	return nodeproto.Filesystem{TotalBytes: int64(st.Blocks * frsize), FreeBytes: int64(st.Bavail * frsize)}, nil //nolint:gosec // checked above; free is at most total
}

// ingressPorts are the ports the SSH listener check asks ss about.
var ingressPorts = []int{80, 443, 2019}

// listeners reads /proc/net/tcp and /proc/net/tcp6 for LISTEN sockets on the
// ingress ports, as ss -ltn does. A host without IPv6 has no tcp6 table and
// so no IPv6 listeners; any other read or parse failure is an error, so the
// adoption check that asks fails rather than see the ports as free.
func (p *hostProbes) listeners() (nodeproto.Listeners, error) {
	held := map[int]bool{}
	for _, table := range []string{"tcp", "tcp6"} {
		raw, err := os.ReadFile(filepath.Join(p.proc, "net", table))
		if table == "tcp6" && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nodeproto.Listeners{}, fmt.Errorf("reading the TCP socket table: %w", err)
		}
		if err := listening(raw, held); err != nil {
			return nodeproto.Listeners{}, err
		}
	}
	ports := []int{}
	for _, port := range ingressPorts {
		if held[port] {
			ports = append(ports, port)
		}
	}
	return nodeproto.Listeners{Ports: ports}, nil
}

// tcpListen is the LISTEN state in /proc/net/tcp.
const tcpListen = "0A"

// listening marks in held the local port of each LISTEN socket in a
// /proc/net/tcp table. The first line is the header.
func listening(table []byte, held map[int]bool) error {
	lines := strings.Split(strings.TrimSpace(string(table)), "\n")
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 4 {
			return errors.New("malformed TCP socket table")
		}
		i := strings.LastIndexByte(f[1], ':')
		port, err := strconv.ParseUint(f[1][i+1:], 16, 16)
		if i < 0 || err != nil {
			return errors.New("malformed TCP socket table")
		}
		if f[3] == tcpListen {
			held[int(port)] = true
		}
	}
	return nil
}
