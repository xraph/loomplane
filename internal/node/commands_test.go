package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
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

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// fakeService records what the commands ask of the service manager.
type fakeService struct {
	mu        sync.Mutex
	installed []Unit
	calls     []string
	running   bool
	// startErrs are the errors the next Starts return, in order; runErr is
	// what Running returns.
	startErrs []error
	runErr    error
	// runningSeq, while it lasts, answers Running in turn before running.
	runningSeq []bool
	// grantChanged is what GrantDocker reports; grants counts its calls.
	grantChanged bool
	grants       int
}

func (f *fakeService) GrantDocker(context.Context) (bool, error) {
	f.note("grant-docker")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants++
	return f.grantChanged, nil
}

func (f *fakeService) note(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeService) Install(_ context.Context, u Unit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = append(f.installed, u)
	f.calls = append(f.calls, "install")
	return nil
}
func (f *fakeService) Start(context.Context) error {
	f.note("start")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.startErrs) == 0 {
		return nil
	}
	err := f.startErrs[0]
	f.startErrs = f.startErrs[1:]
	return err
}
func (f *fakeService) Stop(context.Context) error   { f.note("stop"); return nil }
func (f *fakeService) Remove(context.Context) error { f.note("remove"); return nil }
func (f *fakeService) Running(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runningSeq) > 0 {
		r := f.runningSeq[0]
		f.runningSeq = f.runningSeq[1:]
		return r, f.runErr
	}
	return f.running, f.runErr
}

func (f *fakeService) did(c string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, c)
}

type cmdRig struct {
	opts Options
	svc  *fakeService
	out  *bytes.Buffer
	dir  string
}

// newCmdRig is a Linux root machine whose service commands go nowhere.
func newCmdRig(t *testing.T) *cmdRig {
	t.Helper()
	r := &cmdRig{svc: &fakeService{}, out: &bytes.Buffer{}, dir: filepath.Join(shortDir(t), "state")}
	r.opts = Options{Version: "test", Out: r.out, Getenv: func(string) string { return "" },
		goos: "linux", euid: func() int { return 0 }, home: t.TempDir(), svc: r.svc, detect: quietDetect(),
		binary: func() (string, error) { return "/usr/local/bin/loomctl", nil }, chown: func(string, int, int) error { return nil },
		in: strings.NewReader(""), isTerminal: func() bool { return false }, serviceReady: func() error { return nil },
		systemBin: filepath.Join(t.TempDir(), "lib"), settle: time.Millisecond}
	return r
}

// shortDir is a temp dir with a path short enough for a Unix socket.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func (r *cmdRig) run(args ...string) error {
	c := cli.New(cli.Config{Name: "t", Output: r.out})
	for _, cmd := range Commands(r.opts) {
		if err := c.AddCommand(cmd); err != nil {
			return err
		}
	}
	return c.Run(append([]string{"t"}, args...))
}

func TestCommandsGroup(t *testing.T) {
	cmds := Commands(Options{})
	if len(cmds) != 1 || cmds[0].Name() != "node" {
		t.Fatalf("Commands returned %d commands", len(cmds))
	}
	names := make([]string, 0, len(cmds[0].Subcommands()))
	for _, s := range cmds[0].Subcommands() {
		names = append(names, s.Name())
	}
	for _, want := range []string{"join", "run", "status", "leave", "allow", "list", "revoke", "drain", "update", "verify-release"} {
		if !slices.Contains(names, want) {
			t.Errorf("no node %s; have %v", want, names)
		}
	}
}

