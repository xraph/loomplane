package planning

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
)

func managedCatalogInput(t *testing.T, target string) Input {
	t.Helper()
	cloud, region := "aws", "us-east-1"
	if target == "cloud-sql" {
		cloud, region = "gcp", "europe-west1"
	}
	return Input{Catalog: true, Environment: "production", Graph: &graph.Graph{}, Providers: providers.Registry(),
		Manifest:    parse(t, "version: 1\nresources:\n  database: {kind: postgres, provider: "+target+", version: '17', region: "+region+"}\n"),
		Connections: []ConnectionOption{{ID: "account", Account: "123456789012", Cloud: cloud, Ready: true, Region: region, Details: map[string]string{"region": region, "project_id": "project"}}},
		Chosen:      map[string]string{cloud: "account"}}
}

func TestManagedCatalogPinsProfileAndAccount(t *testing.T) {
	for _, target := range []string{"rds", "cloud-sql"} {
		t.Run(target, func(t *testing.T) {
			in := managedCatalogInput(t, target)
			result := Compute(in)
			if !result.Executable || len(result.Actions) != 1 {
				t.Fatalf("managed plan: %+v", result)
			}
			a := result.Actions[0]
			cfg := a.Resource
			conn := in.Connections[0]
			if cfg.Managed == nil || cfg.Managed.Profile.ID != "postgresql-17-"+target || cfg.Managed.ConnectionID != conn.ID || cfg.Managed.AccountIdentity != connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details) || result.Connections[conn.Cloud] != conn.ID || cfg.Target != "" || cfg.Catalog != nil {
				t.Fatalf("unreviewed managed placement: %+v", cfg)
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			in.Bindings = []*Binding{{ID: "binding", Name: "database", Subject: ResourceSubject, Provider: target, Region: cfg.Region, ConnectionID: conn.ID, Config: raw, ExternalID: "instance"}}
			if next := Compute(in); !next.Executable || next.Actions[0].Op != Keep {
				t.Fatalf("profile changed on reconciliation: %+v", next)
			}
			// A source-backed replan must preserve the same safety contract.
			in.Catalog = false
			if next := Compute(in); !next.Executable || next.Actions[0].Op != Keep || next.Actions[0].Resource.Managed == nil {
				t.Fatal("source replan dropped the reviewed profile")
			}
			in.Catalog = true
			for _, change := range []string{"region", "account", "profile", "version", "provider", "connection"} {
				t.Run(change, func(t *testing.T) {
					changed := managedCatalogInput(t, target)
					binding := *in.Bindings[0]
					changed.Bindings = []*Binding{&binding}
					resource := changed.Manifest.Resources["database"]
					switch change {
					case "region":
						resource.Region = "us-west-2"
					case "version":
						resource.Version = "16"
					case "provider":
						resource.Provider = "render"
					case "account":
						changed.Connections[0].Account = "other"
						changed.Connections[0].Details["project_id"] = "other"
					case "connection":
						changed.Connections[0].ID = "other"
						changed.Chosen[conn.Cloud] = "other"
					case "profile":
						old := *cfg
						profile := *cfg.Managed
						profile.Profile.StorageGB++
						old.Managed = &profile
						binding.Config, _ = json.Marshal(old)
					}
					changed.Manifest.Resources["database"] = resource
					if next := Compute(changed); next.Executable {
						t.Fatalf("%s silently replaced or reconfigured a persistent database", change)
					}
				})
			}
		})
	}
}

func TestManagedCatalogPreservesLegacyConfiguration(t *testing.T) {
	in := managedCatalogInput(t, "cloud-sql")
	in.Catalog = false
	legacy := Compute(in)
	if !legacy.Executable || legacy.Actions[0].Resource.Managed != nil {
		t.Fatal("source deployment was silently assigned a profile")
	}
	cfg := legacy.Actions[0].Resource
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	in.Catalog = true
	in.Bindings = []*Binding{{ID: "binding", Name: "database", Subject: ResourceSubject, Provider: cfg.Provider, Region: cfg.Region, ConnectionID: "account", Config: raw, ExternalID: "instance"}}
	if next := Compute(in); !next.Executable || next.Actions[0].Op != Keep || next.Actions[0].Resource.Managed != nil {
		t.Fatal("existing legacy database was silently reconfigured")
	}
}

func TestManagedCatalogRequiresVerifiedAccountScope(t *testing.T) {
	for _, cloud := range []string{"rds", "cloud-sql"} {
		in := managedCatalogInput(t, cloud)
		in.Connections[0].Details = nil
		if Compute(in).Executable {
			t.Fatal("a managed profile had no verified account scope")
		}
	}
}

func TestManagedCatalogRejectsChosenConnectionFromAnotherCloud(t *testing.T) {
	in := managedCatalogInput(t, "rds")
	in.Connections[0].Cloud = "gcp"
	if Compute(in).Executable {
		t.Fatal("a ready connection from another cloud bypassed the managed profile")
	}
}

func TestManagedCatalogMissingBindingPreservesReview(t *testing.T) {
	for _, target := range []string{"rds", "cloud-sql"} {
		for _, standalone := range []bool{false, true} {
			in := managedCatalogInput(t, target)
			initial := Compute(in)
			cfg := initial.Actions[0].Resource
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			in.Catalog = standalone
			in.Bindings = []*Binding{{ID: "binding", Name: "database", Subject: ResourceSubject, Provider: target, Region: cfg.Region, ConnectionID: "account", Config: raw, Drift: DriftMissing, ExternalID: "instance"}}
			next := Compute(in)
			if !next.Executable || len(next.Actions) != 1 || next.Actions[0].Op != Create || next.Actions[0].Resource.Managed == nil || *next.Actions[0].Resource.Managed != *cfg.Managed {
				t.Fatalf("%s standalone=%v lost its reviewed profile when recreating a missing database: %+v", target, standalone, next)
			}
			// A missing observation cannot grant authority to move the database.
			in.Connections[0].Account = "other"
			in.Connections[0].Details["project_id"] = "other"
			if Compute(in).Executable {
				t.Fatalf("%s missing database silently moved accounts", target)
			}
		}
	}
}
