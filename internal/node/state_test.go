package node

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func TestDefaultStateDir(t *testing.T) {
	if got := DefaultStateDir("darwin", "/Users/a"); got != "/Users/a/Library/Application Support/loomplane-node" {
		t.Fatalf("darwin: %q", got)
	}
	if got := DefaultStateDir("linux", "/home/a"); got != "/var/lib/loomplane-node" {
		t.Fatalf("linux: %q", got)
	}
}

func TestIdentityCreated0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s := State{Dir: dir}
	k1, err := s.Identity()
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: %v %v", di, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "identity.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode: %v %v", fi, err)
	}
	k2, err := s.Identity()
	if err != nil || !k1.Equal(k2) {
		t.Fatalf("second read differs: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}

func TestIdentityRefusesWorldReadable(t *testing.T) {
	dir := t.TempDir()
	s := State{Dir: dir}
	if _, err := s.Identity(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "identity.key")
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		_, err := s.Identity()
		if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("mode %o: want error naming path and chmod fix, got %v", mode, err)
		}
	}
}

func TestIdentityRejectsBadLength(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.key"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (State{Dir: dir}).Identity(); err == nil {
		t.Fatal("expected error")
	}
}

func TestConfigRoundTrip0600(t *testing.T) {
	s := State{Dir: filepath.Join(t.TempDir(), "s")}
	if _, err := s.LoadConfig(); err == nil {
		t.Fatal("missing config should error")
	}
	in := Config{URL: "https://x", NodeID: "n1", OrgID: "o1", IdentityGeneration: 3,
		Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}, IngressMode: nodeproto.IngressRelay, DockerSocket: "/s", Edge: true}
	if err := s.SaveConfig(in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "node.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	out, err := s.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if out.NodeID != "n1" || out.IngressMode != "relay" || len(out.Endpoints) != 1 || !out.Edge || out.IdentityGeneration != 3 {
		t.Fatalf("got %+v", out)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}

func TestLoadConfigDefaultsIngressNone(t *testing.T) {
	s := State{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, "node.json"), []byte(`{"node_id":"n"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := s.LoadConfig()
	if err != nil || c.IngressMode != "none" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestStateDirTightenedAndChecked(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (State{Dir: dir}).Identity(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("not tightened: %v", fi.Mode())
	}
}

func TestStateDirOwnedBySomeoneElseRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	s := State{Dir: t.TempDir(), euid: func() int { return os.Geteuid() + 1 }}
	for name, f := range map[string]func() error{
		"identity": func() error { _, err := s.Identity(); return err },
		"load":     func() error { _, err := s.LoadConfig(); return err },
		"save":     func() error { return s.SaveConfig(Config{}) },
	} {
		if err := f(); err == nil || !strings.Contains(err.Error(), "owned by") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestStateDirSymlinkRefused(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (State{Dir: link}).Identity(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("%v", err)
	}
}

func TestIdentitySymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	if _, err := (State{Dir: other}).Identity(); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "identity.key"), filepath.Join(dir, "identity.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := (State{Dir: dir}).Identity(); err == nil {
		t.Fatal("symlinked identity.key must be refused")
	}
}

func TestIdentityConcurrentFirstRun(t *testing.T) {
	s := State{Dir: filepath.Join(t.TempDir(), "s")}
	keys := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			k, err := s.Identity()
			if err != nil {
				keys <- "err: " + err.Error()
				return
			}
			keys <- string(k)
		}()
	}
	first := <-keys
	for i := 1; i < 8; i++ {
		if got := <-keys; got != first || strings.HasPrefix(got, "err:") {
			t.Fatalf("racing first runs disagree: %.40q vs %.40q", got, first)
		}
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestSupported(t *testing.T) {
	if err := supportedFor("linux"); err != nil {
		t.Fatal(err)
	}
	if err := supportedFor("darwin"); err != nil {
		t.Fatal(err)
	}
	err := supportedFor("windows")
	if err == nil || err.Error() != "loomplane node runs on Linux and macOS; this machine runs windows" {
		t.Fatalf("%v", err)
	}
}

func TestWriteExclusiveFallsBackWhenLinkIsUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.key")
	unsupported := func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EOPNOTSUPP} }
	if err := writeExclusiveWith(path, []byte("secret"), unsupported); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "secret" {
		t.Fatalf("read %q, %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	// Still exclusive: a second writer loses.
	if err := writeExclusiveWith(path, []byte("other"), unsupported); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second write: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "secret" {
		t.Fatalf("overwritten with %q", got)
	}
}

func TestWriteExclusiveKeepsOtherLinkErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x")
	boom := errors.New("boom")
	if err := writeExclusiveWith(path, []byte("a"), func(string, string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a failed link left a file")
	}
}
