package deployments

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
)

func TestRunnerBindingPreservesOwnedResources(t *testing.T) {
	for _, scenario := range []string{"create", "update", "replace", "recreate", "omitted", "malformed", "array"} {
		t.Run(scenario, func(t *testing.T) {
			plans := &fakePlans{bindings: map[string]*planning.Binding{}}
			r := &Runner{plans: plans, now: time.Now}
			d := &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "environment", PlanID: "plan"}
			a := planning.Action{Subject: planning.ResourceSubject, Name: "database", Provider: "cloud-sql", Region: "us-central1", Resource: &planning.ResourceConfig{}}
			original := &planning.Binding{ID: "binding", Provider: a.Provider, Region: a.Region, ConnectionID: "connection", ExternalID: "source", PhysicalID: "source-1", OwnedResources: map[string]json.RawMessage{"zone_id": json.RawMessage(`"123"`)}}
			out := &Outcome{ExternalID: "source", PhysicalID: "source-1", OwnedResources: `{"zone_id":"123"}`}
			var current, replaced *planning.Binding
			switch scenario {
			case "update", "omitted":
				current = original
			case "recreate":
				current = original
				out.PhysicalID = "source-2"
				out.OwnedResources = `{"zone_id":"456"}`
			case "replace":
				replaced = original
				out.ExternalID = "destination"
				out.PhysicalID = "destination-2"
				out.OwnedResources = `{"zone_id":"456"}`
			}
			if scenario == "omitted" {
				out.OwnedResources = ""
			}
			if scenario == "malformed" {
				out.OwnedResources = `{`
			}
			if scenario == "array" {
				out.OwnedResources = `[]`
			}
			b, err := r.bind(t.Context(), d, a, current, out, "connection", replaced)
			if scenario == "omitted" || scenario == "malformed" || scenario == "array" {
				if err == nil || len(plans.bindings) != 0 {
					t.Fatal("invalid or omitted ownership accepted", err, b)
				}
				return
			}
			expected := `"123"`
			if scenario == "replace" || scenario == "recreate" {
				expected = `"456"`
			}
			if err != nil || string(b.OwnedResources["zone_id"]) != expected {
				t.Fatal("binding lost ownership", err, b)
			}
			out.OwnedResources = `{"changed":true}`
			if _, changed := b.OwnedResources["changed"]; changed {
				t.Fatal("binding aliases provider outcome")
			}
			if (scenario == "replace" || scenario == "recreate") && (len(b.Replaced) != 1 || string(b.Replaced[0].OwnedResources["zone_id"]) != `"123"`) {
				t.Fatal("replacement lost original ownership", b.Replaced)
			}
			copied := cloneBinding(b)
			copied.OwnedResources["zone_id"][2] = 'x'
			if b.OwnedResources["zone_id"][2] == 'x' {
				t.Fatal("observer aliases current ownership")
			}
			if len(copied.Replaced) > 0 {
				copied.Replaced[0].OwnedResources["zone_id"][2] = 'x'
				if b.Replaced[0].OwnedResources["zone_id"][2] == 'x' {
					t.Fatal("observer aliases replacement ownership")
				}
			}
		})
	}
}

func TestRunnerReturningToOwnedResourceRequiresItsOwnership(t *testing.T) {
	for _, state := range []string{"", `{"zone_id":"changed"}`, `{"zone_id":"original"}`} {
		t.Run(state, func(t *testing.T) {
			plans := &fakePlans{bindings: map[string]*planning.Binding{}}
			r := &Runner{plans: plans, now: time.Now}
			d := &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "environment", PlanID: "plan"}
			a := planning.Action{Subject: planning.ResourceSubject, Name: "database", Provider: "cloud-sql", Region: "us-central1", Resource: &planning.ResourceConfig{}}
			old := planning.Binding{Provider: a.Provider, Region: a.Region, ConnectionID: "connection", ExternalID: "source", PhysicalID: "source-1", OwnedResources: map[string]json.RawMessage{"zone_id": json.RawMessage(`"original"`)}}
			current := &planning.Binding{ID: "binding", Provider: a.Provider, Region: a.Region, ConnectionID: "connection", ExternalID: "destination", PhysicalID: "destination-2", Replaced: []planning.Binding{old}}
			out := &Outcome{ExternalID: old.ExternalID, PhysicalID: old.PhysicalID, OwnedResources: state}
			b, err := r.bind(t.Context(), d, a, current, out, "connection", current)
			if state != `{"zone_id":"original"}` {
				if err == nil || len(plans.bindings) > 0 {
					t.Fatal("return discarded retained ownership", b, err)
				}
				return
			}
			if err != nil || len(b.Replaced) != 1 || b.Replaced[0].PhysicalID != current.PhysicalID {
				t.Fatal("verified return failed", b, err)
			}
		})
	}
}

func TestRunnerBindingKeepsNoReusedImage(t *testing.T) {
	plans := &fakePlans{bindings: map[string]*planning.Binding{}}
	r := &Runner{plans: plans, now: time.Now}
	d := &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "production", PlanID: "plan"}
	artifact := &planning.ReusedArtifact{Reference: "registry.example/app/api@sha256:" + strings.Repeat("a", 64), FromEnvironment: "staging"}
	a := planning.Action{Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Region: "us-central1",
		Service: &planning.ServiceConfig{Provider: "cloud-run", CommitSHA: "abc", Artifact: artifact}}
	b, err := r.bind(t.Context(), d, a, nil, &Outcome{ExternalID: "api"}, "connection", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg planning.ServiceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Artifact != nil || strings.Contains(string(b.Config), `"artifact"`) {
		t.Fatalf("binding config keeps the reused image: %s", b.Config)
	}
	if cfg.CommitSHA != "abc" || a.Service.Artifact != artifact {
		t.Fatalf("binding config %s, action artifact %+v", b.Config, a.Service.Artifact)
	}
}
