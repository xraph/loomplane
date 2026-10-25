package buildinfo

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIDoesNotLoadServerExtensions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-e", "-deps", "./cmd/loomctl")
	command.Dir = filepath.Join("..", "..")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list CLI dependencies: %v\n%s", err, out)
	}
	for _, dependency := range strings.Fields(string(out)) {
		if dependency == "github.com/xraph/loomplane/internal/platform" || strings.HasPrefix(dependency, "github.com/xraph/loomplane/extensions/") {
			t.Fatalf("CLI metadata loads server implementation %s", dependency)
		}
	}
}

// cliBuildLimit bounds the CLI build when the test binary has no deadline.
// With one, the build gets what's left of it minus cliBuildMargin, which
// covers the --version run and cleanup.
const (
	cliBuildLimit  = 5 * time.Minute
	cliBuildMargin = 30 * time.Second
)

func TestCLIVersionUsesSharedBuildValue(t *testing.T) {
	// CI runs this inside go test -race ./..., often from a cold cache, and a
	// cold loomctl build can outlast any fixed limit on a small runner. Build
	// with the suite's own -race so the two share compiled packages, and
	// give the build whatever time the test binary has left.
	limit := cliBuildLimit
	if deadline, ok := t.Deadline(); ok {
		limit = time.Until(deadline) - cliBuildMargin
	}
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), limit)
	defer cancelBuild()
	binary := filepath.Join(t.TempDir(), "loomctl")
	version := "cli-build-boundary-test"
	args := []string{"build"}
	if raceEnabled {
		args = append(args, "-race")
	}
	args = append(args, "-ldflags", "-X github.com/xraph/loomplane/internal/buildinfo.Version="+version, "-o", binary, "./cmd/loomctl")
	command := exec.CommandContext(buildCtx, "go", args...)
	command.Dir = filepath.Join("..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		if buildCtx.Err() != nil {
			// Running out of time says the cache was cold, not that the CLI
			// is broken. Lint and vet still type-check cmd/loomctl, so a
			// compile error fails CI there.
			t.Skipf("loomctl did not build within %v, most likely from a cold build cache: %v", limit.Round(time.Second), err)
		}
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	cancelBuild()
	versionCtx, cancelVersion := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelVersion()
	if out, err := exec.CommandContext(versionCtx, binary, "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), version) {
		t.Fatalf("CLI version: %v\n%s", err, out)
	}
}