func TestEverySubcommandRefusesWindows(t *testing.T) {
	r := newCmdRig(t)
	r.opts.goos = "windows"
	for _, args := range [][]string{
		{"join", "--token", "x", "--url", "http://x"}, {"run"}, {"status"}, {"leave"}, {"allow", "docker"}, {"list"}, {"revoke", "x"}, {"drain", "x"}, {"update", "--release", "v1.9.0"}, {"verify-release"},
	} {
		err := r.run(append([]string{"node"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "Linux and macOS") {
			t.Errorf("node %s on windows: %v", args[0], err)
		}
	}
}

// joinServer is a Loomplane that mints and accepts join tokens.
type joinServer struct {
	*httptest.Server
	scope  atomic.Pointer[client.NodeJoinScope]
	join   atomic.Pointer[client.JoinRequest]
	minted atomic.Int32
	auth   atomic.Pointer[string]
}

func newJoinServer(t *testing.T) *joinServer {
	t.Helper()
	js := &joinServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/node/v1/challenge", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/v1/nodes/join-tokens", func(w http.ResponseWriter, req *http.Request) {
		var s client.NodeJoinScope
		_ = json.NewDecoder(req.Body).Decode(&s)
		js.scope.Store(&s)
		a := req.Header.Get("Authorization")
		js.auth.Store(&a)
		js.minted.Add(1)
		_, _ = w.Write([]byte(`{"token":"lpj_live_minted","command":"x"}`))
	})
	mux.HandleFunc("/api/v1/nodes/join", func(w http.ResponseWriter, req *http.Request) {
		var j client.JoinRequest
		_ = json.NewDecoder(req.Body).Decode(&j)
		js.join.Store(&j)
		_ = json.NewEncoder(w).Encode(client.JoinResult{NodeID: "node_1", OrgID: "org_1", IdentityGeneration: 1, Endpoints: j.Endpoints})
	})
	js.Server = httptest.NewServer(mux)
	t.Cleanup(js.Close)
	return js
}

const secretKey = "lp_live_supersecretkey"

func TestJoinUsesLoginNotStoredKey(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	r.opts.Login = func() (string, string, error) { return js.URL, secretKey, nil }
	err := r.run("node", "join", "--state-dir", r.dir, "--allow", "probe,docker", "--label", "env=prod", "--label", "rack=7",
		"--cloud", "cloud_1", "--builder", "dedicated", "--edge")
	if err != nil {
		t.Fatalf("join: %v\n%s", err, r.out)
	}
	if js.minted.Load() != 1 || *js.auth.Load() != "Bearer "+secretKey {
		t.Fatalf("minted %d with %v", js.minted.Load(), js.auth.Load())
	}
	s := js.scope.Load()
	wantLabels := map[string]string{"env": "prod", "rack": "7", "build_workers": "dedicated"}
	if s.CloudID != "cloud_1" || !s.Edge || !slices.Equal(s.Endpoints, []nodeproto.Endpoint{"probe", "docker"}) || len(s.Labels) != 3 {
		t.Fatalf("scope %+v", s)
	}
	for k, v := range wantLabels {
		if s.Labels[k] != v {
			t.Errorf("scope label %s=%q", k, s.Labels[k])
		}
	}
	j := js.join.Load()
	if j.Token != "lpj_live_minted" || j.Name != "" || j.Labels["env"] != "prod" {
		t.Fatalf("join request %+v", j)
	}
	// Nothing under the state dir may hold the person's key, or the token.
	err = filepath.WalkDir(r.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(b, []byte("lp_live_")) || bytes.Contains(b, []byte("lpj_")) {
			t.Errorf("%s holds a credential", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := State{Dir: r.dir}.LoadConfig()
	if err != nil || cfg.NodeID != "node_1" || !cfg.Edge {
		t.Fatalf("config %+v %v", cfg, err)
	}
	if len(r.svc.installed) != 1 || r.svc.installed[0].StateDir != r.dir || r.svc.installed[0].Binary != "/usr/local/bin/loomctl" ||
		!r.svc.did("start") {
		t.Fatalf("service: %+v %v", r.svc.installed, r.svc.calls)
	}
}

func TestJoinWithKeyFlagsWhenThereIsNoLogin(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	if err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--key", secretKey); err != nil {
		t.Fatalf("join: %v\n%s", err, r.out)
	}
	if js.minted.Load() != 1 {
		t.Fatal("no token was minted")
	}
	// The environment works too, as in loomctl.
	r2 := newCmdRig(t)
	r2.opts.Getenv = func(k string) string {
		return map[string]string{"LOOMPLANE_URL": js.URL, "LOOMPLANE_API_KEY": secretKey}[k]
	}
	if err := r2.run("node", "join", "--state-dir", r2.dir); err != nil {
		t.Fatalf("join from the environment: %v", err)
	}
}

func TestJoinWithTokenNeedsNoKey(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	if err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "lpj_live_given", "--name", "web-1"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if js.minted.Load() != 0 || js.join.Load().Token != "lpj_live_given" || js.join.Load().Name != "web-1" {
		t.Fatalf("minted %d, join %+v", js.minted.Load(), js.join.Load())
	}
	// A token fixes its own scope, so --cloud cannot ride along.
	r2 := newCmdRig(t)
	err := r2.run("node", "join", "--state-dir", r2.dir, "--url", js.URL, "--token", "x", "--cloud", "c1")
	if err == nil || !strings.Contains(err.Error(), "--cloud") {
		t.Fatalf("--cloud with --token: %v", err)
	}
}

// The command the dashboard renders runs join with the token in
// LOOMPLANE_JOIN_TOKEN and the scope as --allow, with no terminal. The join
// spends that token and asks for exactly those endpoints.
func TestJoinFromTheRenderedCommand(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	r.opts.Getenv = func(k string) string { return map[string]string{"LOOMPLANE_JOIN_TOKEN": "lpj_live_env"}[k] }
	if err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--allow", "probe,docker"); err != nil {
		t.Fatalf("join: %v\n%s", err, r.out)
	}
	j := js.join.Load()
	if js.minted.Load() != 0 || j.Token != "lpj_live_env" || !slices.Equal(j.Endpoints, []nodeproto.Endpoint{"probe", "docker"}) {
		t.Fatalf("minted %d, join %+v", js.minted.Load(), j)
	}
	// --token wins over the environment, and an environment token can't
	// carry --edge either.
	r2 := newCmdRig(t)
	r2.opts.Getenv = r.opts.Getenv
	if err := r2.run("node", "join", "--state-dir", r2.dir, "--url", js.URL, "--token", "lpj_live_flag"); err != nil || js.join.Load().Token != "lpj_live_flag" {
		t.Fatalf("flag over env: %v %+v", err, js.join.Load())
	}
	r3 := newCmdRig(t)
	r3.opts.Getenv = r.opts.Getenv
	if err := r3.run("node", "join", "--state-dir", r3.dir, "--url", js.URL, "--edge"); err == nil || !strings.Contains(err.Error(), "--edge") {
		t.Fatalf("--edge with an environment token: %v", err)
	}
}

func TestJoinArgumentErrors(t *testing.T) {
	js := newJoinServer(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no token and no key":  {[]string{"--url", js.URL}, "--token"},
		"no url":               {[]string{"--token", "x"}, "--url"},
		"bad allow":            {[]string{"--url", js.URL, "--token", "x", "--allow", "public"}, "public"},
		"unknown allow":        {[]string{"--url", js.URL, "--token", "x", "--allow", "nonsense"}, "nonsense"},
		"bad label":            {[]string{"--url", js.URL, "--token", "x", "--label", "nokey"}, "--label"},
		"bad builder":          {[]string{"--url", js.URL, "--token", "x", "--builder", "shared"}, "dedicated"},
		"builder with token":   {[]string{"--url", js.URL, "--token", "x", "--builder", "dedicated"}, "--builder go in the join token"},
		"builder label, token": {[]string{"--url", js.URL, "--token", "x", "--label", "build_workers=dedicated"}, "--builder go in the join token"},
	} {
		r := newCmdRig(t)
		err := r.run(append([]string{"node", "join", "--state-dir", r.dir}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if js.join.Load() != nil || js.minted.Load() != 0 {
			t.Errorf("%s: reached the server", name)
		}
	}
}

func TestJoinOnLinuxNeedsRootUnlessForeground(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	r.opts.euid = func() int { return 1000 }
	err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "root") || !strings.Contains(err.Error(), "--foreground") {
		t.Fatalf("join as a user: %v", err)
	}
	if js.join.Load() != nil {
		t.Fatal("the token was spent before the root check")
	}

	var ran atomic.Bool
	r.opts.runNode = func(_ context.Context, st State, _ RuntimeOptions) error {
		if _, err := st.LoadConfig(); err != nil {
			t.Errorf("joined config is not there when run starts: %v", err)
		}
		ran.Store(true)
		return nil
	}
	if err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x", "--foreground"); err != nil {
		t.Fatalf("foreground join: %v", err)
	}
	if !ran.Load() || len(r.svc.installed) != 0 {
		t.Fatalf("ran=%v installed=%v", ran.Load(), r.svc.installed)
	}
}

func TestJoinOnMacSaysItIsForDevelopment(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	r.opts.goos, r.opts.euid = "darwin", func() int { return 501 }
	if err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.out.String(), "macOS nodes are for development") {
		t.Fatalf("output:\n%s", r.out)
	}
	if len(r.svc.installed) != 1 || !r.svc.did("start") {
		t.Fatalf("service %+v", r.svc.calls)
	}
}

func TestAllowRequiresRoot(t *testing.T) {
	r := newCmdRig(t)
	r.opts.euid = func() int { return 1000 }
	err := r.run("node", "allow", "docker", "--state-dir", r.dir)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("allow as a user: %v", err)
	}
	// macOS has no service user, so no root is needed there.
	r.opts.goos = "darwin"
	st := State{Dir: r.dir}
	if err := st.SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{}}); err != nil {
		t.Fatal(err)
	}
	if err := r.run("node", "allow", "docker", "--state-dir", r.dir); err != nil {
		t.Fatalf("allow on macOS: %v", err)
	}
}

