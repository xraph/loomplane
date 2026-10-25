//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestNativeCloudSQLPITRSettingsSurvivePersistence(t *testing.T) {
	f := nativePointHistoryFixture(t)
	binding := f.source.Binding
	binding.ID, binding.Name = "cloudsql-binding", "cloudsql"
	binding.Provider, binding.Region = "cloud-sql", "us-east1"
	binding.ExternalID, binding.PhysicalID = "source-project:us-east1:cloudsql", "original-cloudsql-incarnation"
	binding.Config = json.RawMessage(`{"kind":"postgres","version":"17","provider":"cloud-sql"}`)
	if err := planning.NewPgStore(f.db).PutBinding(t.Context(), &binding); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{Resource: "cloudsql"})
	if err != nil || len(sources.Sources) != 1 {
		t.Fatal("missing Cloud SQL source", sources, err)
	}
	sourceID := sources.Sources[0].ID
	now := time.Now().UTC().Truncate(time.Second)
	witness := strings.Repeat("a", 64)
	window := recovery.NativePoint{ProviderID: binding.PhysicalID, Name: binding.Name, Kind: "pitr", Status: "available", Engine: "postgres", Version: "17", CreatedAt: now.Add(-30 * 24 * time.Hour), Encrypted: true, PITR: &recovery.NativePITRBounds{Protocol: 1, Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute), SettingsDigest: witness}}
	fetch := func(context.Context, *recovery.NativeSource) (*recovery.NativePoint, error) { return &window, nil }
	saved, err := f.store.RefreshNativePITR(t.Context(), "org", sourceID, fetch)
	if err != nil {
		t.Fatal(err)
	}
	reopened := recovery.NewPgStore(f.db)
	retained, err := reopened.GetNativePoint(t.Context(), "org", sourceID, saved.ID)
	if err != nil || retained.PITR == nil || retained.PITR.SettingsDigest != witness {
		t.Fatal("settings witness did not survive reopening the store", retained, err)
	}
	for _, invalid := range []string{"", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		window.PITR.SettingsDigest = invalid
		if _, err = reopened.RefreshNativePITR(t.Context(), "org", sourceID, fetch); !errors.Is(err, recovery.ErrNativePITRUnavailable) {
			t.Fatal("invalid settings observation replaced the retained point", err)
		}
	}
	retained, err = reopened.GetNativePoint(t.Context(), "org", sourceID, saved.ID)
	if err != nil || retained.PITR.SettingsDigest != witness || !retained.LastSeenAt.Equal(saved.LastSeenAt) {
		t.Fatal("failed refresh changed the retained settings or freshness", retained, err)
	}
	window.PITR.SettingsDigest = witness
	window.PITR.Latest = window.PITR.Latest.Add(time.Second)
	rolled, err := reopened.RefreshNativePITR(t.Context(), "org", sourceID, fetch)
	if err != nil || rolled.ID != saved.ID || rolled.PITR.SettingsDigest != witness || !rolled.PITR.Latest.Equal(window.PITR.Latest) {
		t.Fatal("rolling window changed the settings witness or point identity", rolled, err)
	}
	window.PITR.SettingsDigest = strings.Repeat("b", 64)
	changed, err := reopened.RefreshNativePITR(t.Context(), "org", sourceID, fetch)
	if err != nil || changed.ID != saved.ID || changed.PITR.SettingsDigest != window.PITR.SettingsDigest || saved.PITR.SettingsDigest != witness {
		t.Fatal("fresh settings failed to update the observation independently of the old selection", changed, err)
	}
}

func TestNativePITRSettingsPublishedContract(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	app := start(t, cfg)
	defer app.stop()
	c := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	var raw json.RawMessage
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
				Required   []string                  `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	bounds := spec.Components.Schemas["NativePITRBounds"]
	if bounds.Properties["settings_digest"]["type"] != "string" {
		t.Fatal("published bounds omit the Cloud SQL settings witness")
	}
	for _, required := range bounds.Required {
		if required == "settings_digest" {
			t.Fatal("published bounds require a Cloud SQL-only witness for existing RDS windows")
		}
	}
	if output := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); output != "" {
		if err := os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
