package config

import "testing"

func TestClusterQualificationConfiguration(t *testing.T) {
	cfg, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil || cfg.Clouds.ManagedClusters.Enabled {
		t.Fatal("enabled without qualification", err)
	}
	cfg, err = FromSource(mapGetter{"database.url": testDSN, "clouds.managed_clusters.enabled": true, "clouds.managed_clusters.qualification_org_id": "org", "clouds.managed_clusters.qualification_operator_id": "user", "clouds.managed_clusters.qualification_account_id": "account", "clouds.managed_clusters.qualification_region": "nyc1", "clouds.managed_clusters.max_nodes": 3, "clouds.managed_clusters.max_account_creates": 1})
	if err != nil || !cfg.Clouds.ManagedClusters.Enabled || cfg.Clouds.ManagedClusters.QualificationOperatorID != "user" || cfg.Clouds.ManagedClusters.MaxNodes != 3 || cfg.Clouds.ManagedClusters.MaxAccountCreates != 1 {
		t.Fatal("qualification config lost", err)
	}
}