func TestAllowValidatesEndpoints(t *testing.T) {
	r := newCmdRig(t)
	if err := (State{Dir: r.dir}).SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"public", "control", "nonsense"} {
		if err := r.run("node", "allow", "docker", bad, "--state-dir", r.dir); err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("allow %s: %v", bad, err)
		}
	}
	if err := r.run("node", "allow", "--state-dir", r.dir); err == nil {
		t.Error("allow with nothing to allow")
	}
	cfg, _ := State{Dir: r.dir}.LoadConfig()
	if len(cfg.Endpoints) != 0 || len(cfg.PendingAllowed) != 0 {
		t.Fatalf("a refused allow changed the config: %+v", cfg)
	}
}

func TestAllowWithoutServiceWritesConfigForNextStart(t *testing.T) {
	r := newCmdRig(t)
	var chowned []string
	r.opts.chown = func(p string, _, _ int) error { chowned = append(chowned, filepath.Base(p)); return nil }
	st := State{Dir: r.dir}
	if err := st.SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{"probe"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.run("node", "allow", "docker", "dial", "docker", "--state-dir", r.dir); err != nil {
		t.Fatalf("allow: %v\n%s", err, r.out)
	}
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := []nodeproto.Endpoint{"docker", "dial"}
	if !slices.Equal(cfg.Endpoints, []nodeproto.Endpoint{"probe", "docker", "dial"}) || !slices.Equal(cfg.PendingAllowed, want) {
		t.Fatalf("config %+v", cfg)
	}
	if !slices.Contains(chowned, "node.json") {
		t.Fatalf("node.json was left to root: chowned %v", chowned)
	}
	// The ceiling is written first, by whoever ran allow, and is never
	// handed to the service user.
	c, err := st.ReadCeiling(func(uint32) bool { return true })
	if err != nil || c.NodeID != "n" || !slices.Equal(c.Endpoints, want) {
		t.Fatalf("allow.json %+v %v", c, err)
	}
	if fi, _ := os.Stat(filepath.Join(r.dir, "allow.json")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("allow.json mode %v", fi.Mode())
	}
	if slices.Contains(chowned, "allow.json") {
		t.Fatal("allow.json was handed to the service user")
	}
	if !strings.Contains(r.out.String(), "next time") {
		t.Fatalf("output should say it applies at the next start:\n%s", r.out)
	}
}

// allow docker puts the service user in the docker group, and restarts a
// running service only when membership changed.
func TestAllowDockerGrantsTheGroupAndRestartsOnlyOnChange(t *testing.T) {
	for name, tc := range map[string]struct {
		changed, running bool
		wantRestart      bool
		wantOut          string
	}{
		"joined the group":     {true, true, true, "restarted"},
		"already in the group": {false, true, false, ""},
		"changed, not running": {true, false, false, "next time it starts"},
	} {
		r := newCmdRig(t)
		r.svc.grantChanged = tc.changed
		r.svc.running = tc.running
		if tc.running {
			serveControlStub(t, r.dir, `{"ok":true}`)
		}
		st := State{Dir: r.dir}
		if err := st.SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{"probe"}}); err != nil {
			t.Fatal(err)
		}
		if err := r.run("node", "allow", "docker", "--state-dir", r.dir); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, r.out)
		}
		if r.svc.grants != 1 {
			t.Errorf("%s: GrantDocker ran %d times", name, r.svc.grants)
		}
		if got := r.svc.did("stop") && r.svc.did("start"); got != tc.wantRestart {
			t.Errorf("%s: restarted = %v, want %v (%v)", name, got, tc.wantRestart, r.svc.calls)
		}
		if !strings.Contains(r.out.String(), tc.wantOut) {
			t.Errorf("%s: output lacks %q:\n%s", name, tc.wantOut, r.out)
		}
	}
	// Allowing something else never touches the group.
	r := newCmdRig(t)
	if err := (State{Dir: r.dir}).SaveConfig(Config{URL: "http://x", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	if err := r.run("node", "allow", "dial", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	if r.svc.grants != 0 {
		t.Fatal("allow dial touched the docker group")
	}
}

// allow adds to the ceiling a join wrote, and a ceiling for another node
// counts for nothing.
func TestAllowWidensTheCeiling(t *testing.T) {
	r := newCmdRig(t)
	st := State{Dir: r.dir}
	if err := st.SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{"probe"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCeiling(Ceiling{NodeID: "n", Endpoints: []nodeproto.Endpoint{"probe"}}, -1); err != nil {
		t.Fatal(err)
	}
	if err := r.run("node", "allow", "docker", "--state-dir", r.dir); err != nil {
		t.Fatalf("allow: %v\n%s", err, r.out)
	}
	c, err := st.ReadCeiling(func(uint32) bool { return true })
	if err != nil || !slices.Equal(c.Endpoints, []nodeproto.Endpoint{"probe", "docker"}) {
		t.Fatalf("allow.json %+v %v", c, err)
	}
	if err = st.SaveCeiling(Ceiling{NodeID: "old", Endpoints: []nodeproto.Endpoint{"kube", "dial"}}, -1); err != nil {
		t.Fatal(err)
	}
	if err = r.run("node", "allow", "probe", "--state-dir", r.dir); err != nil {
		t.Fatalf("allow: %v\n%s", err, r.out)
	}
	if c, err = st.ReadCeiling(func(uint32) bool { return true }); err != nil || c.NodeID != "n" || !slices.Equal(c.Endpoints, []nodeproto.Endpoint{"probe"}) {
		t.Fatalf("another node's allow.json was kept: %+v %v", c, err)
	}
}

func TestAllowRefusesWhenServiceRunsButSocketIsGone(t *testing.T) {
	r := newCmdRig(t)
	r.svc.running = true
	if err := (State{Dir: r.dir}).SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{}}); err != nil {
		t.Fatal(err)
	}
	err := r.run("node", "allow", "docker", "--state-dir", r.dir)
	if err == nil || !strings.Contains(err.Error(), "control socket") {
		t.Fatalf("allow: %v", err)
	}
	cfg, _ := State{Dir: r.dir}.LoadConfig()
	if len(cfg.Endpoints) != 0 {
		t.Fatalf("wrote node.json under a running service: %+v", cfg)
	}
}

// liveNode joins a node to a real hub and runs it, with a state dir short
// enough for the control socket.
func liveNode(t *testing.T) (*rig, *Runtime, Config, chan error) {
	t.Helper()
	r := newRig(t)
	r.st = State{Dir: filepath.Join(shortDir(t), "state")}
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	done := start(t, rt)
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	eventually(t, "the first narrow", func() bool { return rt.narrows.Load() >= 1 })
	return r, rt, cfg, done
}

func TestAllowThroughTheRunningService(t *testing.T) {
	r, rt, cfg, _ := liveNode(t)
	cr := newCmdRig(t)
	cr.dir = r.st.Dir
	if err := cr.run("node", "allow", "docker", "--state-dir", cr.dir); err != nil {
		t.Fatalf("allow: %v\n%s", err, cr.out)
	}
	if !rt.live.Load().Allows(nodeproto.EndpointDocker) {
		t.Fatal("the running service did not widen")
	}
	eventually(t, "Loomplane to learn of docker", func() bool {
		return slices.Contains(r.env.Store.Node(cfg.NodeID).Endpoints, nodeproto.EndpointDocker)
	})
	if err := cr.run("node", "allow", "public", "--state-dir", cr.dir); err == nil {
		t.Fatal("public was allowed")
	}
}

func TestLeaveThroughTheRunningService(t *testing.T) {
	r, _, _, done := liveNode(t)
	cr := newCmdRig(t)
	cr.dir = r.st.Dir
	cr.svc.running = true
	if err := cr.run("node", "leave", "--state-dir", cr.dir); err != nil {
		t.Fatalf("leave: %v\n%s", err, cr.out)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not stop")
	}
	if strings.Contains(cr.out.String(), "revoke it in the dashboard") {
		t.Fatalf("the node revoked itself, so the hint is wrong:\n%s", cr.out)
	}
	if !cr.svc.did("stop") || !cr.svc.did("remove") {
		t.Fatalf("service calls %v", cr.svc.calls)
	}
	if _, err := os.Stat(filepath.Join(cr.dir, "node.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("node.json stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cr.dir, "identity.key")); err != nil {
		t.Fatalf("the key must stay: %v", err)
	}
}

func TestLeaveWithoutServicePointsAtTheDashboard(t *testing.T) {
	r := newCmdRig(t)
	st := State{Dir: r.dir}
	if _, err := st.Identity(); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{}}); err != nil {
		t.Fatal(err)
	}
	if err := r.run("node", "leave", "--purge", "--state-dir", r.dir); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if !strings.Contains(r.out.String(), "revoke it in the dashboard") {
		t.Fatalf("output:\n%s", r.out)
	}
	if !r.svc.did("remove") {
		t.Fatalf("service calls %v", r.svc.calls)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "node.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("node.json stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "identity.key")); err != nil {
		t.Fatalf("the key must stay: %v", err)
	}
}

