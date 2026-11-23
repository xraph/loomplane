package gcp

import "testing"

func TestRegistryEndpoint(t *testing.T) {
	for loc, want := range map[string]string{"us-central1": "https://us-central1-docker.pkg.dev", "europe": "https://europe-docker.pkg.dev"} {
		if got, err := RegistryEndpoint(loc); err != nil || got != want {
			t.Errorf("%s: %q %v", loc, got, err)
		}
	}
	for _, bad := range []string{"", "US-CENTRAL1", "us-central1.evil.example", "us/central1"} {
		if _, err := RegistryEndpoint(bad); err == nil {
			t.Errorf("location %q was accepted", bad)
		}
	}
}
