package aisettings

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// nexusTypes reads the provider types nexus's config package builds, one
// per line, from the pinned tag's config/apply.go.
func nexusTypes(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("testdata/nexus-provider-types.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("testdata/nexus-provider-types.txt lists no types")
	}
	return out
}

func TestProviderTypesMatchNexus(t *testing.T) {
	nexus := nexusTypes(t)
	types := ProviderTypes()
	if len(types) == 0 {
		t.Fatal("no provider types")
	}
	seen := map[string]bool{}
	for _, pt := range types {
		if !nexus[pt.Type] {
			t.Errorf("%q isn't a provider type nexus builds", pt.Type)
		}
		if seen[pt.Type] {
			t.Errorf("%q is offered twice", pt.Type)
		}
		seen[pt.Type] = true
		if pt.Title == "" {
			t.Errorf("%q has no title", pt.Type)
		}
	}
	for _, never := range []string{"vertex", "ollama", "lmstudio"} {
		if seen[never] {
			t.Errorf("%q is offered; it must not be", never)
		}
	}
	for i, first := range []string{"anthropic", "openai", "opencompat"} {
		if types[i].Type != first {
			t.Errorf("type %d is %q, want %q", i, types[i].Type, first)
		}
	}
	for i := 4; i < len(types); i++ {
		if strings.ToLower(types[i-1].Title) > strings.ToLower(types[i].Title) {
			t.Errorf("%q sorts before %q", types[i-1].Title, types[i].Title)
		}
	}
	// The field keys are config.ProviderConfig's yaml keys.
	known := map[string]bool{
		"api_key": true, "base_url": true, "resource_name": true, "deployment_id": true, "api_version": true,
		"region": true, "access_key_id": true, "secret_access_key": true, "session_token": true,
	}
	for _, pt := range types {
		for _, f := range pt.Fields {
			if !known[f.Name] {
				t.Errorf("%s field %q isn't a nexus ProviderConfig key", pt.Type, f.Name)
			}
		}
	}
}

func TestProviderTypesHaveAPrimarySecret(t *testing.T) {
	for _, pt := range ProviderTypes() {
		primaries := 0
		for _, f := range pt.Fields {
			if f.Primary {
				primaries++
				if !f.Secret {
					t.Errorf("%s: primary field %q isn't secret", pt.Type, f.Name)
				}
			}
		}
		switch {
		case pt.Type == "opencompat" && primaries > 1:
			t.Errorf("opencompat has %d primary fields", primaries)
		case pt.Type != "opencompat" && primaries != 1:
			t.Errorf("%s has %d primary secret fields, want 1", pt.Type, primaries)
		}
	}

	bedrock, ok := providerType("bedrock")
	if !ok {
		t.Fatal("bedrock isn't offered")
	}
	want := map[string]ProviderField{
		"region":            {Name: "region", Required: true},
		"access_key_id":     {Name: "access_key_id", Secret: true, Required: true, Primary: true},
		"secret_access_key": {Name: "secret_access_key", Secret: true, Required: true},
	}
	for name, w := range want {
		f, ok := bedrock.field(name)
		if !ok {
			t.Errorf("bedrock has no %s field", name)
			continue
		}
		if f.Secret != w.Secret || f.Required != w.Required || f.Primary != w.Primary {
			t.Errorf("bedrock %s = %+v, want secret %v required %v primary %v", name, f, w.Secret, w.Required, w.Primary)
		}
	}

	oc, _ := providerType("opencompat")
	if !oc.Dynamic {
		t.Error("opencompat isn't dynamic")
	}
	if f, ok := oc.field("base_url"); !ok || !f.Required {
		t.Error("opencompat needs a required base_url")
	}
	for _, typ := range []string{"anthropic", "bedrock"} {
		if pt, _ := providerType(typ); pt.Embedding {
			t.Errorf("%s offers embeddings; it has no embeddings API", typ)
		}
	}
	for _, typ := range []string{"openai", "gemini", "mistral", "cohere", "voyageai", "jinaai", "opencompat"} {
		if pt, _ := providerType(typ); !pt.Embedding {
			t.Errorf("%s doesn't offer embeddings", typ)
		}
	}
}