func TestRunExitsWith77WhenRevoked(t *testing.T) {
	r := newRig(t)
	r.st = State{Dir: filepath.Join(shortDir(t), "state")}
	r.join(t, nodeproto.EndpointProbe)
	r.env.Store.BeforeClaim = func(id string) { r.env.Store.Revoke(id) }
	cr := newCmdRig(t)
	err := cr.run("node", "run", "--state-dir", r.st.Dir)
	if err == nil || cli.GetExitCode(err) != ExitRevoked || !errors.Is(err, ErrRevoked) || ExitCode(err) != ExitRevoked {
		t.Fatalf("run returned %v (exit %d)", err, cli.GetExitCode(err))
	}
	if cli.GetExitCode(errors.New("x")) != 1 {
		t.Fatal("other errors must stay 1")
	}
}

func TestStatus(t *testing.T) {
	r := newCmdRig(t)
	if err := r.run("node", "status", "--state-dir", r.dir); err != nil || !strings.Contains(r.out.String(), "not joined") {
		t.Fatalf("unjoined: %v\n%s", err, r.out)
	}
	st := State{Dir: r.dir}
	priv, err := st.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConfig(Config{URL: "https://lp.example.com", NodeID: "node_7", OrgID: "org_1",
		Endpoints: []nodeproto.Endpoint{"probe", "docker", "kube"}, PendingAllowed: []nodeproto.Endpoint{"docker"}, IngressMode: "none"}); err != nil {
		t.Fatal(err)
	}
	// kube is in node.json only, so it is not served.
	if err := st.SaveCeiling(Ceiling{NodeID: "node_7", Endpoints: []nodeproto.Endpoint{"probe", "docker", "dial"}}, -1); err != nil {
		t.Fatal(err)
	}
	r.svc.running = true
	r.out.Reset()
	if err := r.run("node", "status", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	fp := nodeproto.Fingerprint(priv.Public().(ed25519.PublicKey))
	for _, want := range []string{"node_7", "https://lp.example.com", fp, "probe, docker", "running"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, r.out)
		}
	}
	r.out.Reset()
	if err := r.run("node", "status", "--json", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Joined      bool     `json:"joined"`
		NodeID      string   `json:"node_id"`
		Fingerprint string   `json:"fingerprint"`
		Endpoints   []string `json:"endpoints"`
		Running     bool     `json:"service_running"`
	}
	if err := json.Unmarshal(r.out.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if !got.Joined || got.NodeID != "node_7" || got.Fingerprint != fp || !got.Running || len(got.Endpoints) != 2 {
		t.Fatalf("json %+v", got)
	}
}

