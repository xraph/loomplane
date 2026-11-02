package config

import "testing"

func TestPlacementGroupGateDefaultsOff(t *testing.T) {
	cfg, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil || cfg.Clouds.MultiLocationDelivery {
		t.Fatalf("group delivery enabled by default: %v", err)
	}
	cfg, err = FromSource(mapGetter{"database.url": testDSN, "clouds.multi_location_delivery": true})
	if err != nil || !cfg.Clouds.MultiLocationDelivery {
		t.Fatalf("explicit group review gate unavailable: %v", err)
	}
}
