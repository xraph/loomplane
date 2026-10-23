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

func TestCLIVersionUsesSharedBuildValue(t *testing.T) {
	// Building the CLI compiles its whole dependency tree from cold, outside
	// the test cache, while every other test package builds beside it. On a
	// small CI runner that can take most of the package's time, so the build
	// gets everything up to the test binary's own deadline, less a margin to
	// report the failure.
	budget := 10 * time.Minute
	if deadline, ok := t.Deadline(); ok {
		budget = time.Until(deadline) - 30*time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "loomctl")
	version := "cli-build-boundary-test"
	command := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X github.com/xraph/loomplane/internal/buildinfo.Version="+version, "-o", binary, "./cmd/loomctl")
	command.Dir = filepath.Join("..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), version) {
		t.Fatalf("CLI version: %v\n%s", err, out)
	}
}
