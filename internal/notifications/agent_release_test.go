package notifications

import "testing"

// Release preparation's outcomes are agent notices: their kind, and the
// webhook the workspace receives for them.
func TestAgentReleaseNotices(t *testing.T) {
	if KindAgentRelease != "agent.release" {
		t.Fatalf("kind %q", KindAgentRelease)
	}
	var found bool
	for _, d := range webhookDefinitions() {
		if d.Name == KindAgentRelease+".created" {
			found = true
			if d.Group != "agent" || d.Version != "2026-11-28" || d.Description != "The agent prepared a production release" {
				t.Fatalf("definition %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("no webhook definition for agent.release.created")
	}
}
