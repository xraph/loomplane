package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func fakeOpts(t *testing.T) DetectOptions {
	t.Helper()
	return DetectOptions{
		DockerSockets: []string{filepath.Join(t.TempDir(), "none.sock")},
		PodmanSockets: []string{filepath.Join(t.TempDir(), "none.sock")},
		NvidiaSMI:     func(context.Context) ([]byte, error) { return nil, errors.New("not found") },
		Hostname:      func() (string, error) { return "box", nil },
		Ports:         func() string { return "free" },
	}
}

func TestDetectNoDocker(t *testing.T) {
	inv, err := Detect(context.Background(), fakeOpts(t))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Docker != nil || inv.Podman != nil || len(inv.GPUs) != 0 {
		t.Fatalf("%+v", inv)
	}
	if inv.Hostname != "box" || inv.Ports80443 != "free" || inv.Arch == "" || inv.CPUs < 1 {
		t.Fatalf("%+v", inv)
	}
}

func TestDetectNvidiaParse(t *testing.T) {
	o := fakeOpts(t)
	o.NvidiaSMI = func(context.Context) ([]byte, error) {
		return []byte("NVIDIA GeForce RTX 4090, 24564\nTesla T4, 15360\n\n"), nil
	}
	inv, _ := Detect(context.Background(), o)
	if len(inv.GPUs) != 2 || inv.GPUs[0] != (nodeproto.GPU{Vendor: "nvidia", Model: "NVIDIA GeForce RTX 4090", MemoryMiB: 24564}) || inv.GPUs[1].MemoryMiB != 15360 {
		t.Fatalf("%+v", inv.GPUs)
	}
}

func TestParseNvidiaSkipsGarbage(t *testing.T) {
	if g := parseNvidia([]byte("bad line\nx, notanumber\n")); len(g) != 0 {
		t.Fatalf("%v", g)
	}
}

func TestDetectEndpointsFromConfig(t *testing.T) {
	o := fakeOpts(t)
	o.Config = &Config{Endpoints: []nodeproto.Endpoint{eDocker}}
	inv, _ := Detect(context.Background(), o)
	if len(inv.Endpoints) != 1 || inv.Endpoints[0] != eDocker {
		t.Fatalf("%v", inv.Endpoints)
	}
}

func TestDetectDockerOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OK")) })
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"Version": "27.1.0"})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	o := fakeOpts(t)
	o.DockerSockets = []string{sock}
	inv, _ := Detect(context.Background(), o)
	if inv.Docker == nil || inv.Docker.Socket != sock || inv.Docker.Version != "27.1.0" {
		t.Fatalf("%+v", inv.Docker)
	}
}

func TestDockerSocketCandidates(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///custom/d.sock")
	got := dockerSockets()
	if len(got) != 2 || got[0] != "/var/run/docker.sock" || got[1] != "/custom/d.sock" {
		t.Fatalf("%v", got)
	}
	t.Setenv("DOCKER_HOST", "tcp://1.2.3.4:2375")
	if got := dockerSockets(); len(got) != 1 {
		t.Fatalf("tcp host must be ignored: %v", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := podmanSockets(); len(got) != 2 || got[1] != "/run/user/1000/podman/podman.sock" {
		t.Fatalf("%v", got)
	}
}

func TestPortStateFallsBackToDialOnPermissionDenied(t *testing.T) {
	denied := func(int) error { return &net.OpError{Err: os.NewSyscallError("bind", syscall.EACCES)} }
	free := func(int) error { return nil }
	inUse := func(int) error { return &net.OpError{Err: os.NewSyscallError("bind", syscall.EADDRINUSE)} }
	noListener := func(int) bool { return false }
	listener := func(p int) bool { return p == 443 }

	if got := portsState(free, noListener); got != "free" {
		t.Fatalf("free: %s", got)
	}
	if got := portsState(inUse, noListener); got != "busy" {
		t.Fatalf("in use: %s", got)
	}
	if got := portsState(denied, noListener); got != "free" {
		t.Fatalf("denied, nothing listening must be free: %s", got)
	}
	if got := portsState(denied, listener); got != "busy" {
		t.Fatalf("denied, listener on 443 must be busy: %s", got)
	}
}

func TestProbeHandlerWritesInventory(t *testing.T) {
	a, b := net.Pipe()
	go func() {
		_ = ProbeHandler(func() DetectOptions { return fakeOpts(t) })(context.Background(), nodeproto.StreamHeader{}, b)
		_ = b.Close()
	}()
	var inv nodeproto.Inventory
	if err := json.NewDecoder(a).Decode(&inv); err != nil || inv.Hostname != "box" {
		t.Fatalf("%+v %v", inv, err)
	}
}
