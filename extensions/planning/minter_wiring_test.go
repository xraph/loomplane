package planning

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// mintingClouds are the cloud packages whose clients a keyless connection
// needs a minter in.
var mintingClouds = map[string]bool{
	"github.com/xraph/loomplane/internal/clouds/aws":   true,
	"github.com/xraph/loomplane/internal/clouds/gcp":   true,
	"github.com/xraph/loomplane/internal/clouds/azure": true,
}

// Every AWS, Google Cloud and Azure client this extension builds carries
// the issuer as its minter. Without one a ready keyless connection fails
// every deployment with ErrNoMinter, so a client made with New and not
// given WithMinter at once fails here.
func TestEveryCloudClientCarriesAMinter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	built := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		clouds := map[string]bool{}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !mintingClouds[path] {
				continue
			}
			local := filepath.Base(path)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			clouds[local] = true
		}
		minted := map[*ast.CallExpr]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithMinter" {
				if call, ok := sel.X.(*ast.CallExpr); ok {
					minted[call] = true
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "New" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && clouds[pkg.Name] {
				built++
				if !minted[call] {
					t.Errorf("%s: %s.New builds a client without WithMinter", fset.Position(call.Pos()), pkg.Name)
				}
			}
			return true
		})
	}
	if built < 3 {
		t.Fatalf("found %d cloud clients, want at least one each for AWS, Google Cloud and Azure", built)
	}
}
