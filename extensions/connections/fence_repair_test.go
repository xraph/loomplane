package connections

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/forge"
)

type openAPIDoc struct {
	Paths      map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

// The dashboard's client is generated from the committed spec, so it
// carries the fence repair routes, and every schema they reach, as served.
func TestCommittedSpecCarriesFenceRepair(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Loomplane", Version: "1", SpecEnabled: true}))
	if err := (&Extension{}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	committedRaw, err := os.ReadFile("../../apps/dashboard/specs/loomplane.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var served, committed openAPIDoc
	if err = json.Unmarshal(raw, &served); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(committedRaw, &committed); err != nil {
		t.Fatal(err)
	}
	todo := map[string]bool{}
	for _, path := range []string{"/api/v1/connections/{id}/fence-repair/review", "/api/v1/connections/{id}/fence-repair"} {
		item, ok := served.Paths[path]
		if !ok {
			t.Fatalf("the routes don't serve %s", path)
		}
		if !sameJSON(item, committed.Paths[path]) {
			t.Errorf("the committed spec's %s differs from the served one:\n%s", path, item)
		}
		refs(item, todo)
	}
	seen := map[string]bool{}
	for len(todo) > 0 {
		for name := range todo {
			delete(todo, name)
			if seen[name] {
				continue
			}
			seen[name] = true
			if !sameJSON(served.Components.Schemas[name], committed.Components.Schemas[name]) {
				t.Errorf("the committed spec's schema %s differs from the served one:\n%s", name, served.Components.Schemas[name])
			}
			refs(served.Components.Schemas[name], todo)
		}
	}
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if a == nil || b == nil || json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// refs adds the name of every component schema raw refers to.
func refs(raw json.RawMessage, into map[string]bool) {
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
