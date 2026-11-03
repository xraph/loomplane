package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shRig runs the rendered script against stub uname, id, curl, sudo and
// (optionally) cosign, with a local directory standing in for GitHub's
// releases and a temp dir for /usr/local/bin.
type shRig struct {
	t       *testing.T
	stubs   string
	serve   string
	binDir  string
	log     string
	out     string
	archive string
	sum     string
	env     []string
}

const stubLoomctl = "#!/bin/sh\necho \"$@\" > \"$OUT\"\necho \"${LOOMPLANE_JOIN_TOKEN:-}\" > \"$OUT.token\"\n"

func newShRig(t *testing.T, osName, arch, uid string) *shRig {
	t.Helper()
	d := t.TempDir()
	r := &shRig{t: t, stubs: filepath.Join(d, "stubs"), serve: filepath.Join(d, "serve"), binDir: filepath.Join(d, "bin"),
		log: filepath.Join(d, "log"), out: filepath.Join(d, "out")}
	for _, dir := range []string{r.stubs, r.serve, r.binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.stub("uname", "case \"$1\" in -s) echo \"$FAKE_OS\" ;; -m) echo \"$FAKE_ARCH\" ;; esac")
	r.stub("id", "echo \"$FAKE_UID\"")
	r.stub("sudo", "echo \"sudo $*\" >> \"$LOG\"\nexec \"$@\"")
	r.stub("curl", `echo "curl $*" >> "$LOG"
out=
prev=
for a in "$@"; do
	[ "$prev" = -o ] && out=$a
	prev=$a
done
cp "$SERVE/$(basename "$a")" "$out" || exit 22`)
	platform := osName + "_" + map[string]string{"x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[arch]
	r.archive = "loomctl_1.8.0_" + platform + ".tar.gz"
	body := r.tarGz(stubLoomctl)
	r.sum = sha(body)
	r.put(r.archive, body)
	r.put("binaries-checksums.txt", []byte(r.sum+"  "+r.archive+"\n"+strings.Repeat("0", 64)+"  other.tar.gz\n"))
	r.put("binaries-checksums.txt.sigstore.json", []byte("{}"))
	r.env = []string{"PATH=" + r.stubs + ":/usr/bin:/bin", "HOME=" + d, "FAKE_OS=" + map[string]string{"linux": "Linux", "darwin": "Darwin"}[osName],
		"FAKE_ARCH=" + arch, "FAKE_UID=" + uid, "SERVE=" + r.serve, "LOG=" + r.log, "OUT=" + r.out, "LOOMPLANE_BIN_DIR=" + r.binDir}
	return r
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (r *shRig) stub(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.stubs, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

func (r *shRig) put(name string, body []byte) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.serve, name), body, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *shRig) tarGz(loomctl string) []byte {
	r.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "loomctl", Mode: 0o755, Size: int64(len(loomctl)), Typeflag: tar.TypeReg}); err != nil {
		r.t.Fatal(err)
	}
	_, _ = tw.Write([]byte(loomctl))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// cosign installs a stub that accepts only identities containing accept
// ("tags", "heads", or "" to accept none).
func (r *shRig) cosign(accept string) {
	r.stub("cosign", `echo "cosign $*" >> "$LOG"
case "$*" in *"--certificate-identity-regexp"*) exit 2 ;; esac
case "$*" in *"--certificate-oidc-issuer https://token.actions.githubusercontent.com"*) ;; *) exit 3 ;; esac
[ -n "`+accept+`" ] && case "$*" in *"/release.yml@refs/`+accept+`"*) exit 0 ;; esac
echo "cosign: no match" >&2
exit 1`)
}

func (r *shRig) logged() string {
	b, _ := os.ReadFile(r.log)
	return string(b)
}

func (r *shRig) installed() bool {
	_, err := os.Stat(filepath.Join(r.binDir, "loomctl"))
	return err == nil
}

// run renders with sums (or none) and runs the script.
func (r *shRig) run(withSums bool, extraEnv []string, args ...string) (string, error) {
	r.t.Helper()
	p := params()
	if withSums {
		platform := strings.TrimSuffix(strings.TrimPrefix(r.archive, "loomctl_1.8.0_"), ".tar.gz")
		p.Sums = map[string]string{platform: r.sum}
	}
	script, err := Render(p)
	if err != nil {
		r.t.Fatal(err)
	}
	path := filepath.Join(r.t.TempDir(), "node.sh")
	if err = os.WriteFile(path, []byte(script), 0o600); err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.CommandContext(r.t.Context(), "sh", append([]string{path}, args...)...)
	cmd.Env = append(append([]string{}, r.env...), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// tokenSeen is the LOOMPLANE_JOIN_TOKEN loomctl ran with.
func (r *shRig) tokenSeen() string {
	b, _ := os.ReadFile(r.out + ".token")
	return strings.TrimSpace(string(b))
}

func (r *shRig) joined() string {
	b, _ := os.ReadFile(r.out)
	return strings.TrimSpace(string(b))
}

// The command the dashboard renders: the token in the environment, kept
// through sudo, and never on a command line.
func TestScriptKeepsTheTokenOutOfArgv(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "1000")
	// The sudo stub drops what it isn't told to keep, as env_reset does.
	r.stub("sudo", `echo "sudo $*" >> "$LOG"
keep=
case "$1" in --preserve-env=LOOMPLANE_JOIN_TOKEN) keep=$LOOMPLANE_JOIN_TOKEN; shift ;; esac
unset LOOMPLANE_JOIN_TOKEN
[ -n "$keep" ] && export LOOMPLANE_JOIN_TOKEN="$keep"
exec "$@"`)
	out, err := r.run(true, []string{"LOOMPLANE_JOIN_TOKEN=lpj_secret"}, "--allow", "probe,docker")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := r.joined(); got != "node join --url https://loom.example.com --allow probe,docker" {
		t.Fatalf("loomctl ran with %q\n%s", got, out)
	}
	if got := r.tokenSeen(); got != "lpj_secret" {
		t.Fatalf("loomctl saw the token %q", got)
	}
	if log := r.logged(); strings.Contains(log, "lpj_secret") || !strings.Contains(log, "sudo --preserve-env=LOOMPLANE_JOIN_TOKEN ") {
		t.Fatalf("the token reached a command line, or sudo did not keep it:\n%s", log)
	}
}

func TestScriptLinuxNonRootJoinsThroughSudo(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "1000")
	out, err := r.run(true, nil, "--token", "lpj_x")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := r.joined(); got != "node join --url https://loom.example.com --token lpj_x" {
		t.Fatalf("loomctl ran with %q\n%s", got, out)
	}
	if log := r.logged(); !strings.Contains(log, "sudo "+filepath.Join(r.binDir, "loomctl")+" node join") {
		t.Fatalf("the join did not go through sudo:\n%s", log)
	}
	if !strings.Contains(out, "matches the sha256 that https://loom.example.com verified") {
		t.Fatalf("output does not say what was checked:\n%s", out)
	}
	if !strings.Contains(r.logged(), "--proto =https --tlsv1.2") {
		t.Fatalf("curl is not held to https and TLS 1.2:\n%s", r.logged())
	}
}

func TestScriptLinuxRootJoinsDirectly(t *testing.T) {
	r := newShRig(t, "linux", "aarch64", "0")
	if out, err := r.run(true, nil, "--token", "t"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.joined() == "" || strings.Contains(r.logged(), "sudo") {
		t.Fatalf("joined %q, log:\n%s", r.joined(), r.logged())
	}
}

func TestScriptMacJoinsAsTheUser(t *testing.T) {
	r := newShRig(t, "darwin", "arm64", "501")
	if out, err := r.run(true, nil, "--token", "t"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.joined() == "" || strings.Contains(r.logged(), "sudo") {
		t.Fatalf("a macOS node must join as the user. joined %q, log:\n%s", r.joined(), r.logged())
	}
}

func TestScriptInstallsThroughSudoWhenTheBinDirIsNotWritable(t *testing.T) {
	r := newShRig(t, "darwin", "arm64", "501")
	if err := os.Chmod(r.binDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.binDir, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	// The sudo stub runs the command as us, so give it a way to succeed.
	r.stub("sudo", "echo \"sudo $*\" >> \"$LOG\"\nchmod 755 \"$BIN_DIR_FIX\"\nexec \"$@\"")
	out, err := r.run(true, []string{"BIN_DIR_FIX=" + r.binDir}, "--token", "t")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := r.logged()
	if !strings.Contains(log, "sudo install -m 0755") || strings.Contains(log, "node join") {
		t.Fatalf("install should use sudo and the join should not:\n%s", log)
	}
}

func TestScriptRefusesWithoutCosignAndWithoutSums(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "0")
	out, err := r.run(false, nil, "--token", "t")
	if err == nil || !strings.Contains(out, "cosign is not installed") || !strings.Contains(out, "LOOMPLANE_SKIP_SIGNATURE=1") {
		t.Fatalf("err %v\n%s", err, out)
	}
	if r.installed() || r.joined() != "" {
		t.Fatal("it installed an archive it could not check")
	}
}

func TestScriptSkipsTheCheckOnlyWhenToldAndSaysSo(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "0")
	out, err := r.run(false, []string{"LOOMPLANE_SKIP_SIGNATURE=1"}, "--token", "t")
	if err != nil || !strings.Contains(out, "this install is NOT verified") || r.joined() == "" {
		t.Fatalf("err %v joined %q\n%s", err, r.joined(), out)
	}
}

func TestScriptRefusesAnArchiveThatDiffersFromTheRenderedSum(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "0")
	r.put(r.archive, r.tarGz(stubLoomctl+"# tampered\n"))
	out, err := r.run(true, nil, "--token", "t")
	if err == nil || !strings.Contains(out, "does not match the sha256") || r.installed() {
		t.Fatalf("err %v installed %v\n%s", err, r.installed(), out)
	}
}

// Sums from the instance do not weaken cosign: with it installed, the
// signature decides, and the rendered sum is never consulted.
func TestScriptUsesCosignWhenItIsThere(t *testing.T) {
	for accept, want := range map[string][]string{
		"tags":  {"@refs/tags/v1.8.0"},
		"heads": {"@refs/tags/v1.8.0", "@refs/heads/main"},
	} {
		r := newShRig(t, "linux", "x86_64", "0")
		r.cosign(accept)
		out, err := r.run(true, nil, "--token", "t")
		if err != nil || r.joined() == "" || !strings.Contains(out, "signed by the Loomplane release workflow") {
			t.Fatalf("%s: err %v\n%s", accept, err, out)
		}
		log := r.logged()
		last := -1
		for _, w := range want {
			i := strings.Index(log, w)
			if i < 0 || i < last {
				t.Fatalf("%s: identities not tried in order %v:\n%s", accept, want, log)
			}
			last = i
		}
		if strings.Contains(log, "regexp") {
			t.Fatal("cosign was given a regexp")
		}
	}
}

func TestScriptRefusesWhenCosignRejectsEveryIdentity(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "0")
	r.cosign("")
	out, err := r.run(true, nil, "--token", "t")
	if err == nil || !strings.Contains(out, "cosign 2.4 or newer") || !strings.Contains(out, "cosign: no match") || r.installed() {
		t.Fatalf("err %v installed %v\n%s", err, r.installed(), out)
	}
}

func TestScriptChecksTheArchiveAgainstWhatCosignVerified(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "0")
	r.cosign("tags")
	r.put(r.archive, r.tarGz(stubLoomctl+"# tampered\n"))
	out, err := r.run(true, nil, "--token", "t")
	if err == nil || !strings.Contains(out, "does not match the checksum cosign verified") || r.installed() {
		t.Fatalf("err %v\n%s", err, out)
	}
}

func TestScriptVerifiesBeforeItUnpacks(t *testing.T) {
	r := newShRig(t, "linux", "x86_64", "0")
	r.put(r.archive, []byte("not even a tarball"))
	out, err := r.run(true, nil, "--token", "t")
	if err == nil || strings.Contains(out, "does not hold loomctl") || !strings.Contains(out, "does not match") {
		t.Fatalf("a bad archive must fail the check, not the unpack. err %v\n%s", err, out)
	}
}