func apiServer(t *testing.T, revoked *atomic.Value) *httptest.Server {
	t.Helper()
	seen := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []client.Node{
			{ID: "node_a", Name: "web-1", Status: "online", Endpoints: []nodeproto.Endpoint{"probe", "docker"}, Version: "v1.2.3", LastSeenAt: &seen,
				Inventory: nodeproto.Inventory{Arch: "arm64"}},
			{ID: "node_b", Name: "gpu-1", Status: "offline"},
			{ID: "node_c", Name: "web-1x", Status: "online"},
			// An earlier web-1, revoked, which frees its name.
			{ID: "node_d", Name: "web-1", Status: "revoked"},
		}})
	})
	mux.HandleFunc("POST /api/v1/nodes/{id}/revoke", func(w http.ResponseWriter, req *http.Request) {
		revoked.Store(req.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestListAndRevoke(t *testing.T) {
	var revoked atomic.Value
	srv := apiServer(t, &revoked)
	r := newCmdRig(t)
	r.opts.Login = func() (string, string, error) { return srv.URL, secretKey, nil }
	if err := r.run("node", "list"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "STATUS", "ENDPOINTS", "ARCH", "VERSION", "LAST SEEN", "web-1", "probe,docker", "arm64", "v1.2.3", "2026-11-02 09:30 UTC", "gpu-1", "never"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, r.out)
		}
	}
	r.out.Reset()
	if err := r.run("node", "list", "--json"); err != nil {
		t.Fatal(err)
	}
	var nodes []client.Node
	if err := json.Unmarshal(r.out.Bytes(), &nodes); err != nil || len(nodes) != 4 {
		t.Fatalf("json: %v %s", err, r.out)
	}
	if err := r.run("node", "revoke", "web-1"); err != nil || revoked.Load() != "node_a" {
		t.Fatalf("revoke by name: %v %v", err, revoked.Load())
	}
	if err := r.run("node", "revoke", "node_b"); err != nil || revoked.Load() != "node_b" {
		t.Fatalf("revoke by id: %v %v", err, revoked.Load())
	}
	if err := r.run("node", "revoke", "missing"); err == nil {
		t.Fatal("revoked a node that is not there")
	}
	// Without a login, list and revoke say what they need.
	r2 := newCmdRig(t)
	if err := r2.run("node", "list"); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Fatalf("list without a login: %v", err)
	}
}

