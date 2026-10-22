package client

import (
	"encoding/json"
	"testing"
)

func TestBindingDecodesPhysicalIdentity(t *testing.T) {
	var binding Binding
	if err := json.Unmarshal([]byte(`{"id":"binding","physical_id":"db-original"}`), &binding); err != nil {
		t.Fatal(err)
	}
	if binding.PhysicalID != "db-original" {
		t.Fatal("API client discarded the native incarnation")
	}
}
