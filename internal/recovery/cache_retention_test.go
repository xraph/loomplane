package recovery

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestRetentionRecognizesIndependentNativeCacheSnapshots(t *testing.T) {
	for _, kind := range []string{"redis", "valkey"} {
		backup := retentionBackup(t, "point", 30, true)
		recipe, err := recipes.Lookup(kind, recipes.DefaultVersion(kind), "arm64")
		if err != nil {
			t.Fatal(err)
		}
		backup.Source.Config, err = json.Marshal(planning.ResourceConfig{Provider: "server", Kind: kind, Target: "connection", Catalog: recipe})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = retentionScope(&backup); err != nil {
			t.Fatalf("full RDB snapshot refused: %v", err)
		}
		recipe.BackupFormat = "aof-segment"
		backup.Source.Config, err = json.Marshal(planning.ResourceConfig{Provider: "server", Kind: kind, Target: "connection", Catalog: recipe})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = retentionScope(&backup); err == nil {
			t.Fatal("incremental persistence accepted as a full snapshot")
		}
	}
}
