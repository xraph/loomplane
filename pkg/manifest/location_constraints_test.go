package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLocationPlacementOptionalHistory(t *testing.T) {
	b, err := json.Marshal(Settings{})
	if err != nil || strings.Contains(string(b), "placement") {
		t.Fatalf("legacy JSON changed: %s %v", b, err)
	}
	s := Service{Settings: Settings{Placement: &PlacementConstraints{Architecture: "amd64"}}, Environments: map[string]Settings{"production": {Placement: &PlacementConstraints{Architecture: "arm64"}}}}
	if got := s.For("production", Defaults{}).Placement; got == nil || got.Architecture != "arm64" {
		t.Fatalf("override lost: %+v", got)
	}
	if got := s.For("development", Defaults{}).Placement; got == nil || got.Architecture != "amd64" {
		t.Fatalf("base lost: %+v", got)
	}
}

func TestLocationPlacementConstraintValidation(t *testing.T) {
	for _, value := range []PlacementConstraints{{Architecture: "guess"}, {Residency: []string{"us-east-1"}}, {Residency: []string{"ZZ"}}, {Residency: []string{"XX"}}, {Availability: "automatic-failover"}, {GPU: &GPU{Kind: "a100", Count: 0}}, {AllowedLocations: []string{"x", "x"}}} {
		if err := value.Validate(); err == nil {
			t.Fatalf("invalid constraints accepted: %+v", value)
		}
	}
}

func TestLocationPlacementParse(t *testing.T) {
	m, err := Parse([]byte("version: 1\nservices:\n  api:\n    source: .\n    placement:\n      residency: [US]\n      architecture: amd64\n"))
	if err != nil || m.Services["api"].Placement == nil {
		t.Fatalf("placement did not parse: %+v %v", m, err)
	}
	if _, err = Parse([]byte("version: 1\nservices:\n  api:\n    source: .\n    placement:\n      residency: [ZZ]\n")); err == nil {
		t.Fatal("reserved jurisdiction accepted")
	}
}
