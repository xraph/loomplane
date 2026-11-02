package deployments

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func cloudRunPSCJob(t *testing.T) *Job {
	t.Helper()
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil {
		t.Fatal(err)
	}
	conn := &connections.Connection{ID: "conn_app", Cloud: connections.GCP, Status: connections.StatusReady, Details: map[string]string{"project_id": "app-project"}}
	route := &provider.CloudSQLApplicationNetwork{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", Subnetwork: "projects/app-project/regions/us-central1/subnetworks/run"}
	resource := &planning.ResourceConfig{Provider: "cloud-sql", Kind: "postgres", Version: "17", Region: "us-central1", Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: conn.ID, AccountIdentity: connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details), CloudSQLPSC: provider.CloudSQLSourceEndpoint{ConsumerProject: route.ConsumerProject, Network: route.Network, ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database"}}}
	raw, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	binding := &planning.Binding{ID: "binding_db", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "database", Provider: "cloud-sql", Region: "us-central1", ConnectionID: conn.ID, ExternalID: "app-project:us-central1:database", PhysicalID: "physical-fixture", Config: raw}
	service := &planning.ServiceConfig{Provider: "cloud-run", Region: "us-central1", CloudSQLNetwork: route, Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "database", Subject: planning.ResourceSubject}, {Key: "LEGACY_DATABASE_URL", From: "database", Subject: planning.ResourceSubject}}}
	return &Job{Deployment: &Deployment{ID: "dep", OrgID: "org", ProjectID: "project", EnvironmentID: "env", PlanID: "plan"}, Plan: &planning.Plan{ID: "plan", Revision: "review", Connections: map[string]string{connections.GCP: conn.ID}, Actions: []planning.Action{{ID: "resource:database", Name: "database", Subject: planning.ResourceSubject, Provider: "cloud-sql", Region: "us-central1", Op: planning.Keep, Resource: resource}}}, Connection: conn, Action: planning.Action{ID: "service:api", Name: "api", Provider: "cloud-run", Region: "us-central1", Subject: planning.ServiceSubject, Service: service}, Attach: []*planning.Binding{binding, binding}, Env: map[string]string{"DATABASE_URL": databaseURL("postgres", "secret + / @", "database", binding.ExternalID), "LEGACY_DATABASE_URL": databaseURL("postgres", "secret + / @", "database", binding.ExternalID), "ORDINARY": "unchanged"}, Secrets: map[string]bool{"DATABASE_URL": true, "LEGACY_DATABASE_URL": true}}
}

func TestCloudRunSpecSeparatesPSCAndLegacySockets(t *testing.T) {
	job := cloudRunPSCJob(t)
	job.Attach = append(job.Attach, &planning.Binding{Provider: "cloud-sql", ExternalID: "app-project:us-central1:legacy"})
	_, labels := spec(job, "image", nil)
	var transport cloudrun.PSCTransport
	if err := json.Unmarshal([]byte(labels[cloudrun.LabelCloudSQLPSC]), &transport); err != nil {
		t.Fatal("PSC transport absent", err)
	}
	if len(transport.Instances) != 1 || transport.Instances[0] != job.Attach[0].ExternalID || transport.Network != *job.Action.Service.CloudSQLNetwork || labels[cloudrun.LabelCloudSQL] != "app-project:us-central1:legacy" {
		t.Fatal("incorrect socket transports", labels)
	}
}

func TestCloudRunPSCPreflightPreservesSecretCredentials(t *testing.T) {
	job := cloudRunPSCJob(t)
	original := job.Env["DATABASE_URL"]
	prepared, err := prepareCloudRunPSC(job)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || len(prepared.sources) != 1 {
		t.Fatal("aliases were not deduplicated")
	}
	for _, key := range []string{"DATABASE_URL", "LEGACY_DATABASE_URL"} {
		value, err := url.Parse(prepared.env[key])
		if err != nil {
			t.Fatal(err)
		}
		password, _ := value.User.Password()
		if value.Query().Get("host") != "/cloudsql-psc/"+job.Attach[0].ExternalID || password != "secret + / @" || value.User.Username() != sqlUser || value.Path != "/database" || !job.Secrets[key] {
			t.Fatal("credential semantics changed")
		}
	}
	if job.Env["DATABASE_URL"] != original || prepared.env["ORDINARY"] != "unchanged" {
		t.Fatal("input environment mutated")
	}
}

func TestCloudRunPSCPreflightRejectsUnreviewedOrUnsafeAttachments(t *testing.T) {
	for _, scenario := range []string{"wrong-plan", "empty-review", "missing-route", "wrong-network", "wrong-region", "fractional-cpu", "private-egress", "memorystore", "missing-review", "changed-review", "foreign-scope", "missing-pin", "wrong-account", "wrong-database", "duplicate-host", "public-secret", "conflicting-alias"} {
		t.Run(scenario, func(t *testing.T) {
			job := cloudRunPSCJob(t)
			switch scenario {
			case "wrong-plan":
				job.Deployment.PlanID = "other-plan"
			case "empty-review":
				job.Plan.Revision = ""
			case "missing-route":
				job.Action.Service.CloudSQLNetwork = nil
			case "wrong-network":
				job.Action.Service.CloudSQLNetwork.Network += "-other"
			case "wrong-region":
				job.Action.Region = "us-east1"
			case "fractional-cpu":
				job.Action.Service.Size = &manifest.Size{CPU: 0.5}
			case "private-egress":
				job.Action.Service.PrivateEgress = true
			case "memorystore":
				job.Attach = append(job.Attach, &planning.Binding{Provider: "memorystore"})
			case "missing-review":
				job.Plan.Actions = nil
			case "changed-review":
				job.Plan.Actions[0].Resource.Managed.CloudSQLPSC.ForwardingRule += "-other"
			case "foreign-scope":
				job.Attach[0].OrgID = "other"
			case "missing-pin":
				job.Attach[0].PhysicalID = ""
			case "wrong-account":
				job.Connection.Details["project_id"] = "other-project"
			case "wrong-database":
				job.Env["DATABASE_URL"] = strings.ReplaceAll(job.Env["DATABASE_URL"], "database?host", "other?host")
			case "duplicate-host":
				job.Env["DATABASE_URL"] += "&host=attacker"
			case "public-secret":
				job.Secrets["DATABASE_URL"] = false
			case "conflicting-alias":
				b := *job.Attach[0]
				b.PhysicalID = "replacement"
				job.Attach[1] = &b
			}
			if _, err := prepareCloudRunPSC(job); err == nil {
				t.Fatal("unsafe attachment accepted")
			}
		})
	}
}
