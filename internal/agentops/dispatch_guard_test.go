package agentops

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Only cortex resuming a run a person approved at a checkpoint excuses an
// approval, inside the engine. No code outside tests calls the registry's
// Dispatch with approved set, anywhere in the repository: anything that
// injects the registry could otherwise skip a checkpoint.
func TestNothingDispatchesAsApproved(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	approved := regexp.MustCompile(`\.Dispatch\([^()]*(\([^()]*\)[^()]*)*,\s*true\s*\)`)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "apps", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if approved.Match(src) {
			t.Errorf("%s dispatches a call as approved", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !approved.MatchString(`reg.Dispatch(ctx, inv, true)`) {
		t.Fatal("the guard doesn't recognise a call")
	}
}