// serveControlStub answers every control request with resp.
func serveControlStub(t *testing.T, dir, resp string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "unix", filepath.Join(dir, controlSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(c).ReadBytes('\n')
			_, _ = c.Write([]byte(resp + "\n"))
			_ = c.Close()
		}
	}()
}

func TestLeaveKeepsEverythingWhenTheNodeCannotConfirm(t *testing.T) {
	r := newCmdRig(t)
	st := State{Dir: r.dir}
	if err := st.SaveConfig(Config{URL: "http://x", NodeID: "n", Endpoints: []nodeproto.Endpoint{}}); err != nil {
		t.Fatal(err)
	}
	serveControlStub(t, r.dir, `{"ok":false,"error":"Loomplane did not confirm"}`)
	err := r.run("node", "leave", "--state-dir", r.dir)
	if err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("leave: %v", err)
	}
	if strings.Contains(r.out.String(), "revoked this node") {
		t.Fatalf("claimed a revoke:\n%s", r.out)
	}
	if _, err := st.LoadConfig(); err != nil {
		t.Fatalf("node.json must stay: %v", err)
	}
	if r.svc.did("remove") || r.svc.did("stop") {
		t.Fatalf("the service was touched: %v", r.svc.calls)
	}
}

// fakeDocker serves just enough of the Docker API for Detect to find it.
func fakeDocker(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(shortDir(t), "d.sock")
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OK")) })
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"Version": "27.1.0"})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestJoinRefusesAStateDirWithForeignFiles(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "passwd"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var chowned int
	r.opts.chown = func(string, int, int) error { chowned++; return nil }
	err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "passwd") {
		t.Fatalf("join: %v", err)
	}
	if js.join.Load() != nil || len(r.svc.installed) != 0 || chowned != 0 {
		t.Fatal("the join went ahead")
	}
	if fi, _ := os.Stat(r.dir); fi.Mode().Perm() != 0o755 {
		t.Fatalf("the dir's mode changed to %v", fi.Mode())
	}
	// A dir that holds only a node's own files is fine to join again in.
	r2 := newCmdRig(t)
	if err := os.MkdirAll(filepath.Join(r2.dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkJoinDir(r2.dir); err != nil {
		t.Fatalf("own files: %v", err)
	}
}

func TestStateDirMustBeAbsolute(t *testing.T) {
	r := newCmdRig(t)
	for _, args := range [][]string{{"status"}, {"run"}, {"leave"}, {"allow", "docker"}, {"join", "--url", "http://x", "--token", "x"}} {
		err := r.run(append(append([]string{"node"}, args...), "--state-dir", "relative/dir")...)
		if err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Errorf("node %s: %v", args[0], err)
		}
	}
	if _, err := os.Stat("relative"); err == nil {
		t.Fatal("a relative dir was created")
	}
}

