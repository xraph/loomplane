package currentauthority

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

//go:embed testdata/project_schema_sources.json
var projectMigrationSources []byte

func TestProjectSchemaMigrationSourceContract(t *testing.T) {
	var want map[string]string
	if err := json.Unmarshal(projectMigrationSources, &want); err != nil {
		t.Fatal(err)
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, checkErr := os.Stat(filepath.Join(root, "go.mod")); checkErr == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repository not found")
		}
		root = parent
	}
	paths, err := filepath.Glob(filepath.Join(root, "database", "migrations", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		relevant := false
		for name := range projectFunctionBodies {
			if strings.Contains(string(data), name) {
				relevant = true
			}
		}
		for _, name := range []string{"stateEventTriggerFunction", "planEventFunctions", "execPlanEventFunctions"} {
			if strings.Contains(string(data), name) {
				relevant = true
			}
		}
		if relevant {
			sum := sha256.Sum256(data)
			got[filepath.Base(path)] = hex.EncodeToString(sum[:])
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("project function migration references changed; explicitly requalify final definitions and all writers")
	}
}
