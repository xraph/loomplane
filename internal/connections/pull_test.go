package connections

import "testing"

func TestRegistryPullReady_refusesStaleAndUnverifiedAccess(t *testing.T) {
	c := &Connection{Cloud: Registry, Status: StatusIncomplete, Details: map[string]string{"api": "ready", "pull": "ready", "push": "unavailable"}, Missing: []string{"registry:push"}}
	if !RegistryPullReady(c) {
		t.Fatal("verified pull-only credential was refused")
	}
	c.Status = StatusFailed
	if RegistryPullReady(c) {
		t.Fatal("failed check retained stale pull capability")
	}
	c.Status = StatusIncomplete
	c.Missing = append(c.Missing, "registry:api")
	if RegistryPullReady(c) {
		t.Fatal("incomplete API access was accepted")
	}
	c.Missing = []string{"registry:push"}
	c.Details["pull"] = "unavailable"
	if RegistryPullReady(c) {
		t.Fatal("missing pull access was accepted")
	}
}
