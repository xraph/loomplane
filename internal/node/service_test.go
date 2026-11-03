package node

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRunner records the commands a service manager runs and answers them
// from a script, so no test touches systemctl, useradd or launchctl.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	// reply returns the error for a command line; nil means success.
	reply func(line string) error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.mu.Unlock()
	if f.reply != nil {
		return nil, f.reply(line)
	}
	return nil, nil
}

func (f *fakeRunner) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from its golden file.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestSystemdUnitRendered(t *testing.T) {
	got, err := renderSystemdUnit(Unit{Binary: "/usr/local/bin/loomctl", StateDir: "/var/lib/loomplane-node"}, "loomplane-node")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "loomplane-node.service.golden", got)
}

func TestSystemdUnitRefusesSpacesInStateDir(t *testing.T) {
	if _, err := renderSystemdUnit(Unit{Binary: "/x/loomctl", StateDir: "/var/lib/my node"}, "loomplane-node"); err == nil {
		t.Fatal("a state dir with a space was accepted")
	}
}

func TestLaunchdPlistRendered(t *testing.T) {
	got, err := renderLaunchdPlist(Unit{Binary: "/usr/local/bin/loomctl", StateDir: "/Users/a & b/Library/Application Support/loomplane-node"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "com.loomplane.node.plist.golden", got)
}

type systemdRig struct {
	m       *systemdManager
	run     *fakeRunner
	unitDir string
	state   string
	src     string
	chowned map[string][2]int
	created bool
}

func newSystemdRig(t *testing.T) *systemdRig {
	t.Helper()
	dir := t.TempDir()
	r := &systemdRig{run: &fakeRunner{}, unitDir: filepath.Join(dir, "etc"), state: filepath.Join(dir, "state"), src: filepath.Join(dir, "loomctl"), chowned: map[string][2]int{}}
	if err := os.WriteFile(r.src, []byte("#!binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.run.reply = func(line string) error {
		if strings.HasPrefix(line, "useradd ") {
			r.created = true
		}
		return nil
	}
	r.m = &systemdManager{run: r.run, unitDir: r.unitDir, user: "loomplane-node",
		lookup: func(name string) (int, int, error) {
			if !r.created {
				return 0, 0, user.UnknownUserError(name)
			}
			return 990, 991, nil
		},
		chown: func(path string, uid, gid int) error { r.chowned[path] = [2]int{uid, gid}; return nil }}
	return r
}

func TestSystemdInstallCreatesUserCopiesBinaryAndWritesUnit(t *testing.T) {
	r := newSystemdRig(t)
	if err := os.MkdirAll(r.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.state, "node.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.state, "keep-out"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(r.state, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Install(t.Context(), Unit{Binary: r.src, StateDir: r.state, Docker: true}); err != nil {
		t.Fatal(err)
	}
	for p := range r.chowned {
		if b := filepath.Base(p); b == "keep-out" || b == "other" {
			t.Errorf("chowned %s, which is not a node file", p)
		}
	}
	if len(r.chowned) > 5 {
		t.Errorf("chowned %d paths: %v", len(r.chowned), r.chowned)
	}
	want := []string{
		"useradd --system --no-create-home --home-dir " + r.state + " --shell /usr/sbin/nologin loomplane-node",
		"getent group docker",
		"usermod -aG docker loomplane-node",
		"systemctl daemon-reload",
	}
	if got := r.run.got(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	bin := filepath.Join(r.state, "bin", "loomctl")
	if b, err := os.ReadFile(bin); err != nil || string(b) != "#!binary" {
		t.Fatalf("copied binary: %q %v", b, err)
	}
	if fi, _ := os.Stat(bin); fi.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode %v", fi.Mode())
	}
	for _, p := range []string{r.state, filepath.Join(r.state, "bin"), bin, filepath.Join(r.state, "node.json")} {
		if r.chowned[p] != [2]int{990, 991} {
			t.Errorf("%s was chowned to %v", p, r.chowned[p])
		}
	}
	if fi, _ := os.Stat(r.state); fi.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode %v", fi.Mode())
	}
	unit, err := os.ReadFile(filepath.Join(r.unitDir, "loomplane-node.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"ExecStart=" + bin + " node run --state-dir " + r.state,
		"Restart=always", "NoNewPrivileges=yes", "ProtectSystem=strict", "ReadWritePaths=" + r.state, "User=loomplane-node",
	} {
		if !strings.Contains(string(unit), line+"\n") {
			t.Errorf("unit lacks %q:\n%s", line, unit)
		}
	}
}

func TestSystemdInstallSkipsWhatIsThere(t *testing.T) {
	r := newSystemdRig(t)
	r.created = true // the user exists
	if err := os.MkdirAll(r.state, 0o700); err != nil {
		t.Fatal(err)
	}
	r.run.reply = func(line string) error {
		if line == "getent group docker" {
			return &runError{Code: 2} // no docker group on this machine
		}
		return nil
	}
	if err := r.m.Install(t.Context(), Unit{Binary: r.src, StateDir: r.state, Docker: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.run.got() {
		if strings.HasPrefix(c, "useradd") || strings.HasPrefix(c, "usermod") {
			t.Fatalf("ran %q", c)
		}
	}
}

func TestSystemdLifecycle(t *testing.T) {
	r := newSystemdRig(t)
	r.created = true
	if err := os.MkdirAll(r.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Install(t.Context(), Unit{Binary: r.src, StateDir: r.state}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := r.m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.m.Running(ctx); !ok || err != nil {
		t.Fatalf("running: %v %v", ok, err)
	}
	r.run.reply = func(line string) error {
		if strings.HasPrefix(line, "systemctl is-active") {
			return &runError{Code: 3}
		}
		return nil
	}
	if ok, err := r.m.Running(ctx); ok || err != nil {
		t.Fatalf("an inactive unit: %v %v", ok, err)
	}
	r.run.reply = func(string) error { return errors.New("systemctl: not found") }
	if _, err := r.m.Running(ctx); err == nil {
		t.Fatal("a missing systemctl was read as 'not running'")
	}
	r.run.reply = nil
	if err := r.m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.unitDir, "loomplane-node.service")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unit file stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.state, "bin", "loomctl")); err != nil {
		t.Fatalf("remove must keep the state dir: %v", err)
	}
	got := strings.Join(r.run.got(), "\n")
	for _, want := range []string{"systemctl enable --now loomplane-node", "systemctl is-active --quiet loomplane-node",
		"systemctl stop loomplane-node", "systemctl disable --now loomplane-node"} {
		if !strings.Contains(got, want) {
			t.Errorf("never ran %q:\n%s", want, got)
		}
	}
}

func TestLaunchdLifecycle(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "loomctl")
	if err := os.WriteFile(src, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	run := &fakeRunner{}
	m := &launchdManager{run: run, plistDir: filepath.Join(dir, "LaunchAgents"), uid: 501}
	ctx := t.Context()
	if err := m.Install(ctx, Unit{Binary: src, StateDir: state}); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(dir, "LaunchAgents", "com.loomplane.node.plist")
	b, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), filepath.Join(state, "bin", "loomctl")) {
		t.Fatalf("the plist does not run the copied binary:\n%s", b)
	}
	if got, err := os.ReadFile(filepath.Join(state, "bin", "loomctl")); err != nil || string(got) != "bin" {
		t.Fatalf("copied binary: %q %v", got, err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.Running(ctx); !ok || err != nil {
		t.Fatalf("running: %v %v", ok, err)
	}
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the plist stayed: %v", err)
	}
	got := strings.Join(run.got(), "\n")
	for _, want := range []string{"launchctl bootstrap gui/501 " + plist, "launchctl print gui/501/com.loomplane.node", "launchctl bootout gui/501/com.loomplane.node"} {
		if !strings.Contains(got, want) {
			t.Errorf("never ran %q:\n%s", want, got)
		}
	}
}

func TestUnitValuesAreChecked(t *testing.T) {
	good := Unit{Binary: "/usr/local/bin/loomctl", StateDir: "/var/lib/loomplane-node"}
	for name, u := range map[string]Unit{
		"relative dir":    {Binary: good.Binary, StateDir: "var/lib/node"},
		"space":           {Binary: good.Binary, StateDir: "/var/lib/my node"},
		"newline":         {Binary: good.Binary, StateDir: "/var/lib/n\nExecStart=/bin/sh"},
		"percent":         {Binary: good.Binary, StateDir: "/var/lib/%n"},
		"odd binary name": {Binary: "/usr/local/bin/lo omctl", StateDir: good.StateDir},
	} {
		if _, err := renderSystemdUnit(u, "loomplane-node"); err == nil {
			t.Errorf("systemd accepted %s", name)
		}
	}
	if _, err := renderLaunchdPlist(Unit{Binary: good.Binary, StateDir: "relative"}); err == nil {
		t.Error("launchd accepted a relative dir")
	}
	if _, err := renderLaunchdPlist(Unit{Binary: "/x/lo omctl", StateDir: "/Users/a/Library/Application Support/loomplane-node"}); err == nil {
		t.Error("launchd accepted an odd binary name")
	}
}
