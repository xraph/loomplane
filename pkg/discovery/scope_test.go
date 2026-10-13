package discovery

import "testing"

func TestScopeExclusion(t *testing.T) {
	for _, tt := range []struct {
		name, root string
		excluded   bool
	}{
		{"examples/api/main.go", "", true},
		{"samples/server/app.py", "", true},
		{"fixtures/Dockerfile", "", true},
		{"crates/api/testdata/src/main.rs", "", true},
		{"apps/web/__tests__/package.json", "", true},
		{"scripts/server/index.js", "", true},
		{"internal/client/generators/schema", "", true},
		{"services/examples-api", "", false},
		{"services/toolshed", "", false},
		{"examples/polyglot/api", "examples/polyglot", false},
		{"examples/polyglot/testdata/api", "examples/polyglot", true},
		{"apps/web/testdata/api", "apps/web", true},
		{"apps/web/testdata/api", "apps/web/testdata", false},
		{"scripts/migrate", "scripts/migrate", false},
		{"examples/api", ".", true},
	} {
		t.Run(tt.name+"/"+tt.root, func(t *testing.T) {
			if got := ScopeExclusion(tt.name, tt.root); (got != "") != tt.excluded {
				t.Fatalf("ScopeExclusion(%q, %q) = %q, excluded %v", tt.name, tt.root, got, tt.excluded)
			}
		})
	}
}

func TestExplicitTarget(t *testing.T) {
	for _, name := range []string{".forge.yaml", "apps/worker/.forge.yml"} {
		if !ExplicitTarget([]Evidence{{Path: name}}) {
			t.Fatalf("declaration %s was not recognized", name)
		}
	}
	if ExplicitTarget([]Evidence{{Path: "examples/forge.yaml"}, {Path: "go.mod"}}) {
		t.Fatal("ordinary configuration was treated as a declared target")
	}
}
