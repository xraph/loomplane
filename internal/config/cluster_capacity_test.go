package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClusterScalingConfigDefaultOff(t *testing.T) {
	defaults := Defaults()
	if defaults.Clouds.ClusterCapacity.Enabled {
		t.Fatal("cluster capacity enabled by default")
	}
	cfg, err := FromSource(mapGetter{"database.url": testDSN, "clouds.cluster_capacity.enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Clouds.ClusterCapacity.Enabled || cfg.Clouds.ManagedClusters.Enabled {
		t.Fatal("cluster capacity gate did not remain independent")
	}
}

// The repository's config.yaml wires automatic capacity to the environment,
// off unless LOOMPLANE_AUTOMATIC_CAPACITY_ENABLED says otherwise.
func TestAutomaticCapacityFromEnvironment(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		env  string
		want bool
	}{{"", false}, {"false", false}, {"true", true}} {
		t.Run("env="+tt.env, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
			t.Setenv("LOOMPLANE_AUTOMATIC_CAPACITY_ENABLED", tt.env)
			c, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Clouds.AutomaticCapacity.Enabled != tt.want {
				t.Fatalf("automatic capacity = %v, want %v", c.Clouds.AutomaticCapacity.Enabled, tt.want)
			}
			if c.Clouds.ClusterCapacity.Enabled || c.Clouds.ManagedClusters.Enabled {
				t.Fatal("automatic capacity turned on another cloud gate")
			}
		})
	}
}
