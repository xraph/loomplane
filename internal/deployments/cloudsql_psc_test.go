package deployments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudSQLPSCReviewRejectsChangedSourceBeforeProviderAccess(t *testing.T) {
	for _, scenario := range []string{"endpoint", "drop-endpoint", "profile", "region", "account"} {
		t.Run(scenario, func(t *testing.T) {
			job := managedDatabaseJob(t, "cloud-sql")
			cfg := job.Action.Resource
			cfg.Region, job.Action.Region, job.Binding.Region = "us-central1", "us-central1", "us-central1"
			profile, err := manageddb.CloudSQLPSCPostgres("17")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Managed.Profile = *profile
			cfg.Managed.CloudSQLPSC = manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database"}
			job.Binding.Config, err = json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = managedDatabaseRequest(job, "cloud-sql"); err != nil {
				t.Fatal("valid PSC review rejected", err)
			}
			switch scenario {
			case "endpoint":
				cfg.Managed.CloudSQLPSC.ForwardingRule += "-other"
			case "drop-endpoint":
				cfg.Managed.CloudSQLPSC = manageddb.CloudSQLSourceEndpoint{}
			case "profile":
				cfg.Managed.Profile.StorageGB++
			case "region":
				cfg.Region = "us-east1"
			case "account":
				job.Connection.Details["project_id"] = "another-project"
			}
			if _, err = (&CloudSQL{}).Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "managed database review") {
				t.Fatal("changed review reached provider", err)
			}
		})
	}
}

func TestCloudRunRefusesPSCBindingBeforeProviderAccess(t *testing.T) {
	cfg := planning.ResourceConfig{Managed: &planning.ManagedDatabaseConfig{CloudSQLPSC: manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project"}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{Action: planning.Action{Service: &planning.ServiceConfig{}}, Attach: []*planning.Binding{{Provider: "cloud-sql", Config: raw}}}
	if _, err = (&CloudRun{}).Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "PSC application transport") {
		t.Fatal("unconfigured PSC attachment reached provider", err)
	}
}
