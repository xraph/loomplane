package planning

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	plan "github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
)

func usageConnection(cloud string) *connections.Connection {
	c := &connections.Connection{ID: "connection", OrgID: "org", Cloud: cloud, Account: "123456789012", Details: map[string]string{}}
	switch cloud {
	case "aws":
		c.Details["region"] = "us-east-1"
	case "gcp":
		c.Details["project_id"] = "project-test"
	case "digitalocean":
		c.Details["account_uuid"] = "account-1"
	case "render":
		c.Details["owner_id"] = "tea-1"
	case "azure":
		c.Details = map[string]string{"tenant_id": "11111111-1111-1111-1111-111111111111", "subscription_id": "22222222-2222-2222-2222-222222222222", "resource_group": "selected"}
	}
	return c
}
func usageBinding(t *testing.T, provider, external, physical string, c *connections.Connection) *plan.Binding {
	t.Helper()
	region := "us-east-1"
	if provider == "cloud-sql" || provider == "memorystore" {
		region = "us-central1"
	}
	cfg := plan.ResourceConfig{Provider: provider, Kind: "postgres", Version: "17", Region: region, AccountIdentity: connections.AccountIdentity(c.Cloud, c.Account, c.Details)}
	if provider == "rds" || provider == "cloud-sql" {
		profile, err := manageddb.Postgres(provider, "17")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Managed = &plan.ManagedDatabaseConfig{Profile: *profile, ConnectionID: c.ID, AccountIdentity: cfg.AccountIdentity}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &plan.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Subject: plan.ResourceSubject, Name: "main", Provider: provider, Region: region, ExternalID: external, PhysicalID: physical, ConnectionID: c.ID, PlanID: "plan", Config: raw}
}
func TestProviderResourceBindingUsesNativeIdentityConventions(t *testing.T) {
	azureID := "/subscriptions/22222222-2222-2222-2222-222222222222/resourceGroups/selected/providers/Microsoft.DBforPostgreSQL/flexibleServers/main"
	for _, tc := range []struct {
		provider, cloud, external, physical, wantID, wantIncarnation string
		owned                                                        bool
	}{
		{"digitalocean", "digitalocean", "db/uuid-1", "", "uuid-1", "", false},
		{"azure-database", "azure", azureID, "", strings.ToLower(azureID), "", false},
		{"rds", "aws", "main", "db-PHYSICAL", "arn:aws:rds:us-east-1:123456789012:db:main", "db-PHYSICAL", true},
		{"cloud-sql", "gcp", "project-test:us-central1:main", "cloud-sql:" + strings.Repeat("a", 64), "projects/project-test/instances/main", "cloud-sql:" + strings.Repeat("a", 64), true},
		{"render", "render", "postgres/dpg-1", "", "dpg-1", "", false},
		{"s3", "aws", "bucket", "", "arn:aws:s3:::bucket", "", false},
		{"cloud-storage", "gcp", "bucket", "", "projects/project-test/buckets/bucket", "", false},
		{"elasticache", "aws", "cache", "", "arn:aws:elasticache:us-east-1:123456789012:replicationgroup:cache", "", false},
		{"memorystore", "gcp", "us-central1/cache", "", "projects/project-test/locations/us-central1/instances/cache", "", false},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			c := usageConnection(tc.cloud)
			b := usageBinding(t, tc.provider, tc.external, tc.physical, c)
			if tc.provider == "cloud-sql" || tc.provider == "memorystore" {
				b.Region = "us-central1"
			}
			got, ok := providerResourceBinding(b, c)
			if !ok || got.Provider != tc.cloud || got.ExternalID != tc.wantID || got.Incarnation != tc.wantIncarnation || got.OwnershipVerified != tc.owned || got.AccountIdentity != providerresources.AccountIdentity(c) {
				t.Fatalf("%+v ok=%v", got, ok)
			}
		})
	}
}
func TestProviderResourceBindingRejectsChangedAccountAndUnprovenOwnership(t *testing.T) {
	c := usageConnection("aws")
	for _, scenario := range []string{"changed-account", "wrong-region", "foreign-org", "foreign-connection", "path-injection", "no-physical", "no-plan"} {
		t.Run(scenario, func(t *testing.T) {
			b := usageBinding(t, "rds", "main", "db-PHYSICAL", c)
			current := *c
			switch scenario {
			case "changed-account":
				current.Account = "999999999999"
			case "wrong-region":
				b.Region = "us-west-2"
			case "foreign-org":
				b.OrgID = "other"
			case "foreign-connection":
				b.ConnectionID = "other"
			case "path-injection":
				b.ExternalID = "../main"
			case "no-physical":
				b.PhysicalID = ""
			case "no-plan":
				b.PlanID = ""
			}
			got, ok := providerResourceBinding(b, &current)
			if scenario == "no-physical" || scenario == "no-plan" {
				if !ok || got.OwnershipVerified {
					t.Fatalf("%+v ok=%v", got, ok)
				}
			} else if ok {
				t.Fatalf("unproven identity admitted: %+v", got)
			}
		})
	}
}
func TestProviderResourceBindingExternalReferenceNeverTransfersOwnership(t *testing.T) {
	c := usageConnection("aws")
	b := usageBinding(t, "rds", "arn:aws:rds:us-east-1:123456789012:db:main", "db-PHYSICAL", c)
	cfg := plan.ResourceConfig{Provider: "rds", Kind: "postgres", ExistingResource: &plan.ExistingResourceConfig{ResourceID: "inventory-id", Revision: 7, EndpointID: "endpoint", EndpointRevision: 3, AccountIdentity: "account-pin", ExternalID: b.ExternalID, Incarnation: b.PhysicalID}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.Config = raw
	got, ok := providerResourceBinding(b, nil)
	if !ok || got.InventoryResourceID != "inventory-id" || got.OwnershipVerified || got.Revision != 7 || got.EndpointID != "endpoint" || got.EndpointRevision != 3 || got.AccountIdentity != "account-pin" || got.Incarnation != "db-PHYSICAL" {
		t.Fatalf("%+v ok=%v", got, ok)
	}
}

func TestProviderResourceBindingRejectsForeignNativeScopeAndAttachmentMarkers(t *testing.T) {
	for _, tc := range []struct{ provider, cloud, external string }{
		{"azure-database", "azure", "/subscriptions/22222222-2222-2222-2222-222222222222/resourceGroups/foreign/providers/Microsoft.DBforPostgreSQL/flexibleServers/main"},
		{"cloud-sql", "gcp", "foreign-project:us-central1:main"},
		{"digitalocean", "digitalocean", "app-1"}, {"render", "render", "srv-1"}, {"s3", "aws", "bucket/path"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			c := usageConnection(tc.cloud)
			b := usageBinding(t, tc.provider, tc.external, "", c)
			if got, ok := providerResourceBinding(b, c); ok {
				t.Fatalf("foreign native scope admitted: %+v", got)
			}
		})
	}
	c := usageConnection("aws")
	b := usageBinding(t, "rds", "main", "db-PHYSICAL", c)
	b.Config = json.RawMessage(`{"existing_resource":null}`)
	if got, ok := providerResourceBinding(b, c); ok {
		t.Fatalf("malformed external marker treated as native: %+v", got)
	}
}
func TestProviderResourceBindingOwnershipRequiresExactManagedProfile(t *testing.T) {
	for _, scenario := range []string{"missing-profile", "changed-profile", "changed-managed-account", "changed-managed-connection"} {
		t.Run(scenario, func(t *testing.T) {
			c := usageConnection("aws")
			b := usageBinding(t, "rds", "main", "db-PHYSICAL", c)
			var cfg plan.ResourceConfig
			if err := json.Unmarshal(b.Config, &cfg); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing-profile":
				cfg.Managed = nil
			case "changed-profile":
				cfg.Managed.Profile.Revision = "99"
			case "changed-managed-account":
				cfg.Managed.AccountIdentity = "foreign"
			case "changed-managed-connection":
				cfg.Managed.ConnectionID = "other"
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			b.Config = raw
			got, ok := providerResourceBinding(b, c)
			if got.OwnershipVerified {
				t.Fatalf("unverified ownership: %+v ok=%v", got, ok)
			}
		})
	}
}
