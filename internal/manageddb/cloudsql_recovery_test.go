package manageddb

import (
	"maps"
	"testing"
)

func TestCloudSQLRecoveryRouteRequiresCanonicalDedicatedConsumerNetwork(t *testing.T) {
	settings := map[string]string{"cloud_sql_recovery_network": " projects/recovery-consumer/global/networks/recovery-worker "}
	for _, scenario := range []string{"configured", "absent", "url", "subnet", "path-traversal", "missing-project", "missing-network", "malformed-network", "shared-vpc-override"} {
		t.Run(scenario, func(t *testing.T) {
			in := maps.Clone(settings)
			switch scenario {
			case "absent":
				in = nil
			case "url":
				in["cloud_sql_recovery_network"] = "https://example.test/projects/recovery-consumer/global/networks/recovery-worker"
			case "subnet":
				in["cloud_sql_recovery_network"] = "projects/recovery-consumer/regions/us-central1/subnetworks/recovery-worker"
			case "path-traversal":
				in["cloud_sql_recovery_network"] = "projects/../global/networks/recovery-worker"
			case "missing-project":
				in["cloud_sql_recovery_network"] = "projects//global/networks/recovery-worker"
			case "missing-network":
				in["cloud_sql_recovery_network"] = "projects/recovery-consumer/global/networks/"
			case "malformed-network":
				in["cloud_sql_recovery_network"] = "projects/recovery-consumer/global/networks/recovery-worker?source=production"
			case "shared-vpc-override":
				in["cloud_sql_recovery_consumer_project"] = "production-app"
			}
			route, err := CloudSQLRecoverySettings(in)
			if scenario == "absent" {
				if err != nil || route.Valid() || route != (CloudSQLRecoveryNetwork{}) || route.Settings() != nil {
					t.Fatal("missing optional recovery network changed ordinary GCP availability", route, err)
				}
				return
			}
			if scenario != "configured" {
				if err == nil || route.Valid() || route != (CloudSQLRecoveryNetwork{}) {
					t.Fatal("invalid or ambiguous recovery route accepted", route, err)
				}
				return
			}
			if err != nil || !route.Valid() || route.ConsumerProject != "recovery-consumer" || route.Network != "projects/recovery-consumer/global/networks/recovery-worker" || route.Settings()["cloud_sql_recovery_network"] != route.Network {
				t.Fatal("reviewed route lost canonical placement", route, err)
			}
		})
	}
}