func TestStatusChangesNothing(t *testing.T) {
	r := newCmdRig(t)
	dir := filepath.Join(shortDir(t), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.run("node", "status", "--state-dir", dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Fatalf("status changed the dir to %v", fi.Mode())
	}
	missing := filepath.Join(shortDir(t), "never")
	if err := r.run("node", "status", "--state-dir", missing); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("status created the state dir")
	}
	// Nor do allow and leave touch a dir that is not a node's.
	for _, args := range [][]string{{"allow", "docker"}, {"leave"}} {
		_ = r.run(append([]string{"node"}, append(args, "--state-dir", dir)...)...)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Fatalf("allow or leave changed the dir to %v", fi.Mode())
	}
}

func TestJoinAsksWhichEndpointsOnATerminal(t *testing.T) {
	sock := fakeDocker(t)
	for name, tc := range map[string]struct {
		answer string
		want   []nodeproto.Endpoint
	}{"yes": {"y\n", []nodeproto.Endpoint{"probe", "docker"}}, "no": {"n\n", nil}, "eof": {"", nil}} {
		js := newJoinServer(t)
		r := newCmdRig(t)
		r.opts.isTerminal = func() bool { return true }
		r.opts.in = strings.NewReader(tc.answer)
		r.opts.detect.DockerSockets = []string{sock}
		r.opts.Login = func() (string, string, error) { return js.URL, secretKey, nil }
		if err := r.run("node", "join", "--state-dir", r.dir); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, r.out)
		}
		if got := js.scope.Load().Endpoints; !slices.Equal(got, tc.want) {
			t.Errorf("%s: the minted scope has %v, want %v", name, got, tc.want)
		}
		if got := js.join.Load().Endpoints; len(got) != len(tc.want) {
			t.Errorf("%s: the join sent %v", name, got)
		}
		if !strings.Contains(r.out.String(), "Allow these?") || !strings.Contains(r.out.String(), sock) {
			t.Errorf("%s: the question lacks what was found:\n%s", name, r.out)
		}
	}
}

func TestJoinWithoutTerminalOrAllowAllowsNothing(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	r.opts.in = strings.NewReader("y\n")
	r.opts.detect.DockerSockets = []string{fakeDocker(t)}
	r.opts.Login = func() (string, string, error) { return js.URL, secretKey, nil }
	if err := r.run("node", "join", "--state-dir", r.dir); err != nil {
		t.Fatal(err)
	}
	if len(js.scope.Load().Endpoints) != 0 || len(js.join.Load().Endpoints) != 0 {
		t.Fatalf("scope %v, join %v", js.scope.Load().Endpoints, js.join.Load().Endpoints)
	}
	if !strings.Contains(r.out.String(), "node allow docker") || strings.Contains(r.out.String(), "Allow these?") {
		t.Fatalf("output:\n%s", r.out)
	}
	// --allow wins over the question, terminal or not.
	r2 := newCmdRig(t)
	r2.opts.isTerminal = func() bool { return true }
	r2.opts.Login = func() (string, string, error) { return js.URL, secretKey, nil }
	if err := r2.run("node", "join", "--state-dir", r2.dir, "--allow", "probe"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r2.out.String(), "Allow these?") {
		t.Fatalf("asked although --allow was given:\n%s", r2.out)
	}
}

