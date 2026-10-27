package plane

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestPlaneImportsAreShippable keeps the data plane buildable on its own: it
// ships as a separate binary, so it may use the standard library, pgx and
// gorilla/websocket and nothing else from the repository or the module graph.
func TestPlaneImportsAreShippable(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: bad import %s", name, imp.Path.Value)
			}
			first, _, _ := strings.Cut(path, "/")
			switch {
			case !strings.Contains(first, "."): // standard library
			case path == "github.com/jackc/pgx/v5" || strings.HasPrefix(path, "github.com/jackc/pgx/v5/"):
			case path == "github.com/gorilla/websocket":
			default:
				t.Errorf("%s imports %q, which the data plane may not use", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no non-test Go files to check")
	}
}
