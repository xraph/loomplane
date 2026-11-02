package manageddb

import (
	"encoding/json"
	"testing"
)

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

func TestCloudSQLProvisionSubnetIsExplicitAndValidated(t *testing.T) {
	for _, subnet := range []string{"projects/app-project/regions/us-central1/subnetworks/psc", "projects/app-project/regions/us-east1/subnetworks/psc", "projects/other-project/regions/us-central1/subnetworks/psc", "https://example.com/subnet"} {
		t.Run(subnet, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"consumer_project": "app-project", "network": "projects/app-project/global/networks/apps", "forwarding_rule": "projects/app-project/regions/us-central1/forwardingRules/database", "provision_subnetwork": subnet})
			var endpoint CloudSQLSourceEndpoint
			if err := json.Unmarshal(raw, &endpoint); err != nil {
				t.Fatal(err)
			}
			valid := subnet == "projects/app-project/regions/us-central1/subnetworks/psc"
			if endpoint.Valid("us-central1") != valid {
				t.Fatal("invalid provisioning subnet accepted", subnet)
			}
			encoded, _ := json.Marshal(endpoint)
			var kept map[string]string
			_ = json.Unmarshal(encoded, &kept)
			if kept["provision_subnetwork"] != subnet {
				t.Fatal("provisioning intent lost on round trip")
			}
		})
	}
}