func TestJoinRecordsDetectedDockerEvenWhenNotAllowed(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	sock := fakeDocker(t)
	r.opts.detect.DockerSockets = []string{sock}
	if err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x", "--allow", "probe"); err != nil {
		t.Fatal(err)
	}
	cfg, err := State{Dir: r.dir}.LoadConfig()
	if err != nil || cfg.DockerSocket != sock {
		t.Fatalf("config %+v %v", cfg, err)
	}
	// Detected but not allowed: the socket path is kept, the group is not asked for.
	if len(r.svc.installed) != 1 || r.svc.installed[0].Docker {
		t.Fatalf("the unit asks for the docker group although docker was not allowed: %+v", r.svc.installed)
	}
	// Allowed and detected: the group is asked for.
	r3 := newCmdRig(t)
	r3.opts.detect.DockerSockets = []string{sock}
	if err := r3.run("node", "join", "--state-dir", r3.dir, "--url", js.URL, "--token", "x", "--allow", "probe,docker"); err != nil {
		t.Fatal(err)
	}
	if len(r3.svc.installed) != 1 || !r3.svc.installed[0].Docker {
		t.Fatalf("the unit does not ask for the docker group: %+v", r3.svc.installed)
	}
	// And a machine without Docker does not.
	r2 := newCmdRig(t)
	if err := r2.run("node", "join", "--state-dir", r2.dir, "--url", js.URL, "--token", "x", "--allow", "probe"); err != nil {
		t.Fatal(err)
	}
	if r2.svc.installed[0].Docker {
		t.Fatal("docker group for a machine without docker")
	}
}

func TestJoinNeedsSystemdAndUseraddBeforeSpendingTheToken(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	r.opts.serviceReady = func() error { return errors.New("this machine does not run systemd") }
	err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x")
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "--foreground") || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("join: %v", err)
	}
	if js.join.Load() != nil {
		t.Fatal("the token was spent")
	}
}

func TestEdgeCannotRideOnAToken(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x", "--edge")
	if err == nil || !strings.Contains(err.Error(), "--edge") {
		t.Fatalf("join: %v", err)
	}
}

func TestMacJoinAndLeaveRefuseRoot(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	r.opts.goos = "darwin" // euid stays 0
	for _, args := range [][]string{{"join", "--url", js.URL, "--token", "x"}, {"leave"}} {
		err := r.run(append([]string{"node"}, append(args, "--state-dir", r.dir)...)...)
		if err == nil || !strings.Contains(err.Error(), "your own user") {
			t.Errorf("node %s as root on macOS: %v", args[0], err)
		}
	}
	if js.join.Load() != nil {
		t.Fatal("the token was spent")
	}
}

func TestRunTouchesNothingInADirThatHasNotJoined(t *testing.T) {
	r := newCmdRig(t)
	dir := filepath.Join(shortDir(t), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := r.run("node", "run", "--state-dir", dir)
	if err == nil || !strings.Contains(err.Error(), "hasn't joined") {
		t.Fatalf("run: %v", err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Fatalf("run changed the dir to %v", fi.Mode())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("run created %v", entries)
	}
	missing := filepath.Join(shortDir(t), "never")
	if err := r.run("node", "run", "--state-dir", missing); err == nil {
		t.Fatal("run without a state dir succeeded")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("run created the state dir")
	}
}

func TestJoinChecksUnitValuesBeforeSpendingTheToken(t *testing.T) {
	js := newJoinServer(t)
	r := newCmdRig(t)
	bad := filepath.Join(shortDir(t), "my state")
	err := r.run("node", "join", "--state-dir", bad, "--url", js.URL, "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "systemd unit") {
		t.Fatalf("odd dir: %v", err)
	}
	r.opts.binary = func() (string, error) { return "/usr/local/bin/lo omctl", nil }
	err = r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "binary name") {
		t.Fatalf("odd binary: %v", err)
	}
	if js.join.Load() != nil {
		t.Fatal("the token was spent")
	}
}

func TestJoinRefusesAPlantedSymlinkOrWrongKind(t *testing.T) {
	js := newJoinServer(t)
	for name, plant := range map[string]func(dir string) error{
		"bin symlink":    func(d string) error { return os.Symlink("/etc", filepath.Join(d, "bin")) },
		"bin file":       func(d string) error { return os.WriteFile(filepath.Join(d, "bin"), nil, 0o600) },
		"key symlink":    func(d string) error { return os.Symlink("/etc/passwd", filepath.Join(d, "identity.key")) },
		"config as dir":  func(d string) error { return os.Mkdir(filepath.Join(d, "node.json"), 0o700) },
		"socket as file": func(d string) error { return os.WriteFile(filepath.Join(d, controlSocket), nil, 0o600) },
	} {
		r := newCmdRig(t)
		if err := os.MkdirAll(r.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := plant(r.dir); err != nil {
			t.Fatal(err)
		}
		err := r.run("node", "join", "--state-dir", r.dir, "--url", js.URL, "--token", "x")
		if err == nil || !strings.Contains(err.Error(), "not the kind of file") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if js.join.Load() != nil {
		t.Fatal("the token was spent")
	}
}
