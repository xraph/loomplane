package planning

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestCloudSQLPSCPlanPinsEndpointAndRejectsInPlaceChanges(t *testing.T) {
	in := managedCatalogInput(t, "cloud-sql")
	r := in.Manifest.Resources["database"]
	endpoint := provider.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/europe-west1/forwardingRules/database"}
	r.CloudSQLPSC = &endpoint
	in.Manifest.Resources["database"] = r
	result := Compute(in)
	if !result.Executable || len(result.Actions) != 1 || result.Actions[0].Resource.Managed == nil {
		t.Fatal("PSC source not reviewed", result)
	}
	cfg := result.Actions[0].Resource
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil || cfg.Managed.Profile != *profile || cfg.Managed.CloudSQLPSC != endpoint {
		t.Fatal("profile or endpoint omitted", cfg, err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Network = "projects/app-project/global/networks/changed"
	if cfg.Managed.CloudSQLPSC.Network == endpoint.Network {
		t.Fatal("review aliases mutable manifest input")
	}
	endpoint = cfg.Managed.CloudSQLPSC
	in.Bindings = []*Binding{{ID: "binding", Name: "database", Subject: ResourceSubject, Provider: "cloud-sql", Region: cfg.Region, ConnectionID: cfg.Managed.ConnectionID, Config: raw, ExternalID: "project:europe-west1:database"}}
	if next := Compute(in); !next.Executable || next.Actions[0].Op != Keep {
		t.Fatal("PSC replan changed the saved review", next)
	}
	in.Catalog = false
	if next := Compute(in); !next.Executable || next.Actions[0].Op != Keep || *next.Actions[0].Resource.Managed != *cfg.Managed {
		t.Fatal("source-backed replan lost its PSC review", next)
	}
	in.Bindings[0].Config = json.RawMessage(`{"truncated"`)
	if next := Compute(in); !slices.ContainsFunc(next.Problems, func(f Finding) bool { return f.Code == "managed-profile-invalid" }) {
		t.Fatal("unreadable source binding silently dropped the PSC review", next)
	}
	in.Bindings[0].Config = raw
	endpoint.ForwardingRule = strings.Replace(endpoint.ForwardingRule, "/database", "/other", 1)
	if next := Compute(in); next.Executable {
		t.Fatal("changed endpoint silently reconfigured a live database")
	}
}

func TestCloudSQLPSCPlanRefusesUnsupportedConsumers(t *testing.T) {
	in := managedCatalogInput(t, "cloud-sql")
	in.Graph = polyglot(t)
	in.Manifest.Services = map[string]manifest.Service{"api": {Source: "services/api", Settings: manifest.Settings{Provider: "cloud-run"}, Needs: []string{"database"}}}
	r := in.Manifest.Resources["database"]
	r.CloudSQLPSC = &provider.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/europe-west1/forwardingRules/database"}
	in.Manifest.Resources["database"] = r
	result := Compute(in)
	if result.Executable || !slices.ContainsFunc(result.Problems, func(f Finding) bool { return f.Code == "cloud-sql-psc-transport-unavailable" }) {
		t.Fatalf("unconfigured PSC consumer was approved: %+v", result)
	}
}

func TestCloudSQLPSCPlanRefusesIgnoredOptIn(t *testing.T) {
	for _, kind := range []string{"mysql", "fabriq"} {
		t.Run(kind, func(t *testing.T) {
			in := managedCatalogInput(t, "cloud-sql")
			r := in.Manifest.Resources["database"]
			r.Kind = kind
			r.CloudSQLPSC = &provider.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/europe-west1/forwardingRules/database"}
			in.Manifest.Resources["database"] = r
			result := Compute(in)
			if !slices.ContainsFunc(result.Problems, func(f Finding) bool { return f.Code == "managed-profile-invalid" }) {
				t.Fatalf("PSC input silently ignored: %+v", result)
			}
		})
	}
}

func TestLegacyManagedProfileOmitsPSCConfiguration(t *testing.T) {
	cfg := Compute(managedCatalogInput(t, "cloud-sql")).Actions[0].Resource.Managed
	raw, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(raw), "cloud_sql_psc") {
		t.Fatal("legacy encoding changed", string(raw), err)
	}
}
