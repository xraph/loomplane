package deployments

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestCloudSQLOwnedCheckpointRestoresBoundIdentityAndRefusesChanges(t *testing.T) {
	for _, scenario := range []string{"bound-reapply", "bound-remove", "forwarding-rule", "missing", "partial", "foreign-project", "foreign-physical", "subnet", "manual-with-ownership", "changed-checkpoint", "cleanup-before-remove"} {
		t.Run(scenario, func(t *testing.T) {
			job := managedDatabaseJob(t, "cloud-sql")
			cfg := job.Action.Resource
			cfg.Region, job.Action.Region, job.Binding.Region = "us-central1", "us-central1", "us-central1"
			route := provider.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database", ProvisionSubnetwork: "projects/app-project/regions/us-central1/subnetworks/psc"}
			profile, err := manageddb.CloudSQLPSCPostgres("17")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Managed.Profile, cfg.Managed.CloudSQLPSC = *profile, route
			job.Binding.PhysicalID = "cloud-sql:original"
			job.Binding.Config, _ = json.Marshal(cfg)
			project := job.Connection.Details["project_id"]
			owned := cloudSQLPSCOwnership{Version: 1, Route: route, Provision: gcp.SQLPSCProvisionState{Version: 2, SourceProject: project, SourcePhysicalID: job.Binding.PhysicalID, Subnet: gcp.SQLApplicationNetwork{Route: provider.CloudSQLApplicationNetwork{ConsumerProject: route.ConsumerProject, Network: route.Network, Subnetwork: route.ProvisionSubnetwork}}, Address: gcp.PSCComputeState{ResourceID: "456", Complete: true}, ForwardingRule: gcp.PSCComputeState{ResourceID: "789", Complete: true}, DNS: gcp.SQLPSCSourceDNSState{Complete: true}}, Endpoint: gcp.SQLPSCProvisionedEndpoint{AddressID: "456", ForwardingRuleID: "789", Address: "10.20.0.8", DNSName: "source.sql.goog.", ServiceAttachment: "attachment"}}
			switch scenario {
			case "partial":
				owned.Provision.DNS.Complete = false
			case "foreign-project":
				owned.Provision.SourceProject = "foreign"
			case "foreign-physical":
				owned.Provision.SourcePhysicalID = "foreign"
			case "forwarding-rule":
				cfg.Managed.CloudSQLPSC.ForwardingRule += "-other"
			case "subnet":
				owned.Provision.Subnet.Route.Subnetwork += "-other"
			case "manual-with-ownership":
				cfg.Managed.CloudSQLPSC.ProvisionSubnetwork = ""
			}
			raw, _ := json.Marshal(owned)
			job.Binding.OwnedResources = map[string]json.RawMessage{cloudSQLPSCOwnershipKey: raw}
			if scenario == "missing" {
				job.Binding.OwnedResources = nil
			}
			job.Action.ID = "resource:database"
			job.Deployment = &Deployment{ID: "deployment"}
			var saved []byte
			job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
			mode := "apply"
			if scenario == "bound-remove" {
				mode = "remove"
			}
			state, err := readCloudSQLState(job, cfg, project, cfg.Region, "source", mode)
			if scenario == "bound-reapply" || scenario == "bound-remove" || scenario == "changed-checkpoint" || scenario == "cleanup-before-remove" {
				if err != nil {
					t.Fatal("bound ownership not restored", err)
				}
				if err = state.save(); err != nil {
					t.Fatal(err)
				}
				if scenario == "changed-checkpoint" {
					state.PSC.Endpoint.Address = "10.20.0.9"
					if err = state.save(); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "cleanup-before-remove" {
					state.PSCCleanup = &gcp.SQLPSCCleanupState{Version: 1}
					if err = state.save(); err != nil {
						t.Fatal(err)
					}
				}
				job.Resume = saved
				state, err = readCloudSQLState(job, cfg, project, cfg.Region, "source", mode)
				if scenario == "bound-reapply" || scenario == "bound-remove" {
					if err != nil || *state.PSC != owned {
						t.Fatal("saved ownership lost", err)
					}
					state.PSC.Endpoint.Address = "mutated"
					var original cloudSQLPSCOwnership
					_ = json.Unmarshal(job.Binding.OwnedResources[cloudSQLPSCOwnershipKey], &original)
					if original.Endpoint.Address != "10.20.0.8" {
						t.Fatal("binding aliases checkpoint")
					}
					return
				}
			}
			if err == nil {
				t.Fatal("changed ownership allowed")
			}
		})
	}
}
