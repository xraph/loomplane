package cloudinfra

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func TestManagedClusterMigrationCompatibility(t *testing.T) {
	raw := []byte(`{"id":"legacy","runtime":"kubernetes","connection_id":"static"}`)
	var target cloudprofiles.Target
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	if target.ManagedResourceID != "" || target.LocationID != "" {
		t.Fatal("legacy target acquired ownership")
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["managed_resource_id"] != nil || fields["location_id"] != nil {
		t.Fatal("legacy target JSON changed")
	}
}

func TestManagedClusterPageLimit(t *testing.T) {
	for input, want := range map[int]int{-1: 50, 0: 50, 1: 1, 101: 100} {
		if got := resourcePageLimit(input); got != want {
			t.Fatalf("limit %d = %d, want %d", input, got, want)
		}
	}
}
