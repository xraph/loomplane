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

func TestClusterManagementProbeConfiguration(t *testing.T) {
	cfg, err := FromSource(mapGetter{"database.url": testDSN,
		"clouds.managed_clusters.management_probe.provider":    "digitalocean",
		"clouds.managed_clusters.management_probe.network_id":  "network",
		"clouds.managed_clusters.management_probe.evidence_id": "evidence",
		"clouds.managed_clusters.management_probe.url":         "https://management.example/readyz",
		"clouds.managed_clusters.management_probe.spki_sha256": "pin",
		"clouds.managed_clusters.management_probe.expires_at":  "2026-10-29T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	probe := cfg.Clouds.ManagedClusters.ManagementProbe
	if probe.Provider != "digitalocean" || probe.NetworkID != "network" || probe.EvidenceID != "evidence" || probe.URL != "https://management.example/readyz" || probe.SPKISHA256 != "pin" || probe.ExpiresAt != "2026-10-29T12:00:00Z" {
		t.Fatal("management probe configuration lost")
	}
}
