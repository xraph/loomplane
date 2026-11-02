package deployments

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func nativeUsageFixture(t *testing.T, cloud, kind string) (*planning.Binding, *connections.Connection, *planning.ResourceConfig) {
	t.Helper()
	details := map[string]string{"account_id": cfAccount}
	adapter := "cloudflare-workers"
	if cloud == connections.Vercel {
		details = map[string]string{"team_id": "team_account"}
		adapter = "vercel"
	}
	conn := &connections.Connection{ID: "connection", OrgID: "org", Cloud: cloud, Status: connections.StatusReady, Details: details}
	identity := connections.AccountIdentity(cloud, "", details)
	cfg := &planning.ResourceConfig{Provider: adapter, Kind: kind, Target: conn.ID, AccountIdentity: identity, TargetSettings: planning.ReviewedConnectionSettings(cloud, details), Settings: map[string]string{}}
	config, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Name: "resource", Provider: adapter, ConnectionID: conn.ID, Subject: planning.ResourceSubject, Config: config, OwnedResources: map[string]json.RawMessage{}}
	return b, conn, cfg
}

func TestNativeProviderResourceBindingsVerifyCloudflareNativeIncarnations(t *testing.T) {
	for _, kind := range []string{"cloudflare-d1", "cloudflare-kv", "object-storage", "cloudflare-vectorize", "cloudflare-hyperdrive", "cloudflare-queue"} {
		t.Run(kind, func(t *testing.T) {
			b, conn, cfg := nativeUsageFixture(t, connections.Cloudflare, kind)
			id := cfResourceID
			incarnation := id
			if kind == "cloudflare-d1" {
				id = cfVersion
				incarnation = id
			}
			if kind == "object-storage" {
				id = "assets"
				incarnation = "2026-11-01T00:00:00Z"
				cfg.Settings["jurisdiction"] = "eu"
			}
			if kind == "cloudflare-vectorize" {
				id = "embeddings"
				incarnation = "2026-11-01T00:00:00Z"
			}
			b.ExternalID, b.PhysicalID = id, id
			if kind == "object-storage" || kind == "cloudflare-vectorize" {
				b.PhysicalID = id + "@" + incarnation
			}
			b.Config, _ = json.Marshal(cfg)
			receipt := CloudflareResourceOwnedReceipt{AccountID: cfAccount, AccountIdentity: cfg.AccountIdentity, ConnectionID: conn.ID, OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Name: b.Name, Kind: kind, ResourceID: id, Incarnation: incarnation, ConfigDigest: cloudflareConfigurationDigest(b.Name, cfg)}
			b.OwnedResources["cloudflare-resource"], _ = json.Marshal(receipt)
			got := NativeProviderResourceBindings(b, conn)
			wantID := id
			if kind == "object-storage" {
				wantID = "r2/eu/assets"
			}
			if len(got) != 1 || got[0].ExternalID != wantID || got[0].Incarnation != incarnation || !got[0].OwnershipVerified || got[0].Provider != connections.Cloudflare {
				t.Fatalf("projection %#v", got)
			}
			b.PhysicalID = "another-incarnation"
			if got = NativeProviderResourceBindings(b, conn); len(got) != 0 {
				t.Fatalf("tampered incarnation managed: %#v", got)
			}
		})
	}
}

func TestNativeProviderResourceBindingsRejectTamperedScopeAndImportedMetadata(t *testing.T) {
	for _, change := range []string{"missing-receipt", "wrong-org", "wrong-project", "wrong-environment", "wrong-account", "wrong-connection", "wrong-config", "imported"} {
		t.Run(change, func(t *testing.T) {
			b, conn, cfg := nativeUsageFixture(t, connections.Cloudflare, "cloudflare-kv")
			b.ExternalID = cfResourceID
			b.PhysicalID = cfResourceID
			receipt := CloudflareResourceOwnedReceipt{AccountID: cfAccount, AccountIdentity: cfg.AccountIdentity, ConnectionID: conn.ID, OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Name: b.Name, Kind: cfg.Kind, ResourceID: cfResourceID, Incarnation: cfResourceID, ConfigDigest: cloudflareConfigurationDigest(b.Name, cfg)}
			switch change {
			case "wrong-org":
				receipt.OrgID = "other"
			case "wrong-project":
				receipt.ProjectID = "other"
			case "wrong-environment":
				receipt.EnvironmentID = "other"
			case "wrong-account":
				conn.Details["account_id"] = "023e105f4ecef8ad9ca31a8372d0c354"
			case "wrong-connection":
				receipt.ConnectionID = "other"
			case "wrong-config":
				cfg.Settings["jurisdiction"] = "us"
				b.Config, _ = json.Marshal(cfg)
			case "imported":
				cfg.ExistingResource = &planning.ExistingResourceConfig{}
				b.Config, _ = json.Marshal(cfg)
			}
			if change != "missing-receipt" {
				b.OwnedResources["cloudflare-resource"], _ = json.Marshal(receipt)
			}
			if got := NativeProviderResourceBindings(b, conn); len(got) != 0 {
				t.Fatalf("unsafe receipt marked managed: %#v", got)
			}
		})
	}
}

