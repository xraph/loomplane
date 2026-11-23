package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// fakeRunnerService records what the runner commands asked of the service.
type fakeRunnerService struct {
	mu              sync.Mutex
	calls           []string
	active, enabled bool
}

func (f *fakeRunnerService) note(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}
func (f *fakeRunnerService) Install(context.Context, string, string) error {
	f.note("install")
	return nil
}
func (f *fakeRunnerService) Start(context.Context) error {
	f.note("start")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.enabled = true, true
	return nil
}
func (f *fakeRunnerService) Stop(context.Context) error {
	f.note("stop")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.enabled = false, false
	return nil
}
func (f *fakeRunnerService) Remove(context.Context) error { f.note("remove"); return nil }
func (f *fakeRunnerService) State(context.Context) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, f.enabled
}
func (f *fakeRunnerService) did(c string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, c)
}

// runnerJoinServer is a Loomplane that makes whoever joins a runner of
// rpool_1.
func runnerJoinServer(t *testing.T) (*httptest.Server, *atomic.Pointer[client.JoinRequest]) {
	t.Helper()
	var got atomic.Pointer[client.JoinRequest]
	mux := http.NewServeMux()
	mux.HandleFunc("/node/v1/challenge", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/v1/nodes/join", func(w http.ResponseWriter, req *http.Request) {
		var j client.JoinRequest
		_ = json.NewDecoder(req.Body).Decode(&j)
		got.Store(&j)
		_ = json.NewEncoder(w).Encode(client.JoinResult{NodeID: "node_r1", OrgID: "org_1", IdentityGeneration: 1, Endpoints: j.Endpoints,
			Role: "runner", PoolID: "rpool_1", PoolConnection: "conn_pool"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &got
}

// runnerRig is a Linux root machine with KVM, whose runner service goes
// nowhere.
func runnerRig(t *testing.T) (*cmdRig, *fakeRunnerService, string, *[]string) {
	t.Helper()
	r := newCmdRig(t)
	svc := &fakeRunnerService{}
	var ran []string
	r.opts.runnerSvc = svc
	r.opts.kvm = func() error { return nil }
	r.opts.runRunner = func(_ context.Context, st State) error { ran = append(ran, st.Dir); return nil }
	fcConfig := filepath.Join(t.TempDir(), "firecracker.json")
	if err := os.WriteFile(fcConfig, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return r, svc, fcConfig, &ran
}

func TestRunnerStartJoinsAndStaysUp(t *testing.T) {
	srv, got := runnerJoinServer(t)
	r, svc, fcConfig, _ := runnerRig(t)
	err := r.run("runner", "start", "--state-dir", r.dir, "--url", srv.URL, "--token", "lpj_live_runner",
		"--firecracker-config", fcConfig, "--relay-allow", "10.0.0.0/16")
	if err != nil {
		t.Fatalf("runner start: %v\n%s", err, r.out)
	}
	j := got.Load()
	if j.Role != "runner" || !slices.Equal(j.Endpoints, []nodeproto.Endpoint{"probe", "firecracker", "relay"}) {
		t.Fatalf("join request %+v", j)
	}
	cfg, err := (State{Dir: r.dir}).LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Role != RoleRunner || cfg.PoolID != "rpool_1" || cfg.PoolConnection != "conn_pool" || cfg.FirecrackerConfig != fcConfig || cfg.DockerSocket != "" {
		t.Fatalf("config %+v", cfg)
	}
	ceiling, err := (State{Dir: r.dir}).ReadCeiling(func(uint32) bool { return true })
	if err != nil || !slices.Equal(ceiling.Relay, []string{"10.0.0.0/16"}) {
		t.Fatalf("ceiling %+v %v", ceiling, err)
	}
	if !svc.did("install") || !svc.did("start") {
		t.Fatalf("service calls %v", svc.calls)
	}
}

func TestRunnerStopStaysStoppedAndStartBringsItBack(t *testing.T) {
	srv, _ := runnerJoinServer(t)
	r, svc, fcConfig, _ := runnerRig(t)
	if err := r.run("runner", "start", "--state-dir", r.dir, "--url", srv.URL, "--token", "lpj_live_runner", "--firecracker-config", fcConfig); err != nil {
		t.Fatal(err)
	}
	if err := r.run("runner", "stop", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, stoppedFile)); err != nil {
		t.Fatal("stop left no mark for status")
	}
	r.out.Reset()
	if err := r.run("runner", "status", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.out.String(), "stopped by its owner") {
		t.Fatalf("status:\n%s", r.out)
	}
	// Start again, as joined: no new join, the service comes back.
	if err := r.run("runner", "start", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	if active, enabled := svc.State(context.Background()); !active || !enabled {
		t.Fatal("runner start didn't bring the service back")
	}
	if _, err := os.Stat(filepath.Join(r.dir, stoppedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("start kept the stop mark")
	}
}

func TestRunnerStartOnceInstallsNothing(t *testing.T) {
	srv, _ := runnerJoinServer(t)
	r, svc, fcConfig, ran := runnerRig(t)
	if err := r.run("runner", "start", "--state-dir", r.dir, "--url", srv.URL, "--token", "lpj_live_runner", "--firecracker-config", fcConfig, "--once"); err != nil {
		t.Fatal(err)
	}
	if svc.did("install") || svc.did("start") {
		t.Fatalf("--once touched the service: %v", svc.calls)
	}
	if len(*ran) != 1 || (*ran)[0] != r.dir {
		t.Fatalf("ran %v", *ran)
	}
}

func TestRunnerStartRefusesBeforeSpendingAToken(t *testing.T) {
	srv, got := runnerJoinServer(t)
	for _, tc := range []struct {
		name  string
		setup func(r *cmdRig)
		args  []string
		want  string
	}{
		{"not Linux", func(r *cmdRig) { r.opts.goos = "darwin" }, nil, "need Linux with KVM"},
		{"not root", func(r *cmdRig) { r.opts.euid = func() int { return 1000 } }, nil, "needs root"},
		{"no KVM", func(r *cmdRig) { r.opts.kvm = func() error { return errors.New("/dev/kvm isn't here") } }, nil, "/dev/kvm"},
		{"no Firecracker config", nil, []string{"--firecracker-config", "/nonexistent/firecracker.json"}, "Firecracker host config"},
		{"a relay that isn't a CIDR", nil, []string{"--relay-allow", "10.0.0.5"}, "CIDR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, fcConfig, _ := runnerRig(t)
			if tc.setup != nil {
				tc.setup(r)
			}
			args := []string{"runner", "start", "--state-dir", r.dir, "--url", srv.URL, "--token", "lpj_live_runner", "--firecracker-config", fcConfig}
			args = append(args, tc.args...)
			err := r.run(args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if got.Load() != nil {
				t.Fatal("the token was spent")
			}
		})
	}
}

func TestRunnerRelayAllowWidensTheRelay(t *testing.T) {
	srv, _ := runnerJoinServer(t)
	r, _, fcConfig, _ := runnerRig(t)
	if err := r.run("runner", "start", "--state-dir", r.dir, "--url", srv.URL, "--token", "lpj_live_runner", "--firecracker-config", fcConfig); err != nil {
		t.Fatal(err)
	}
	if err := r.run("runner", "relay", "allow", "--state-dir", r.dir, "192.168.1.0/24", "10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	ceiling, err := (State{Dir: r.dir}).ReadCeiling(func(uint32) bool { return true })
	if err != nil || !slices.Equal(ceiling.Relay, []string{"192.168.1.0/24", "10.0.0.0/8"}) {
		t.Fatalf("relay %v %v", ceiling.Relay, err)
	}
	if err := r.run("runner", "relay", "allow", "--state-dir", r.dir, "not-a-cidr"); err == nil {
		t.Fatal("a relay network that isn't a CIDR was allowed")
	}
}

func TestNodeStopAndStart(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	if err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "lpj_live_x", "--allow", "probe"); err != nil {
		t.Fatal(err)
	}
	if err := r.run("node", "stop", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, stoppedFile)); err != nil {
		t.Fatal("node stop left no mark")
	}
	if err := r.run("node", "start", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	if !r.svc.did("start") {
		t.Fatalf("node start didn't start the service: %v", r.svc.calls)
	}
	if _, err := os.Stat(filepath.Join(r.dir, stoppedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("node start kept the stop mark")
	}
}
