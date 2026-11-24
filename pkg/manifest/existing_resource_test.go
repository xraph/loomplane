package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

const inventoryResourceID = "providerresource_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestExistingResourceReferenceRoundTrip(t *testing.T) {
	doc := "version: 1\nresources:\n  db:\n    kind: postgres\n    existing_resource: " + inventoryResourceID + "\n"
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m.Resources["db"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["existing_resource"] != inventoryResourceID {
		t.Fatalf("lost inventory identity: %s", raw)
	}
	out, err := WithResource([]byte("version: 1\n# keep this comment\n"), "db", m.Resources["db"])
	if err != nil || !strings.Contains(string(out), inventoryResourceID) || !strings.Contains(string(out), "# keep this comment") {
		t.Fatalf("resource edit: %s, %v", out, err)
	}
}

func TestExistingResourceRejectsCreationAndLifecycleSettings(t *testing.T) {
	for _, extra := range []string{"provider: digitalocean", "target: conn", "region: nyc1", "version: '17'", "gateway: true", "uses: {postgres: {enabled: true}}", "settings: {x: y}", "environments: {staging: {provider: render}}", "gpu: {kind: nvidia-l4, count: 1}"} {
		t.Run(extra, func(t *testing.T) {
			doc := "version: 1\nresources:\n  db:\n    kind: postgres\n    existing_resource: " + inventoryResourceID + "\n    " + extra + "\n"
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatal("external inventory reference accepted resource creation settings")
			}
		})
	}
	if _, err := Parse([]byte("version: 1\nresources:\n  db: {kind: postgres, existing_resource: other-workspace/db}\n")); err == nil {
		t.Fatal("invalid inventory ID accepted")
	}
}

func TestExistingResourceNamesAWorkspaceService(t *testing.T) {
	const service = "endpoint_01jd8x5k2m3n4p5q6r7s8t9v0w"
	doc := "version: 1\nresources:\n  orders:\n    kind: postgres\n    existing_resource: " + service + "\n    environments:\n      staging:\n        enabled: false\nservices:\n  api:\n    source: services/api\n    needs: [orders]\n"
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if m.Resources["orders"].ExistingResource != service {
		t.Fatalf("existing_resource: %q", m.Resources["orders"].ExistingResource)
	}
	for _, bad := range []string{"endpoint_short", "endpoint_01JD8X5K2M3N4P5Q6R7S8T9V0W", "workspace_01jd8x5k2m3n4p5q6r7s8t9v0w"} {
		_, err := Parse([]byte(strings.Replace(doc, service, bad, 1)))
		if err == nil || !strings.Contains(err.Error(), "must name a synced provider resource or a workspace service") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// A workspace service takes no creation or lifecycle settings either.
	if _, err := Parse([]byte(strings.Replace(doc, "    existing_resource:", "    version: \"16\"\n    existing_resource:", 1))); err == nil {
		t.Fatal("a version was accepted on a workspace service")
	}
}
