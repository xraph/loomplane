//go:build integration

package integration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

func TestPlatformFederationRuntimeRequiresSigningKeys(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	cfg.Federation = config.FederationConfig{Enabled: true, Runtime: config.FederationRuntimeConfig{Enabled: true, ListenAddress: "127.0.0.1:9443", OrganizationID: "org", TrustDomain: "loomplane.test", WorkloadAPI: "unix:///run/spire/agent.sock", SigningKeysFile: filepath.Join(t.TempDir(), "missing-keys.json")}}
	app, err := platform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := app.Stop(cleanup); err != nil {
			t.Log("failed startup cleanup:", err)
		}
	})
	// This reaches the real extension after migrations and shared bus startup.
	// Missing signing authority must stop startup before any runtime socket opens.
	if err := app.Start(ctx); !errors.Is(err, access.ErrSigningKeys) {
		t.Fatal("runtime startup bypassed signing keys or failed before runtime wiring", err)
	}
}
