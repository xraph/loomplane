package manageddb

import "testing"

func TestCloudSQLSourceEndpointRequiresCanonicalSameRegionResources(t *testing.T) {
	for _, scenario := range []string{"valid", "url", "other-project", "other-region", "wrong-kind", "missing-network", "path-injection", "shared-service-project"} {
		t.Run(scenario, func(t *testing.T) {
			endpoint := CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database"}
			switch scenario {
			case "url":
				endpoint.ForwardingRule = "https://www.googleapis.com/compute/v1/" + endpoint.ForwardingRule
			case "other-project":
				endpoint.ForwardingRule = "projects/other-project/regions/us-central1/forwardingRules/database"
			case "other-region":
				endpoint.ForwardingRule = "projects/app-project/regions/us-east1/forwardingRules/database"
			case "wrong-kind":
				endpoint.ForwardingRule = "projects/app-project/regions/us-central1/addresses/database"
			case "missing-network":
				endpoint.Network = ""
			case "path-injection":
				endpoint.ForwardingRule = "projects/app-project/regions/us-central1/forwardingRules/../database"
			case "shared-service-project":
				endpoint.Network = "projects/host-project/global/networks/apps"
			}
			if endpoint.Valid("us-central1") != (scenario == "valid") {
				t.Fatal("incorrect source endpoint contract", endpoint)
			}
		})
	}
}
