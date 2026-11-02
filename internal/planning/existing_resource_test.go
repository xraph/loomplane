package planning

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/providers"
)

const syncedResource = "providerresource_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func existingInput(t *testing.T) Input {
	t.Helper()
	return Input{Catalog: true, Environment: "production", Graph: &graph.Graph{}, Manifest: parse(t, "version: 1\nresources:\n  db: {kind: postgres, existing_resource: "+syncedResource+"}\n"), Providers: providers.Registry(), Connections: []ConnectionOption{{ID: "conn_do", Cloud: "digitalocean", Ready: true}}, ExistingResources: map[string]ExistingResourceCheck{syncedResource: {Resource: &providerresources.ProviderResource{ID: syncedResource, Provider: "digitalocean", Kind: "postgres", Name: "orders", ExternalID: "db-1", Incarnation: "created-1", Region: "nyc1", ConnectionID: "conn_do", AccountIdentity: "account", EndpointID: "endpoint-1", EndpointRevision: 1, Revision: 3}, Endpoint: &catalogconnect.Endpoint{ID: "endpoint-1", Engine: "postgres", Revision: 1, Host: "db.example", Port: 25060, TLS: "verify-full"}}}}
}

func TestExistingResourcePlansNonOwningAttachment(t *testing.T) {
	in := existingInput(t)
	got := Compute(in)
	if !got.Executable || len(got.Actions) != 1 || got.Actions[0].Resource.ExistingResource == nil {
		t.Fatalf("attachment not planned: %+v", got)
	}
	a := got.Actions[0]
	if a.ConnectionID != "conn_do" || a.Resource.Managed != nil || a.Resource.Catalog != nil || a.Resource.ExistingResource.ResourceID != syncedResource || !strings.Contains(a.Creates, "attachment") {
		t.Fatalf("attachment acquired native lifecycle: %+v", a)
	}
	in.Bindings = boundFrom(t, got)
	if next := Compute(in); !next.Executable || next.Actions[0].Op != Keep {
		t.Fatalf("unchanged attachment was not kept: %+v", next)
	}
	delete(in.Manifest.Resources, "db")
	deleted := Compute(in)
	if !deleted.Executable || len(deleted.Actions) != 1 || deleted.Actions[0].Op != Delete || !strings.Contains(deleted.Actions[0].Reason, "remains") || hasCode(deleted.Warnings, "data-loss") {
		t.Fatalf("detach treated as cloud deletion: %+v", deleted)
	}
}

func TestExistingResourceRejectsMissingIdentityAndOwnershipConversion(t *testing.T) {
	for _, broken := range []string{"not-found", "incarnation", "missing", "endpoint", "engine"} {
		t.Run(broken, func(t *testing.T) {
			in := existingInput(t)
			check := in.ExistingResources[syncedResource]
			switch broken {
			case "not-found":
				check.Resource = nil
			case "incarnation":
				check.Resource.Incarnation = ""
			case "missing":
				check.Resource.Status = "missing"
			case "endpoint":
				check.Endpoint = nil
			case "engine":
				check.Endpoint.Engine = "redis"
			}
			in.ExistingResources[syncedResource] = check
			if Compute(in).Executable {
				t.Fatal("unverified inventory resource was attachable")
			}
		})
	}
	in := existingInput(t)
	raw, err := json.Marshal(ResourceConfig{Provider: "digitalocean", Kind: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	in.Bindings = []*Binding{{Subject: ResourceSubject, Name: "db", Provider: "digitalocean", ConnectionID: "conn_do", ExternalID: "owned-db", Config: raw}}
	if Compute(in).Executable {
		t.Fatal("owned database silently became an external attachment")
	}
}

func TestExistingResourceConsumerNeedsNetworkProofAndEnvironmentOptIn(t *testing.T) {
	in := existingInput(t)
	in.Catalog = false
	in.Graph = infrastructureGraph()
	in.Connections = append(in.Connections, infrastructureConnections(t)...)
	in.Manifest = parse(t, "version: 1\nresources:\n  db: {kind: postgres, existing_resource: "+syncedResource+", environments: {production: {enabled: true}}}\nservices:\n  api: {source: cmd/api, provider: server, target: "+targetA+", endpoint: {host_port: 8080}, needs: [db]}\n")
	if result := Compute(in); result.Executable || !hasCode(result.Problems, "existing-resource-network") {
		t.Fatalf("consumer skipped target verification: %+v", result)
	}
	in.ExistingResourceChecks = map[string]EndpointCheck{"api:db": {ID: "endpoint-1", Found: true, Engine: "postgres", Report: &catalogconnect.Report{}}}
	result := Compute(in)
	if !result.Executable {
		t.Fatalf("verified consumer blocked: %+v", result.Problems)
	}
	var linked bool
	for _, a := range result.Actions {
		if a.Service != nil {
			for _, link := range a.Service.Env {
				if link.From == "db" {
					linked = true
				}
			}
		}
	}
	if !linked {
		t.Fatal("verified external connection was not linked to the application")
	}
	in.Environment = "staging"
	other := Compute(in)
	if !other.Executable {
		t.Fatalf("external need changed another environment: %+v", other.Problems)
	}
	for _, a := range other.Actions {
		if a.Resource != nil && a.Resource.ExistingResource != nil {
			t.Fatal("production inventory grant entered staging")
		}
		if a.Service != nil {
			for _, link := range a.Service.Env {
				if link.From == "db" {
					t.Fatal("production credential linked in staging")
				}
			}
		}
	}
}