func TestNativeProviderResourceBindingsProjectOnlyOwnedWorkerNamespaces(t *testing.T) {
	b, conn, _ := nativeUsageFixture(t, connections.Cloudflare, "cloudflare-kv")
	b.Subject = planning.ServiceSubject
	b.Name = "api"
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	cfg := &planning.ServiceConfig{Provider: "cloudflare-workers", Target: conn.ID, TargetAccountIdentity: identity, Worker: &provider.WorkerSettings{Main: "worker.js", CompatibilityDate: "2026-11-01", DurableObjects: []provider.DurableObjectClass{{Name: "COUNTER", ClassName: "Counter"}}}}
	b.Config, _ = json.Marshal(cfg)
	job := &Job{Deployment: &Deployment{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID}, Action: planning.Action{Name: b.Name}}
	b.ExternalID = CloudflareWorkerName(job)
	b.PhysicalID = b.ExternalID + "@2026-11-01T00:00:00Z"
	receipt := CloudflareWorkerOwnedReceipt{AccountID: cfAccount, ScriptName: b.ExternalID, ScriptCreatedAt: "2026-11-01T00:00:00Z", OwnerTag: cloudflareOwner(b.OrgID, b.ProjectID, b.EnvironmentID, b.Name), ConfigDigest: cloudflareConfigurationDigest(b.Name, cfg), Namespaces: []CloudflareWorkerOwnedNamespace{{ID: cfResourceID, ClassName: "Counter", CreatedInRelease: "release"}}}
	b.OwnedResources["cloudflare-worker"], _ = json.Marshal(receipt)
	got := NativeProviderResourceBindings(b, conn)
	if len(got) != 1 || got[0].ExternalID != cfResourceID || got[0].Incarnation != cfResourceID || !got[0].OwnershipVerified {
		t.Fatalf("owned namespace absent: %#v", got)
	}
	receipt.Namespaces[0].CreatedInRelease = ""
	b.OwnedResources["cloudflare-worker"], _ = json.Marshal(receipt)
	if got = NativeProviderResourceBindings(b, conn); len(got) != 0 {
		t.Fatalf("namespace without creation proof claimed: %#v", got)
	}
	delete(b.OwnedResources, "cloudflare-worker")
	if got = NativeProviderResourceBindings(b, conn); len(got) != 0 {
		t.Fatalf("external reference claimed: %#v", got)
	}
}

func TestNativeProviderResourceBindingsVerifyVercelReceiptAndConfigDigest(t *testing.T) {
	b, conn, cfg := nativeUsageFixture(t, connections.Vercel, "vercel-global-config")
	b.ExternalID = "gcfg_native"
	b.PhysicalID = b.ExternalID
	receipt := vercelResourceReceipt{ResourceID: b.ExternalID, AccountID: "team_account", AccountIdentity: cfg.AccountIdentity, ConnectionID: conn.ID, OrgID: b.OrgID, ProjectScopeID: b.ProjectID, EnvironmentID: b.EnvironmentID, Kind: cfg.Kind, NativeType: "global-config", ConfigDigest: vercelResourceDigest(cfg, b.Name)}
	b.OwnedResources[vercelResourceOwnershipKey], _ = json.Marshal(receipt)
	got := NativeProviderResourceBindings(b, conn)
	if len(got) != 1 || got[0].ExternalID != "gcfg_native" || got[0].Incarnation != "gcfg_native" || !got[0].OwnershipVerified || got[0].Provider != connections.Vercel {
		t.Fatalf("Vercel owned projection %#v", got)
	}
	receipt.AccountID = "team_other"
	b.OwnedResources[vercelResourceOwnershipKey], _ = json.Marshal(receipt)
	if got = NativeProviderResourceBindings(b, conn); len(got) != 0 {
		t.Fatalf("Vercel wrong-account receipt claimed: %#v", got)
	}
}
