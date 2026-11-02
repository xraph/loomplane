package recipes_test

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func TestDependencyBackupDefaultIsValidatedAndCloned(t *testing.T) {
	base, _ := recipestest.Registry(t).Kind("sample")
	for _, tc := range []struct {
		name, target string
		hours        int
	}{
		{"zero interval", "server", 0}, {"long interval", "server", 169}, {"unknown target", "unknown", 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := base
			kind.Dependencies = append([]recipes.Dependency(nil), base.Dependencies...)
			kind.Dependencies[0].BackupEveryHours = map[string]int{tc.target: tc.hours}
			err := recipes.NewRegistry().Register(kind)
			if err == nil || !strings.Contains(err.Error(), "backup") {
				t.Fatalf("invalid default accepted: %v", err)
			}
		})
	}
	base.Dependencies[0].BackupEveryHours = map[string]int{"server": 24}
	reg := recipes.NewRegistry()
	if err := reg.Register(base); err != nil {
		t.Fatal(err)
	}
	base.Dependencies[0].BackupEveryHours["server"] = 12
	first, _ := reg.Kind("sample")
	if first.Dependencies[0].BackupEveryHours["server"] != 24 {
		t.Fatal("caller changed registered default")
	}
	first.Dependencies[0].BackupEveryHours["server"] = 6
	second, _ := reg.Kind("sample")
	if second.Dependencies[0].BackupEveryHours["server"] != 24 {
		t.Fatal("reader changed registered default")
	}
}
