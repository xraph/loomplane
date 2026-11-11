package config

import "testing"

func TestFleetMetricRetentionConfiguration(t *testing.T) {
	for _, days := range []int{-1, 3651} {
		if err := (NotificationsConfig{FleetMetricRetentionDays: days}).Validate(EnvDevelopment); err == nil {
			t.Fatalf("invalid retention %d", days)
		}
	}
	for _, days := range []int{0, 1, 90, 3650} {
		if err := (NotificationsConfig{FleetMetricRetentionDays: days}).Validate(EnvDevelopment); err != nil {
			t.Fatalf("valid retention %d: %v", days, err)
		}
	}
}
