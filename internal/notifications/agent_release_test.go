package notifications

import (
	"strings"
	"testing"
)

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

// Platform Cloud notices reach webhook subscribers only when the catalog lists
// them under the event names the generic outbox path sends.
func TestPlatformCloudWebhookDefinitions(t *testing.T) {
	have := map[string]string{}
	for _, d := range webhookDefinitions() {
		if d.Group == "platform_cloud" {
			have[d.Name] = d.Description
		}
	}
	for _, kind := range []string{"platform_cloud.suspended", "platform_cloud.restored", "platform_cloud.waiting_capacity"} {
		description, ok := have[kind+".created"]
		if !ok || description == "" {
			t.Fatalf("no webhook definition for %s.created", kind)
		}
		if strings.Contains(description, "—") {
			t.Fatalf("description %q has an em dash", description)
		}
	}
}
