package planning

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestCloudLocationLegacyTarget(t *testing.T) {
	p := targetPlacement(&cloudprofiles.Assignment{}, cloudprofiles.Target{Name: "eu-independent"}, ConnectionOption{}, "", "", "")
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "location_id") || strings.Contains(string(raw), "managed_resource_id") {
		t.Fatalf("legacy wire shape changed: %s", raw)
	}
	p = targetPlacement(&cloudprofiles.Assignment{}, cloudprofiles.Target{LocationID: "location_1", ManagedResourceID: "resource_1"}, ConnectionOption{}, "", "", "")
	if p.LocationID != "location_1" || p.ManagedResourceID != "resource_1" {
		t.Fatal("explicit target membership lost")
	}
}

func TestCloudLocationRegionOverride(t *testing.T) {
	a, c := cloudTargetFixture()
	a.Targets[0].LocationID = "location-1"
	a.Targets[0].Region = "nyc1"
	m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend, region: sfo3}\n")
	if _, _, err := ResolveCloudTargets(m, "production", a, c); err == nil {
		t.Fatal("manifest region override kept an unrelated location identity")
	}
	a.Targets[0].LocationID = ""
	if _, _, err := ResolveCloudTargets(m, "production", a, c); err != nil {
		t.Fatalf("legacy override semantics changed: %v", err)
	}
}

func TestCloudLocationComposedRegion(t *testing.T) {
	a, c := cloudTargetFixture()
	a.Targets[0].LocationID = "location-1"
	a.Targets[0].Region = "nyc1"
	m := &manifest.Manifest{Resources: map[string]manifest.Resource{"db": {Kind: "postgres", Target: "a"}}}
	placed := composedPlacements(m, "production", []string{"db"}, a, c)
	if placed["db"].Region != "nyc1" {
		t.Fatalf("location region missing from resource placement: %+v", placed)
	}
	r := m.Resources["db"]
	r.Region = "sfo3"
	m.Resources["db"] = r
	if len(composedPlacements(m, "production", []string{"db"}, a, c)) != 0 {
		t.Fatal("resource override pins unrelated location")
	}
}

func TestCloudLocationComposedAmbiguity(t *testing.T) {
	a, c := cloudTargetFixture()
	a.Targets[0].LocationID = "location-nyc"
	a.Targets[0].Region = "nyc1"
	a.Targets[1].ConnectionID = "a"
	a.Targets[1].LocationID = "location-sfo"
	a.Targets[1].Region = "sfo3"
	m := &manifest.Manifest{Resources: map[string]manifest.Resource{"db": {Kind: "postgres", Target: "a"}}}
	if len(composedPlacements(m, "production", []string{"db"}, a, c)) != 0 {
		t.Fatal("shared connection inferred one of two locations")
	}
	r := m.Resources["db"]
	r.Region = "sfo3"
	m.Resources["db"] = r
	placed := composedPlacements(m, "production", []string{"db"}, a, c)
	if placed["db"].LocationID != "location-sfo" {
		t.Fatalf("explicit region did not select its location: %+v", placed)
	}
	a.Targets[0].LocationID = ""
	if len(composedPlacements(m, "production", []string{"db"}, a, c)) != 0 {
		t.Fatal("mixed legacy and classified targets inferred a location")
	}
	a.Targets[1].LocationID = ""
	if len(composedPlacements(m, "production", []string{"db"}, a, c)) != 1 {
		t.Fatal("legacy-only shared connection changed")
	}
}
