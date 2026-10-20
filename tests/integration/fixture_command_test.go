//go:build integration

package integration

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func fixtureCommand(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	output, err := command.Output()
	if err != nil {
		t.Fatalf("fixture command %s: %v\nstdout:\n%s\nstderr:\n%s", args[0], err, output, diagnostic.String())
	}
	return strings.TrimSpace(string(output))
}

func TestFixtureCommandKeepsDiagnosticsOutOfResults(t *testing.T) {
	got := fixtureCommand(t, "sh", "-c", "printf 'restored-through-cli\\n'; printf 'client warning\\n' >&2")
	if got != "restored-through-cli" {
		t.Fatalf("diagnostic text contaminated command results: %q", got)
	}
}
