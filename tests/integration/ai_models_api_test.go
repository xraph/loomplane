//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

// With AI off the models API isn't there. With it on, a signed-in owner
// browses the registry and reads the AI settings, and the published spec
// has the routes with the shapes the dashboard's client is generated from:
// no moving in a deploy's body, a status's move cost and a cost's source.
// Nothing here reaches Hugging Face or a cloud.
func TestModelsAPIPublishedContract(t *testing.T) {
	off := config.Defaults()
	off.Database.URL = freshDatabase(t)
	off.Storage.URL = "file://" + t.TempDir()
	off.Dashboard.Dir = t.TempDir()
	app := start(t, off)
	c := &client{t: t, base: app.srv.URL, key: off.Identity.PublishableKey}
	for _, path := range []string{"/api/v1/ai/models/registry", "/api/v1/ai/settings", "/api/v1/ai/models/deployments"} {
		c.must(http.StatusNotFound, http.MethodGet, path, nil, nil)
	}
	app.stop()

	on := config.Defaults()
	on.AI.Enabled = true
	on.Database.URL = freshDatabase(t)
	on.Storage.URL = "file://" + t.TempDir()
	on.Dashboard.Dir = t.TempDir()
	app = start(t, on)
	defer app.stop()
	c = &client{t: t, base: app.srv.URL, key: on.Identity.PublishableKey}
	c.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/ai/models/registry", nil, nil)

	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{
		"email": "ada@example.com", "password": "Correct-horse-battery-9", "first_name": "Ada",
	}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Analytical Engines", "slug": "analytical-engines"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	var list struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
		Counts map[string]int `json:"counts"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/ai/models/registry?task=embedding", nil, &list)
	if len(list.Entries) == 0 || list.Counts["embedding"] != len(list.Entries) {
		t.Fatalf("registry: %d entries, counts %v", len(list.Entries), list.Counts)
	}
	var entry struct {
		ID string `json:"id"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/ai/models/registry/entry?id="+list.Entries[0].ID, nil, &entry)
	if entry.ID != list.Entries[0].ID {
		t.Fatalf("entry %q, want %q", entry.ID, list.Entries[0].ID)
	}
	var deployed struct {
		Models []json.RawMessage `json:"models"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/ai/models/deployments", nil, &deployed)
	if deployed.Models == nil || len(deployed.Models) != 0 {
		t.Fatalf("deployments: %v", deployed.Models)
	}
	var view struct {
		CanManage bool `json:"can_manage"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/ai/settings", nil, &view)
	if !view.CanManage {
		t.Fatal("the owner can't manage the AI settings")
	}
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/projects/nope/environments/nope/ai/models/llm", nil, nil)

	var raw json.RawMessage
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	var spec struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	const env = "/api/v1/projects/{projectId}/environments/{environmentId}/ai/models"
	for path, methods := range map[string][]string{
		"/api/v1/ai/models/registry":            {"get"},
		"/api/v1/ai/models/registry/entry":      {"get"},
		"/api/v1/ai/models/registry/card":       {"get"},
		"/api/v1/ai/models/inspect":             {"post"},
		"/api/v1/ai/models/deployments":         {"get"},
		env + "/preview":                        {"post"},
		env:                                     {"post"},
		env + "/{resource}":                     {"get", "patch", "delete"},
		env + "/{resource}/move-preview":        {"post"},
		env + "/{resource}/move":                {"post"},
		env + "/{resource}/retry":               {"post"},
		env + "/{resource}/move-to-pin":         {"post"},
		env + "/{resource}/waiting":             {"delete"},
		"/api/v1/ai/settings":                   {"get"},
		"/api/v1/ai/settings/huggingface-token": {"put", "delete"},
	} {
		for _, m := range methods {
			if _, ok := spec.Paths[path][m]; !ok {
				t.Errorf("the spec has no %s %s", m, path)
			}
		}
	}
	for path := range spec.Paths {
		for _, deferred := range []string{"/usable", "/use", "/usage", "/try-key", "/update-preview", "/endpoints"} {
			if strings.Contains(path, "/ai/") && strings.HasSuffix(path, deferred) {
				t.Errorf("the spec serves %s, which waits for the gateway", path)
			}
		}
	}
	schemas := spec.Components.Schemas
	// Forge names a request body <Type>Body, and prefixes the package when
	// two types share a name.
	if _, ok := schemas["DeployInputBody"].Properties["moving"]; ok || len(schemas["DeployInputBody"].Properties) == 0 {
		t.Errorf("DeployInputBody: %v", schemas["DeployInputBody"].Properties)
	}
	if _, ok := schemas["MovingFrom"]; ok {
		t.Error("the spec has MovingFrom")
	}
	for schema, props := range map[string][]string{
		"ModelsStatus": {"move_cost", "cost", "linked", "pin", "update_available", "month_to_date_usd", "projected_usd"},
		"Cost":         {"source", "known", "estimate", "hourly_usd", "monthly_usd", "share"},
		"LicenseInfo":  {"id", "title", "url", "commercial"},
		"Problem":      {"code", "message"},
	} {
		for _, prop := range props {
			if _, ok := schemas[schema].Properties[prop]; !ok {
				t.Errorf("%s has no %s: %v", schema, prop, schemas[schema].Properties)
			}
		}
	}
	for name, s := range schemas {
		for prop := range s.Properties {
			if prop == "api_key" || prop == "APIKey" {
				t.Errorf("%s publishes %s", name, prop)
			}
		}
	}
	// The committed spec, which the dashboard's client is generated from,
	// is grafted by hand. Every AI path the app serves, and every schema
	// those paths reach, must be in it as served, so drift fails here.
	committedRaw, err := os.ReadFile("../../apps/dashboard/specs/loomplane.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	served, committed := openAPIDoc(t, raw), openAPIDoc(t, committedRaw)
	todo := map[string]bool{}
	for path, item := range served.Paths {
		if !strings.Contains(path, "/ai/") {
			continue
		}
		if !jsonEqual(item, committed.Paths[path]) {
			t.Errorf("the committed spec's %s differs from the served one", path)
		}
		schemaRefs(item, todo)
	}
	seen := map[string]bool{}
	for len(todo) > 0 {
		for name := range todo {
			delete(todo, name)
			if seen[name] {
				continue
			}
			seen[name] = true
			if !jsonEqual(served.Components.Schemas[name], committed.Components.Schemas[name]) {
				t.Errorf("the committed spec's schema %s differs from the served one", name)
			}
			schemaRefs(served.Components.Schemas[name], todo)
		}
	}
	if len(seen) == 0 {
		t.Error("the AI paths reach no schema")
	}
	// The copies of a model in each environment: a deploy's preview says
	// which resource it adds an environment to, and the manifest edit
	// (not an AI path, so not walked above) edits one environment's
	// block. A pin move plans one environment, so a deploy's result has
	// one plan.
	for _, doc := range []struct {
		name string
		doc  openAPIDocument
	}{{"served", served}, {"committed", committed}} {
		for _, prop := range [][2]string{{"DeployPreview", "adds_to"}, {"EditInputBody", "update_resource.environment"}, {"EditInputBody", "update_resource.enabled"}} {
			if property(t, doc.doc, prop[0], prop[1]) == nil {
				t.Errorf("the %s spec's %s has no %s", doc.name, prop[0], prop[1])
			}
		}
		if property(t, doc.doc, "DeployResult", "plans") != nil {
			t.Errorf("the %s spec's DeployResult has plans", doc.name)
		}
	}
	for _, prop := range [][2]string{{"DeployPreview", "adds_to"}, {"EditInputBody", "update_resource.environment"}, {"EditInputBody", "update_resource.enabled"}} {
		if !jsonEqual(property(t, served, prop[0], prop[1]), property(t, committed, prop[0], prop[1])) {
			t.Errorf("the committed spec's %s.%s differs from the served one", prop[0], prop[1])
		}
	}
	if output := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); output != "" {
		if err := os.WriteFile(output, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// openAPIDocument is the parts of an OpenAPI document the contract compares.
type openAPIDocument struct {
	Paths      map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

func openAPIDoc(t *testing.T, raw []byte) openAPIDocument {
	t.Helper()
	var doc openAPIDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// property is the schema of a property of a component schema, by a dotted
// path through nested objects ("update_resource.environment"), or nil.
func property(t *testing.T, doc openAPIDocument, schema, path string) json.RawMessage {
	t.Helper()
	raw := doc.Components.Schemas[schema]
	for name := range strings.SplitSeq(path, ".") {
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if raw == nil || json.Unmarshal(raw, &s) != nil {
			return nil
		}
		raw = s.Properties[name]
	}
	return raw
}

// jsonEqual compares two JSON values whatever their key order and layout.
// A missing value (nil) equals nothing.
func jsonEqual(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return false
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// schemaRefs adds the name of every component schema raw refers to.
func schemaRefs(raw json.RawMessage, into map[string]bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if ref, ok := x.(string); ok && k == "$ref" {
					if name, found := strings.CutPrefix(ref, "#/components/schemas/"); found {
						into[name] = true
					}
					continue
				}
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
}
