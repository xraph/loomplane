package cloudinfra

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/forge"
)

func TestEdgeOpenAPIContract(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Loomplane", Version: "1", SpecEnabled: true}))
	if err := (&Extension{}).domainRoutes(r, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"domainEdge.get", "domainEdge.update", "domainEdge.purge", "domainEdge.retry"} {
		if !strings.Contains(string(raw), operation) {
			t.Error("missing operation", operation)
		}
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	state := spec.Components.Schemas["EdgeState"].Properties
	if _, ok := state["policy"]; !ok {
		t.Fatal("missing policy")
	}
	if _, ok := state["principal"]; ok {
		t.Fatal("saved authorization leaked")
	}
}
