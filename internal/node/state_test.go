package node

import (
	"os"
	"path/filepath"
	"strings"
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
		Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}, IngressMode: "relay", DockerSocket: "/s", Edge: true}
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
