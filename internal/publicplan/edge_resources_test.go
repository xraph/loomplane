package publicplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestEdgeResourcesExposeReviewedSettingsWithoutCredentialAuthority(t *testing.T) {
	p := &planning.Plan{Actions: []planning.Action{{Service: &planning.ServiceConfig{Worker: &provider.WorkerSettings{Main: "worker.js", CompatibilityDate: "2026-11-01"}}, Resource: &planning.ResourceConfig{Settings: map[string]string{"dimensions": "384", "token": "private-credential"}, EndpointPins: map[string]planning.EndpointPin{"origin_endpoint": {ID: "endpoint", Revision: 2, Engine: "postgres", Host: "db.example.test"}}}}}}
	out, err := json.Marshal(Project(p))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"worker"`) || !strings.Contains(string(out), `"dimensions":"384"`) || !strings.Contains(string(out), `"endpoint_pins"`) {
		t.Fatal("review omitted Worker or provider resource changes")
	}
	if strings.Contains(string(out), "private-credential") {
		t.Fatal("credential authority escaped into public review")
	}
}

func TestMarketplaceMetadataProjectionOnlyIncludesReviewedProvisioningValues(t *testing.T) {
	got := edgeResourceSettings(map[string]string{"metadata": `{"region":"iad1","replicas":2}`})
	if !strings.Contains(got["metadata"], `"region":"iad1"`) {
		t.Fatal("product provisioning options omitted from review")
	}
	for _, raw := range []string{`{"token":"private"}`, `{"region":{"password":"private"}}`, `{"url":"postgres://user:password@db.example/database"}`} {
		if output := edgeResourceSettings(map[string]string{"metadata": raw}); output["metadata"] != "" {
			t.Fatal("private credential escaped through provisioning options")
		}
	}
}
