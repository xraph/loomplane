package node

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// releaseServer serves one release the way GitHub does: under
// /<tag>/<file>.
type releaseServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	asked []string
}

func newReleaseServer(t *testing.T, files map[string][]byte) *releaseServer {
	t.Helper()
	rs := &releaseServer{files: files}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		rs.asked = append(rs.asked, r.URL.Path)
		body, ok := rs.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// verifyCall is what node update asked the signature check to verify.
type verifyCall struct {
	version, artifact, sum string
	checksums, bundle      string
}

type updateRig struct {
	*cmdRig
	rel     *releaseServer
	archive []byte
	calls   []verifyCall
	verr    error
}

const oldBinary = "OLD loomctl"

// newUpdateRig is a Linux root machine with a node whose service runs a
// copy of loomctl in its state dir, and a release v1.9.0 to update to.
func newUpdateRig(t *testing.T) *updateRig {
	t.Helper()
	u := &updateRig{cmdRig: newCmdRig(t)}
	u.archive = tarGz(t, map[string]string{"LICENSE": "license", "README.md": "readme", "loomctl": "NEW loomctl"})
	u.rel = newReleaseServer(t, map[string][]byte{
		"/v1.9.0/loomctl_1.9.0_linux_amd64.tar.gz":     u.archive,
		"/v1.9.0/binaries-checksums.txt":               []byte("the checksums"),
		"/v1.9.0/binaries-checksums.txt.sigstore.json": []byte("the bundle"),
		"/v1.9.0/loomctl_1.9.0_darwin_arm64.tar.gz":    u.archive,
	})
	u.opts.releaseBase = u.rel.URL
	u.opts.goarch = "amd64"
	u.opts.verifyRelease = func(_ context.Context, version string, checksums, bundle []byte, artifact, sum string) error {
		u.calls = append(u.calls, verifyCall{version, artifact, sum, string(checksums), string(bundle)})
		return u.verr
	}
	if err := os.MkdirAll(filepath.Join(u.dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.binPath(), []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	return u
}

func (u *updateRig) binPath() string { return filepath.Join(u.dir, "bin", "loomctl") }

func (u *updateRig) binary(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(u.binPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateReplacesTheServiceBinaryAndRestarts(t *testing.T) {
	u := newUpdateRig(t)
	u.svc.running = true
	if err := u.run("node", "update", "--state-dir", u.dir, "--release", "v1.9.0"); err != nil {
		t.Fatal(err)
	}
	if got := u.binary(t); got != "NEW loomctl" {
		t.Fatalf("the service binary is %q", got)
	}
	fi, err := os.Stat(u.binPath())
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v, %v", fi, err)
	}
	entries, _ := os.ReadDir(filepath.Join(u.dir, "bin"))
	if len(entries) != 1 {
		t.Fatalf("bin holds %d files, want only loomctl: %v", len(entries), entries)
	}
	if !slices.Equal(u.svc.calls, []string{"stop", "start"}) {
		t.Fatalf("service calls %v", u.svc.calls)
	}
	want := verifyCall{"v1.9.0", "loomctl_1.9.0_linux_amd64.tar.gz", archiveSum(string(u.archive)), "the checksums", "the bundle"}
	if len(u.calls) != 1 || u.calls[0] != want {
		t.Fatalf("verified %+v, want %+v", u.calls, want)
	}
	if !strings.Contains(u.out.String(), "v1.9.0") {
		t.Fatalf("output:\n%s", u.out)
	}
}

func TestUpdateAcceptsAVersionWithoutTheV(t *testing.T) {
	u := newUpdateRig(t)
	if err := u.run("node", "update", "--state-dir", u.dir, "--release", "1.9.0"); err != nil {
		t.Fatal(err)
	}
	if u.binary(t) != "NEW loomctl" || u.calls[0].version != "v1.9.0" {
		t.Fatalf("binary %q, verified %+v", u.binary(t), u.calls)
	}
}

func TestUpdateDoesNotStartAServiceThatWasStopped(t *testing.T) {
	u := newUpdateRig(t)
	if err := u.run("node", "update", "--state-dir", u.dir, "--release", "v1.9.0"); err != nil {
		t.Fatal(err)
	}
	if u.binary(t) != "NEW loomctl" || len(u.svc.calls) != 0 {
		t.Fatalf("binary %q, service calls %v", u.binary(t), u.svc.calls)
	}
}

func TestUpdateChangesNothingWhenTheSignatureFails(t *testing.T) {
	u := newUpdateRig(t)
	u.svc.running = true
	u.verr = errors.New("not signed by the release workflow")
	err := u.run("node", "update", "--state-dir", u.dir, "--release", "v1.9.0")
	if err == nil || !strings.Contains(err.Error(), "not signed by the release workflow") {
		t.Fatalf("err %v", err)
	}
	if u.binary(t) != oldBinary || len(u.svc.calls) != 0 {
		t.Fatalf("binary %q, service calls %v", u.binary(t), u.svc.calls)
	}
}

func TestUpdateUsesTheNodesArchitecture(t *testing.T) {
	u := newUpdateRig(t)
	u.opts.goos, u.opts.euid, u.opts.goarch = "darwin", func() int { return 501 }, "arm64"
	if err := u.run("node", "update", "--state-dir", u.dir, "--release", "v1.9.0"); err != nil {
		t.Fatal(err)
	}
	if len(u.calls) != 1 || u.calls[0].artifact != "loomctl_1.9.0_darwin_arm64.tar.gz" {
		t.Fatalf("verified %+v", u.calls)
	}
}

func TestUpdateRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*updateRig)
		args  []string
		want  string
	}{
		"no version":     {func(*updateRig) {}, nil, "--release"},
		"bad version":    {func(*updateRig) {}, []string{"--release", "v1;id"}, "not a release"},
		"not root":       {func(u *updateRig) { u.opts.euid = func() int { return 1000 } }, []string{"--release", "v1.9.0"}, "needs root"},
		"mac root":       {func(u *updateRig) { u.opts.goos = "darwin" }, []string{"--release", "v1.9.0"}, "without sudo"},
		"no release":     {func(*updateRig) {}, []string{"--release", "v7.7.7"}, "404"},
		"no service bin": {func(u *updateRig) { _ = os.Remove(u.binPath()) }, []string{"--release", "v1.9.0"}, "loomctl"},
		"no binary in the archive": {func(u *updateRig) {
			u.rel.files["/v1.9.0/loomctl_1.9.0_linux_amd64.tar.gz"] = tarGz(t, map[string]string{"README.md": "x"})
		}, []string{"--release", "v1.9.0"}, "does not hold loomctl"},
		"a directory as the binary": {func(u *updateRig) {
			_ = os.Remove(u.binPath())
			_ = os.Mkdir(u.binPath(), 0o700)
		}, []string{"--release", "v1.9.0"}, "loomctl"},
	} {
		u := newUpdateRig(t)
		tc.setup(u)
		err := u.run(append([]string{"node", "update", "--state-dir", u.dir}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if fi, serr := os.Lstat(u.binPath()); serr == nil && fi.Mode().IsRegular() && u.binary(t) != oldBinary {
			t.Errorf("%s: the binary was replaced", name)
		}
		if len(u.svc.calls) != 0 {
			t.Errorf("%s: service calls %v", name, u.svc.calls)
		}
	}
}

func TestUpdateKeepsTheOwnerOfTheServiceBinary(t *testing.T) {
	u := newUpdateRig(t)
	var chowned []string
	u.opts.chown = func(p string, _, _ int) error { chowned = append(chowned, filepath.Base(p)); return nil }
	if err := u.run("node", "update", "--state-dir", u.dir, "--release", "v1.9.0"); err != nil {
		t.Fatal(err)
	}
	// The new file is chowned before it is renamed, under its temp name.
	if len(chowned) != 1 || !strings.HasPrefix(chowned[0], ".loomctl.tmp-") {
		t.Fatalf("chown %v", chowned)
	}
}

func TestVerifyReleaseCommandChecksTheDownload(t *testing.T) {
	u := newUpdateRig(t)
	dir := t.TempDir()
	archive := filepath.Join(dir, "loomctl_1.9.0_linux_amd64.tar.gz")
	for p, body := range map[string][]byte{archive: u.archive, filepath.Join(dir, "sums"): []byte("sums"), filepath.Join(dir, "bundle"): []byte("bundle")} {
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"node", "verify-release", "--release", "v1.9.0", "--archive", archive, "--checksums", filepath.Join(dir, "sums"), "--bundle", filepath.Join(dir, "bundle")}
	if err := u.run(args...); err != nil {
		t.Fatal(err)
	}
	want := verifyCall{"v1.9.0", "loomctl_1.9.0_linux_amd64.tar.gz", archiveSum(string(u.archive)), "sums", "bundle"}
	if len(u.calls) != 1 || u.calls[0] != want {
		t.Fatalf("verified %+v, want %+v", u.calls, want)
	}
	u.verr = errors.New("bad signature")
	if err := u.run(args...); err == nil || !strings.Contains(err.Error(), "bad signature") {
		t.Fatalf("a failed check: %v", err)
	}
	if err := u.run("node", "verify-release", "--release", "v1.9.0", "--archive", filepath.Join(dir, "missing"), "--checksums", "x", "--bundle", "y"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}
}
