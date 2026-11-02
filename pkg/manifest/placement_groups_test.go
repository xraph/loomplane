package manifest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func placementGroupManifest() *Manifest {
	return &Manifest{Version: 2, Services: map[string]Service{"api": {Source: ".", Kind: "http-service"}}, Federation: &Federation{Destinations: map[string]DestinationRef{"east": {ID: "dest_00000000000000000000000001"}, "west": {ID: "dest_00000000000000000000000002"}}, PlacementGroups: map[string]FederationPlacementGroup{"web": {Service: "api", MinReady: 2, Routing: GroupRouting{Mode: "external", Hostname: "api.example.com"}, Members: map[string]PlacementMember{"east": {Destination: "east", Replicas: 1, PrerequisiteID: "prereq-east", PrerequisiteRevision: 1}, "west": {Destination: "west", Replicas: 1, PrerequisiteID: "prereq-west", PrerequisiteRevision: 1}}}}}}
}
func TestPlacementGroupBounds(t *testing.T) {
	m := placementGroupManifest()
	if p := ValidateFederation(m); len(p) > 0 {
		t.Fatal(p)
	}
	g := m.Federation.PlacementGroups["web"]
	g.MinReady = 1
	m.Federation.PlacementGroups["web"] = g
	if len(ValidateFederation(m)) == 0 {
		t.Fatal("partial readiness accepted")
	}
	m = placementGroupManifest()
	member := m.Federation.PlacementGroups["web"].Members["east"]
	member.Replicas = 0
	m.Federation.PlacementGroups["web"].Members["east"] = member
	if len(ValidateFederation(m)) == 0 {
		t.Fatal("zero replicas accepted")
	}
	m = placementGroupManifest()
	g = m.Federation.PlacementGroups["web"]
	g.Members = map[string]PlacementMember{}
	m.Federation.Destinations = map[string]DestinationRef{}
	for i := 0; i < 21; i++ {
		name := fmt.Sprintf("m%d", i)
		m.Federation.Destinations[name] = DestinationRef{ID: fmt.Sprintf("dest_%026d", i+1)}
		g.Members[name] = PlacementMember{Destination: name, Replicas: 1, PrerequisiteID: "p-" + name, PrerequisiteRevision: 1}
		g.MinReady = len(g.Members)
		m.Federation.PlacementGroups["web"] = g
		if i == 19 && len(ValidateFederation(m)) != 0 {
			t.Fatal("20 members rejected")
		}
	}
	if len(ValidateFederation(m)) == 0 {
		t.Fatal("21 members accepted")
	}
	m = placementGroupManifest()
	g = m.Federation.PlacementGroups["web"]
	m.Federation.PlacementGroups = map[string]FederationPlacementGroup{}
	for i := 0; i < 100; i++ {
		m.Federation.PlacementGroups[fmt.Sprintf("g%d", i)] = g
	}
	if len(ValidateFederation(m)) != 0 {
		t.Fatal("100 groups rejected")
	}
	m.Federation.PlacementGroups["extra"] = g
	if len(ValidateFederation(m)) == 0 {
		t.Fatal("101 groups accepted")
	}
}
func TestPlacementGroupDuplicateDestination(t *testing.T) {
	m := placementGroupManifest()
	g := m.Federation.PlacementGroups["web"]
	g.Members["west"] = g.Members["east"]
	if len(ValidateFederation(m)) == 0 {
		t.Fatal("duplicate destination accepted")
	}
	m = placementGroupManifest()
	m.Federation.Placements = map[string]FederationPlacement{"old": {Service: "api", Destination: "east"}}
	if len(ValidateFederation(m)) == 0 {
		t.Fatal("legacy and group overlap accepted")
	}
}
func TestPlacementGroupLegacyDigestUnchanged(t *testing.T) {
	for _, fixture := range []struct {
		manifest Manifest
		json     string
	}{
		{Manifest{Version: 1, Services: map[string]Service{"api": {Source: "."}}}, `{"version":1,"services":{"api":{"source":"."}}}`},
		{Manifest{Version: 2, Federation: &Federation{Destinations: map[string]DestinationRef{"east": {ID: "dest_00000000000000000000000001"}}}, Services: map[string]Service{"api": {Source: "."}}}, `{"federation":{"destinations":{"east":{"id":"dest_00000000000000000000000001"}}},"version":2,"services":{"api":{"source":"."}}}`},
	} {
		raw, err := json.Marshal(fixture.manifest)
		if err != nil || sha256.Sum256(raw) != sha256.Sum256([]byte(fixture.json)) {
			t.Fatalf("legacy manifest digest changed: %s %v", raw, err)
		}
	}

	b, err := json.Marshal(Federation{})
	if err != nil || string(b) != "{}" {
		t.Fatalf("legacy JSON changed: %s %v", b, err)
	}
	if _, err = Parse([]byte("version: 2\nfederation:\n  placement_groups:\n    web:\n      unknown: true\n")); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown group field accepted: %v", err)
	}
}
