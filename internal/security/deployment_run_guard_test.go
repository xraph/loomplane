package security

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyDeploymentsMarkADeploymentRun: the mark lets the agent change
// infrastructure, so only the deployments runner may set it. This scans the
// module's non-test Go source for the call.
func TestOnlyDeploymentsMarkADeploymentRun(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s: %v", root, err)
	}
	allowed := map[string]bool{"internal/deployments": true, "internal/security": true}
	var inDeployments, checked int
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".superpowers", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		checked++
		if !strings.Contains(string(raw), "WithDeploymentRun(") {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !allowed[rel] {
			t.Errorf("%s marks a deployment run; only internal/deployments may", filepath.ToSlash(filepath.Join(rel, d.Name())))
		}
		if rel == "internal/deployments" {
			inDeployments++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 || inDeployments == 0 {
		t.Fatalf("scanned %d files and found the runner's mark in %d; the scan isn't seeing the source", checked, inDeployments)
	}
}
