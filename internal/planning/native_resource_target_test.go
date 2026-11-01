package planning

import (
	"maps"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providers"
)

func TestNativeDigitalOceanTargetAllowsTokenRotationWithinReviewedTeam(t *testing.T) {
	before := map[string]string{"team_uuid": "reviewed-team", "account_uuid": "first-member"}
	after := map[string]string{"team_uuid": "reviewed-team", "account_uuid": "second-member"}
	if connections.AccountIdentity(connections.DigitalOcean, "", before) != connections.AccountIdentity(connections.DigitalOcean, "", after) {
		t.Fatal("fixture changed deployment account")
	}
	if !maps.Equal(ReviewedConnectionSettings(connections.DigitalOcean, before), ReviewedConnectionSettings(connections.DigitalOcean, after)) {
		t.Fatal("same-team credential rotation changed the reviewed deployment destination")
	}
}

func TestAppPlatformRollbackRefusesHistoricalExternalCredentials(t *testing.T) {
	commit := strings.Repeat("a", 40)
	a := Action{Name: "api", Provider: "digitalocean", Op: Update, Service: &ServiceConfig{CommitSHA: commit, TargetAccountIdentity: "reviewed-account"}}
	choice, artifact, finding := appPlatformRollback(a, ReleasedImage{Service: "api", Provider: "digitalocean", Revision: "historical-deployment", CommitSHA: commit}, "previous")
	if finding == nil || artifact != nil || choice.Reused || !strings.Contains(finding.Message, "external") {
		t.Fatal("historical rollback could restore unchecked external credentials")
	}
}

func TestExternalDatabasePlansNativeRuntimeDeliveryWithCheckScope(t *testing.T) {
	for _, tc := range []struct{ cloud, provider string }{{"digitalocean", "digitalocean"}, {"azure", "azure-container-apps"}} {
		t.Run(tc.cloud, func(t *testing.T) {
			in := existingInput(t)
			in.Catalog = false
			in.Graph = infrastructureGraph()
			in.Manifest = parse(t, "version: 1\nresources:\n  db: {kind: postgres, existing_resource: "+syncedResource+"}\nservices:\n  api: {source: cmd/api, provider: "+tc.provider+", needs: [db]}\n")
			in.Connections = append(in.Connections, ConnectionOption{ID: "native", Cloud: tc.cloud, Ready: true, Details: map[string]string{"account_uuid": "target-account", "tenant_id": "tenant", "subscription_id": "sub", "resource_group": "prod"}})
			in.Chosen = map[string]string{tc.cloud: "native"}
			in.ExistingResourceChecks = map[string]EndpointCheck{"api:db": {ID: "endpoint-1", Engine: "postgres", Found: true, Report: &catalogconnect.Report{}}}
			result := Compute(in)
			if !result.Executable {
				t.Fatalf("native external DB blocked: %+v", result.Problems)
			}
			var linked, noted bool
			for _, action := range result.Actions {
				if action.Service == nil {
					continue
				}
				if action.ConnectionID != "native" || action.Service.TargetAccountIdentity == "" {
					t.Fatal("native target not pinned")
				}
				for _, link := range action.Service.Env {
					if link.From == "db" && link.Key == "DATABASE_URL" {
						linked = true
					}
				}
			}
			for _, warning := range result.Warnings {
				if warning.Code == "external-resource-check-scope" && strings.Contains(warning.Message, "from Loomplane") {
					noted = true
				}
			}
			if !linked || !noted {
				t.Fatalf("missing native access or scope warning: links=%v scope=%v", linked, noted)
			}
		})
	}
}

func TestExternalNativeTargetPinsCheckedAccountAndPlacement(t *testing.T) {
	for _, tc := range []struct{ cloud, provider string }{{"digitalocean", "digitalocean"}, {"azure", "azure-container-apps"}} {
		t.Run(tc.cloud, func(t *testing.T) {
			details := map[string]string{"account_uuid": "team-a", "tenant_id": "tenant-a", "subscription_id": "sub-a", "resource_group": "production", "registry_name": "production-registry"}
			p := &planner{in: Input{Providers: providers.Registry(), Chosen: map[string]string{tc.cloud: "selected"}, Connections: []ConnectionOption{{ID: "selected", Cloud: tc.cloud, Ready: true, Details: details}}}}
			a := &Action{Provider: tc.provider, Service: &ServiceConfig{Provider: tc.provider}}
			p.pinExternalResourceTarget("api", a, "services.api.needs")
			if len(p.problems) != 0 || a.ConnectionID != "selected" || a.Service.TargetAccountIdentity != connections.AccountIdentity(tc.cloud, "", details) || a.Service.TargetAccountIdentity == "" {
				t.Fatalf("external credentials had no reviewed target account: action=%+v problems=%+v", a, p.problems)
			}
			if tc.cloud == "azure" && a.Service.TargetSettings["resource_group"] != "production" {
				t.Fatal("Azure resource group was not pinned")
			}
			p.in.Connections[0].Ready = false
			p.pinExternalResourceTarget("api", &Action{Provider: tc.provider, Service: &ServiceConfig{}}, "services.api.needs")
			if len(p.problems) == 0 {
				t.Fatal("unchecked native target accepted resource access")
			}
		})
	}
}
