package platformcloud

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only the store may name the setting the projection guard trigger checks,
// so no other code can switch the guard off by accident.
func TestPlatformWriteSettingNamedOnlyByTheStore(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "internal", "platformcloud", "store.go")
	migrations := filepath.Join(root, "database", "migrations")
	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".superpowers", ".claude":
				return filepath.SkipDir
			}
			if path == migrations {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == store {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "loomplane.platform_write") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("only internal/platformcloud/store.go may name loomplane.platform_write, found in %v", offenders)
	}
	b, err := os.ReadFile(store)
	if err != nil || !strings.Contains(string(b), "loomplane.platform_write") {
		t.Fatalf("store.go must name the setting: %v", err)
	}
}
