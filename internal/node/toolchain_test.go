package node

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

type bundleFile struct {
	name, body string
	link       bool
}

// bundle is a toolchain bundle: the files, and a manifest naming each with
// its sha256 unless lie names one to misstate.
func bundle(t *testing.T, lie string, extra ...bundleFile) []byte {
	t.Helper()
	files := map[string]string{"firecracker": "bin/firecracker", "jailer": "bin/jailer", "kernel": "vmlinux", "rootfs": "rootfs.ext4",
		"cni/ptp": "cni/ptp", "cni/host-local": "cni/host-local", "cni/tc-redirect-tap": "cni/tc-redirect-tap"}
	manifest := map[string]any{"arch": runtime.GOARCH}
	entries := map[string]any{}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, path := range files {
		body := "contents of " + name
		sum := sha256.Sum256([]byte(body))
		h := hex.EncodeToString(sum[:])
		if name == lie {
			h = strings.Repeat("0", 64)
		}
		entries[name] = map[string]string{"path": path, "sha256": h}
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	manifest["files"] = entries
	raw, _ := json.Marshal(manifest)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(raw)
	for _, f := range extra {
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if f.link {
			hdr = &tar.Header{Name: f.name, Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"}
		}
		_ = tw.WriteHeader(hdr)
		if !f.link {
			_, _ = tw.Write([]byte(f.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func serveBundle(t *testing.T, b []byte) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(b) }))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(b)
	return srv, hex.EncodeToString(sum[:])
}

func toolchainOpts(t *testing.T, srv *httptest.Server, sum string) ToolchainOptions {
	t.Helper()
	root := t.TempDir()
	return ToolchainOptions{URL: srv.URL + "/toolchain.tar.gz", SHA256: sum, Root: filepath.Join(root, "opt"), Config: filepath.Join(root, "etc", "firecracker.json"),
		StateDir: "/var/lib/loomplane-runner/firecracker", RuntimeDir: "/srv/loomplane-runner", HTTP: srv.Client(),
		Inventory: nodeproto.Inventory{CPUs: 8, MemoryMiB: 16384, DiskGiB: 160}}
}

func TestInstallToolchainWritesAHostConfig(t *testing.T) {
	srv, sum := serveBundle(t, bundle(t, ""))
	o := toolchainOpts(t, srv, sum)
	path, err := InstallToolchain(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg fc.HostConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.HostKey) != 64 || cfg.Firecracker.SHA256 == "" || !strings.HasPrefix(cfg.Firecracker.Path, o.Root) || cfg.CNIHashes["ptp"] == "" {
		t.Fatalf("config %+v", cfg)
	}
	if cfg.Limits.VMs != 4 || cfg.Limits.VCPUs != 8 || cfg.Limits.MemoryMiB != 15360 {
		t.Fatalf("limits %+v: 8 CPUs and 16 GiB fit four 2-vCPU, 2 GiB build VMs", cfg.Limits)
	}
	if len(cfg.DeniedPorts) != 3 || cfg.EgressCIDRs[0] != "0.0.0.0/0" {
		t.Fatalf("network %+v", cfg)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", fi.Mode().Perm())
	}
	// A second install keeps the config, and its host key.
	if _, err = InstallToolchain(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(raw, again) {
		t.Fatal("a second install replaced the host config")
	}
}

// TestShareAcrossWorkspaces checks a global runner caps each workspace at
// half of itself, passes the platform's check for a per-workspace cap, keeps
// a cap its owner set, and never caps under one build VM.
func TestShareAcrossWorkspaces(t *testing.T) {
	srv, sum := serveBundle(t, bundle(t, ""))
	o := toolchainOpts(t, srv, sum)
	path, err := InstallToolchain(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	read := func() fc.HostConfig {
		t.Helper()
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		var cfg fc.HostConfig
		if rerr = json.Unmarshal(raw, &cfg); rerr != nil {
			t.Fatal(rerr)
		}
		return cfg
	}
	before := read()
	if err = shareAcrossWorkspaces(path); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if cfg.Limits.PerOrg != (fc.OrgLimits{VMs: 2, VCPUs: 4, MemoryMiB: 7680}) {
		t.Fatalf("per-workspace %+v of %+v, want half", cfg.Limits.PerOrg, cfg.Limits)
	}
	if cfg.HostKey != before.HostKey || cfg.Limits.VMs != before.Limits.VMs {
		t.Fatalf("sharing changed more than the cap: %+v", cfg)
	}
	if err = fc.ValidatePlatform(cfg); err != nil {
		t.Fatal(err)
	}

	cfg.Limits.PerOrg = fc.OrgLimits{VMs: 1, VCPUs: 2, MemoryMiB: 2048}
	raw, _ := json.Marshal(cfg)
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = shareAcrossWorkspaces(path); err != nil {
		t.Fatal(err)
	}
	if got := read().Limits.PerOrg; got != cfg.Limits.PerOrg {
		t.Fatalf("an owner's cap became %+v", got)
	}

	cfg.Limits = fc.Limits{VMs: 1, VCPUs: 2, MemoryMiB: 2304, DiskMiB: 16384}
	raw, _ = json.Marshal(cfg)
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = shareAcrossWorkspaces(path); err != nil {
		t.Fatal(err)
	}
	if got := read().Limits.PerOrg; got != (fc.OrgLimits{VMs: 1, VCPUs: 2, MemoryMiB: 2304}) {
		t.Fatalf("a one-VM runner capped each workspace at %+v, want the whole VM", got)
	}
}

func TestInstallToolchainRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bundle func(t *testing.T) []byte
		sum    func(string) string
		want   string
	}{
		{"another bundle than the pool pins", func(t *testing.T) []byte { return bundle(t, "") }, func(string) string { return strings.Repeat("a", 64) }, "nothing was unpacked"},
		{"a file that doesn't match its manifest", func(t *testing.T) []byte { return bundle(t, "kernel") }, nil, "kernel doesn't match"},
		{"a path outside the bundle", func(t *testing.T) []byte { return bundle(t, "", bundleFile{name: "../escape", body: "x"}) }, nil, "outside itself"},
		{"a symlink", func(t *testing.T) []byte { return bundle(t, "", bundleFile{name: "shadow", link: true}) }, nil, "not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sum := serveBundle(t, tc.bundle(t))
			if tc.sum != nil {
				sum = tc.sum(sum)
			}
			o := toolchainOpts(t, srv, sum)
			_, err := InstallToolchain(t.Context(), o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if _, serr := os.Stat(o.Config); serr == nil {
				t.Fatal("a host config was written for a bundle that failed its checks")
			}
		})
	}
}

func TestInstallToolchainNeedsHTTPSAndASum(t *testing.T) {
	if _, err := InstallToolchain(t.Context(), ToolchainOptions{URL: "http://example.com/b.tgz", SHA256: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("an http bundle was accepted")
	}
	if _, err := InstallToolchain(t.Context(), ToolchainOptions{URL: "https://example.com/b.tgz", SHA256: "abc"}); err == nil {
		t.Fatal("a bundle without its sha256 was accepted")
	}
}
