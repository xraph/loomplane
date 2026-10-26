//go:build docker

package dbgateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EDockerBuild builds the generated source with its own Dockerfile, as
// Cloud Build or CodeBuild would. It doesn't run the image.
func TestE2EDockerBuild(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker isn't on PATH")
	}
	if out, ierr := exec.CommandContext(t.Context(), docker, "info").CombinedOutput(); ierr != nil {
		t.Skipf("docker isn't running: %v\n%s", ierr, out)
	}
	data, id, err := Source()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, b := range unpack(t, data) {
		p := filepath.Join(dir, name)
		if err = os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tag := "loomplane-dbgateway-e2e:" + id
	cmd := exec.CommandContext(t.Context(), docker, "build", "--progress=plain", "-f", Dockerfile, "-t", tag, ".")
	cmd.Dir = filepath.Join(dir, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(docker, "rmi", "-f", tag).Run() }) //nolint:noctx // the tag built above
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	t.Logf("docker build %s, last lines:\n%s", tag, strings.Join(lines[max(0, len(lines)-12):], "\n"))
}
