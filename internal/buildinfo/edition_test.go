package buildinfo

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cloudOnly are the packages only a -tags cloud binary may link: the
// billing extension and the libraries it brings in.
var cloudOnly = []string{
	"github.com/xraph/loomplane/extensions/billing",
	"github.com/xraph/ledger/",
	"github.com/xraph/authsome/plugins/subscription",
	"github.com/stripe/",
}

// serverDeps lists what the two server commands link, built with tags.
func serverDeps(t *testing.T, tags string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	args := []string{"list", "-e", "-deps"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./cmd/loomplane", "./cmd/loomplane-worker")
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = filepath.Join("..", "..")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list server dependencies: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

// ledgerCore are the ledger packages authsome's core already imports, so
// every build links them. Anything else under ledger (its stores, its
// billing providers) is cloud code.
var ledgerCore = map[string]bool{
	"github.com/xraph/ledger":                    true,
	"github.com/xraph/ledger/id":                 true,
	"github.com/xraph/ledger/types":              true,
	"github.com/xraph/ledger/coupon":             true,
	"github.com/xraph/ledger/entitlement":        true,
	"github.com/xraph/ledger/feature":            true,
	"github.com/xraph/ledger/internal/telemetry": true,
	"github.com/xraph/ledger/plan":               true,
	"github.com/xraph/ledger/invoice":            true,
	"github.com/xraph/ledger/meter":              true,
	"github.com/xraph/ledger/subscription":       true,
	"github.com/xraph/ledger/provider":           true,
	"github.com/xraph/ledger/plugin":             true,
	"github.com/xraph/ledger/store":              true,
}

func isCloudOnly(dependency string) bool {
	if ledgerCore[dependency] {
		return false
	}
	for _, prefix := range cloudOnly {
		if dependency == prefix || strings.HasPrefix(dependency, prefix) {
			return true
		}
	}
	return false
}

func TestPublicBuildHasNoCloudCode(t *testing.T) {
	for _, dependency := range serverDeps(t, "") {
		if isCloudOnly(dependency) {
			t.Fatalf("public build links cloud code %s", dependency)
		}
	}
}

func TestCloudBuildHasCloudCode(t *testing.T) {
	for _, dependency := range serverDeps(t, "cloud") {
		if dependency == "github.com/xraph/loomplane/extensions/billing" {
			return
		}
	}
	t.Fatal("cloud build does not link extensions/billing")
}
