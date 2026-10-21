//go:build integration

package integration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/config"
	federation "github.com/xraph/loomplane/internal/federation"
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

func TestPlatformFederationIdentityRequiresAdmin(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	cfg.Federation = config.FederationConfig{Enabled: true, Identity: config.FederationIdentityConfig{Enabled: true, OrganizationID: "org", TrustDomain: "loomplane.test", AdminSocket: "unix:///nonexistent/loomplane-server.sock"}}
	app, err := platform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := app.Stop(cleanup); err != nil {
			t.Log("failed startup cleanup:", err)
		}
	})
	if err := app.Start(ctx); err == nil || !strings.Contains(err.Error(), "federation identity administration") {
		t.Fatal("startup bypassed administration or failed before identity wiring", err)
	}
	if _, injectErr := forge.InjectType[*federation.InstallationService](app.Container()); injectErr != nil {
		t.Fatal("installation service not wired", injectErr)
	}
	if _, injectErr := forge.InjectType[*federation.EnrollmentService](app.Container()); injectErr != nil {
		t.Fatal("enrollment service not wired", injectErr)
	}
}

func TestPlatformFederationLaunchWiring(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	cfg.Federation = config.FederationConfig{Enabled: true,
		Identity: config.FederationIdentityConfig{Enabled: true, OrganizationID: "org", TrustDomain: "loomplane.test", AdminSocket: "unix:///nonexistent/loomplane-server.sock"},
		Runtime:  config.FederationRuntimeConfig{Enabled: true, OrganizationID: "org", TrustDomain: "loomplane.test", ListenAddress: "127.0.0.1:9443", WorkloadAPI: "unix:///nonexistent/agent.sock", SigningKeysFile: filepath.Join(t.TempDir(), "keys.json")},
		Launch:   config.FederationLaunchConfig{Enabled: true, ControlEndpoint: "https://control.example:9443", AgentImage: "registry.example/agent@sha256:" + strings.Repeat("a", 64), TrustGeneration: 1},
	}
	app, err := platform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := app.Stop(cleanup); err != nil {
			t.Log("failed startup cleanup:", err)
		}
	})
	if err := app.Start(ctx); err == nil || !strings.Contains(err.Error(), "federation identity administration") {
		t.Fatal("startup bypassed configured identity", err)
	}
	if _, err := forge.InjectType[*federation.LaunchService](app.Container()); err != nil {
		t.Fatal("launch service not wired", err)
	}
	if _, err := forge.InjectType[*federation.LaunchRetirementService](app.Container()); err != nil {
		t.Fatal("launch retirement service not wired", err)
	}
	if _, err := forge.InjectType[*federation.PreparationService](app.Container()); err != nil {
		t.Fatal("preparation service not wired", err)
	}
}
