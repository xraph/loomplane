package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// fixtureProbes reads the cgroup fixture tree under testdata/cgroup/<name>
// copied to a temp dir, after mutate has changed it. The node's own process
// is 4242 in every fixture.
func fixtureProbes(t *testing.T, name string, mutate func(root string)) *hostProbes {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "cgroup", name))); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(root)
	}
	p := newHostProbes(nil)
	p.sys, p.proc, p.pid = filepath.Join(root, "sys"), filepath.Join(root, "proc"), 4242
	return p
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The numbers and failures below are what effectiveCapacityCommand's Python
// prints for the same trees, run once with its /sys/fs/cgroup and /proc
// pointed at the fixture and its own PID set to 4242.
func TestCgroupV2Fixture(t *testing.T) {
	cg := func(root, rel string) string { return filepath.Join(root, "sys", "fs", "cgroup", rel) }
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, root string)
		want   nodeproto.CgroupLimits
		err    string
	}{
		{name: "fixture", want: nodeproto.CgroupLimits{CPU: 3500, Memory: 6442450944}},
		{name: "no limits", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/docker.service/cpu.max"), "max 100000")
			writeFile(t, cg(root, "system.slice/docker.service/memory.max"), "max")
			writeFile(t, cg(root, "system.slice/loomplane-node.service/memory.max"), "max")
		}, want: nodeproto.CgroupLimits{CPU: 5000, Memory: 1152921504606846976}},
		{name: "cpuset caps cpu", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/cpuset.cpus.effective"), "2")
		}, want: nodeproto.CgroupLimits{CPU: 1000, Memory: 6442450944}},
		{name: "no dockerd", mutate: func(_ *testing.T, root string) {
			_ = os.RemoveAll(filepath.Join(root, "proc", "812"))
		}, err: "exact local Docker daemon required"},
		{name: "two dockerds", mutate: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "proc", "900", "comm"), "dockerd")
			writeFile(t, filepath.Join(root, "proc", "900", "cgroup"), "0::/system.slice/docker.service")
		}, err: "exact local Docker daemon required"},
		{name: "missing controller", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "cgroup.controllers"), "cpu memory")
		}, err: "required cgroup v2 controllers unavailable"},
		{name: "escaping entry", mutate: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "proc", "812", "cgroup"), "0::/system.slice/../../etc")
		}, err: "cgroup path"},
		{name: "hidden hierarchy", mutate: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "proc", "812", "cgroup"), "0::/system.slice/gone.service")
		}, err: "hidden cgroup hierarchy"},
		{name: "enabled limit missing", mutate: func(_ *testing.T, root string) {
			_ = os.Remove(cg(root, "system.slice/docker.service/cpu.max"))
		}, err: "enabled controller limit unavailable"},
		{name: "unknown enabled controller", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/cgroup.subtree_control"), "cpu memory pids weird")
		}, err: "unknown enabled controller"},
		{name: "root cpuset missing", mutate: func(_ *testing.T, root string) {
			_ = os.Remove(cg(root, "cpuset.cpus.effective"))
		}, err: "root effective CPU set unavailable"},
		{name: "invalid quota", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/docker.service/cpu.max"), "-1 100000")
		}, err: "invalid CPU quota"},
		{name: "invalid period", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/docker.service/cpu.max"), "max 0")
		}, err: "invalid CPU period"},
		{name: "invalid memory", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/docker.service/memory.max"), "0")
		}, err: "invalid memory limit"},
		{name: "empty cpuset", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/cpuset.cpus.effective"), "")
		}, err: "unknown effective CPU set"},
		{name: "invalid cpu range", mutate: func(t *testing.T, root string) {
			writeFile(t, cg(root, "system.slice/cpuset.cpus.effective"), "3-1")
		}, err: "invalid CPU range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fixtureProbes(t, "v2", func(root string) {
				if tc.mutate != nil {
					tc.mutate(t, root)
				}
			})
			got, err := p.cgroup()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %+v, %v; want an error with %q", got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// The SSH probe reads only cgroup v2: on a v1 tree its Python stops with
// FileNotFoundError for cgroup.controllers, so the native probe fails closed
// too, and capacity stays unknown on both paths.
func TestCgroupV1Fixture(t *testing.T) {
	got, err := fixtureProbes(t, "v1", nil).cgroup()
	if err == nil || !strings.Contains(err.Error(), "cgroup v2") {
		t.Fatalf("got %+v, %v; want a cgroup v2 error", got, err)
	}
}

