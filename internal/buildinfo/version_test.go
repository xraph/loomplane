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
	// CI may compile this binary from an empty cache alongside the race suite.
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancelBuild()
	binary := filepath.Join(t.TempDir(), "loomctl")
	version := "cli-build-boundary-test"
	command := exec.CommandContext(buildCtx, "go", "build", "-ldflags", "-X github.com/xraph/loomplane/internal/buildinfo.Version="+version, "-o", binary, "./cmd/loomctl")
	command.Dir = filepath.Join("..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		if buildCtx.Err() != nil {
			t.Fatalf("build CLI: %v (%v)\n%s", err, buildCtx.Err(), out)
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
