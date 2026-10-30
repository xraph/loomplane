package config

import "testing"

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