func TestFilesystemArgMustBeDockerRoot(t *testing.T) {
	root := t.TempDir()
	info := map[string]any{"ID": "daemon-1", "DockerRootDir": root}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(info) })
	socket := fakeDockerAPI(t, mux)
	p := newHostProbes(func(context.Context) string { return socket })
	ctx := context.Background()
	for _, arg := range []string{"", "relative/docker", root + "/", root + "/../" + filepath.Base(root), "/", root + "\x00"} {
		if _, err := p.filesystem(ctx, arg); err == nil {
			t.Errorf("%q: no error", arg)
		}
	}
	fs, err := p.filesystem(ctx, root)
	if err != nil || fs.TotalBytes <= 0 || fs.FreeBytes < 0 || fs.FreeBytes > fs.TotalBytes {
		t.Fatalf("%+v %v", fs, err)
	}
	// Podman's compat API may leave DockerRootDir out.
	delete(info, "DockerRootDir")
	if _, err := p.filesystem(ctx, root); err == nil || !strings.Contains(err.Error(), "DockerRootDir") {
		t.Fatalf("missing DockerRootDir: %v", err)
	}
	if _, err := newHostProbes(func(context.Context) string { return "" }).filesystem(ctx, root); err == nil {
		t.Fatal("no socket: no error")
	}
}

func TestListenersParseProcNet(t *testing.T) {
	proc := t.TempDir()
	head := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	writeFile(t, filepath.Join(proc, "net", "tcp"), head+
		"   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0000000000000000 100 0 0 10 0\n"+
		"   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1002 1 0000000000000000 100 0 0 10 0\n"+
		"   2: 0100007F:07E3 0100007F:A000 01 00000000:00000000 00:00000000 00000000     0        0 1003 1 0000000000000000 20 4 30 10 -1")
	writeFile(t, filepath.Join(proc, "net", "tcp6"), strings.TrimSuffix(head, "\n")+"\n"+
		"   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2001 1 0000000000000000 100 0 0 10 0\n"+
		"   1: 00000000000000000000000000000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2002 1 0000000000000000 100 0 0 10 0")
	p := newHostProbes(nil)
	p.proc = proc
	got, err := p.listeners()
	if err != nil || !reflect.DeepEqual(got, nodeproto.Listeners{Ports: []int{80, 443}}) {
		t.Fatalf("%+v %v", got, err)
	}

	// No IPv6 is no IPv6 listeners, as ss reports it; a missing or garbled
	// IPv4 table can't be read, so the probe fails rather than say free.
	_ = os.Remove(filepath.Join(proc, "net", "tcp6"))
	if got, err := p.listeners(); err != nil || !reflect.DeepEqual(got, nodeproto.Listeners{Ports: []int{80}}) {
		t.Fatalf("without tcp6: %+v %v", got, err)
	}
	writeFile(t, filepath.Join(proc, "net", "tcp"), head+"   0: garbage")
	if _, err := p.listeners(); err == nil {
		t.Fatal("garbled table: no error")
	}
	_ = os.Remove(filepath.Join(proc, "net", "tcp"))
	if _, err := p.listeners(); err == nil {
		t.Fatal("missing table: no error")
	}
}

func TestGPUProbeNoSmi(t *testing.T) {
	sys := t.TempDir()
	writeFile(t, filepath.Join(sys, "bus", "pci", "devices", "0000:01:00.0", "vendor"), "0x10DE")
	writeFile(t, filepath.Join(sys, "bus", "pci", "devices", "0000:00:02.0", "vendor"), "0x8086")
	p := newHostProbes(nil)
	p.sys = sys
	p.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	p.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran a command that isn't there")
		return nil, nil
	}
	got := gpuOf(t, p)
	if got != (nodeproto.GPUProbe{SmiExit: 127, NvidiaPCI: true}) {
		t.Fatalf("%+v", got)
	}
}

func TestGPUProbeRunsSmi(t *testing.T) {
	p := newHostProbes(nil)
	p.sys = t.TempDir()
	var ran []string
	p.lookPath = func(name string) (string, error) {
		if name == "nvidia-smi" || name == "nvidia-ctk" {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	p.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return []byte("0, GPU-1, NVIDIA L4, 23034\n"), nil
	}
	got := gpuOf(t, p)
	want := nodeproto.GPUProbe{Smi: "0, GPU-1, NVIDIA L4, 23034\n", Toolkit: true}
	if got != want || len(ran) != 1 || ran[0] != "/usr/bin/nvidia-smi --query-gpu=index,uuid,name,memory.total --format=csv,noheader,nounits" {
		t.Fatalf("%+v %v", got, ran)
	}

	// A failing nvidia-smi reports its exit code and what it printed; one
	// that outlives the timeout reports 124 and nothing.
	p.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("partial"), exec.CommandContext(context.Background(), "sh", "-c", "exit 9").Run()
	}
	if got := gpuOf(t, p); got.SmiExit != 9 || got.Smi != "partial" {
		t.Fatalf("failing smi %+v", got)
	}
	p.run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return []byte("late"), ctx.Err()
	}
	p.gpuTimeout = 1
	if got := gpuOf(t, p); got.SmiExit != 124 || got.Smi != "" {
		t.Fatalf("slow smi %+v", got)
	}
	p.gpuTimeout = gpuTimeout
	p.run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exec format error") }
	if got := gpuOf(t, p); got.SmiExit != 127 {
		t.Fatalf("unstartable smi %+v", got)
	}
}

func gpuOf(t *testing.T, p *hostProbes) nodeproto.GPUProbe {
	t.Helper()
	got, err := p.gpu(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}
