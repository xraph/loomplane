package providerresources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestAPIAccessConnectPinsObservedAccountResourceAndNativeDescriptor(t *testing.T) {
	for _, kind := range []string{"cloudflare-d1", "cloudflare-hyperdrive", "cloudflare-durable-object", "object-storage"} {
		t.Run(kind, func(t *testing.T) {
			s, c, reader, auth := fixture(t)
			id := strings.Repeat("a", 32)
			for _, row := range c.rows {
				row.Cloud = connections.Cloudflare
				row.Details = map[string]string{"account_id": id}
			}
			externalID := "resource-id"
			if kind == "object-storage" {
				externalID = "r2/eu/application-assets"
			}
			scope := "accounts/" + id + "/d1"
			if kind == "cloudflare-durable-object" {
				scope = "accounts/" + id + "/durable-objects/scripts/worker/classes/Counter"
			}
			if kind == "object-storage" {
				scope = "accounts/" + id + "/r2/eu"
			}
			reader.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
				return provider.ResourceInventory{Scope: scope, Complete: true, Resources: []provider.ResourceObservation{{ID: externalID, Incarnation: "created", Kind: kind, Name: "application-assets", Region: "auto", Status: "online"}}}, nil
			}
			secrets := endpointSecrets{}
			s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, secrets, auth)
			if _, err := s.Sync(t.Context(), principal, "one"); err != nil {
				t.Fatal(err)
			}
			list, err := s.List(t.Context(), principal)
			if err != nil {
				t.Fatal(err)
			}
			r := list.Resources[0]
			if r.APIAccountID != id {
				t.Fatal("API account not derived from verified scope")
			}
			access := catalogconnect.APIAccess{Provider: "cloudflare", Kind: kind, AccountID: id, ResourceID: externalID, Token: "application-token"}
			if kind == "object-storage" {
				access.Kind = "cloudflare-r2"
			}
			if kind != "cloudflare-d1" {
				access.Native = true
				access.Token = ""
			}
			wrong := access
			wrong.AccountID = strings.Repeat("b", 32)
			if _, err = s.Connect(t.Context(), principal, r.ID, ConnectInput{APIAccess: &wrong}); !errors.Is(err, ErrInvalid) || len(secrets) != 0 {
				t.Fatal("account substitution reached Vault")
			}
			got, err := s.Connect(t.Context(), principal, r.ID, ConnectInput{APIAccess: &access})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err = s.Resolve(t.Context(), principal, r.ID, got.Revision); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "application-token") {
				t.Fatal("credential in public inventory")
			}
		})
	}
}

type resourceAccessReader struct {
	*testReader
	calls int
	fail  bool
}

func (r *resourceAccessReader) ResolveResourceAccess(_ context.Context, _ *connections.Connection, _ []byte, selected *ProviderResource, project, environment string) (catalogconnect.APIAccess, error) {
	r.calls++
	if r.fail || project != "prj_application" || environment != "production" {
		return catalogconnect.APIAccess{}, ErrUnverified
	}
	return catalogconnect.APIAccess{Provider: "vercel", Kind: "vercel-integration", ProjectID: "prj_application", Environment: "production", AccountID: "team_application", ResourceID: selected.ExternalID, Values: map[string]string{"DATABASE_URL": "postgres://application:PRIVATE@database.example/app?sslmode=verify-full"}}, nil
}
func TestProviderCredentialBridgeRequiresExplicitScopeAndNeverAcceptsManualValues(t *testing.T) {
	s, c, reader, auth := fixture(t)
	for _, row := range c.rows {
		row.Cloud = connections.Vercel
		row.Details = map[string]string{"team_id": "team_application"}
	}
	reader.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "teams/team_application/integrations/neon/postgres/native", Complete: true, Resources: []provider.ResourceObservation{{ID: "store_native", Incarnation: "store_native", Kind: "vercel-integration", Name: "Production", Status: "ready"}}}, nil
	}
	bridge := &resourceAccessReader{testReader: reader}
	s.deps.Reader = bridge
	secrets := endpointSecrets{}
	s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, secrets, auth)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	id := list.Resources[0].ID
	forged := catalogconnect.APIAccess{Provider: "vercel", Kind: "vercel-integration", ProjectID: "prj_application", Environment: "production", AccountID: "team_application", ResourceID: "store_native", Values: map[string]string{"DATABASE_URL": "postgres://evil/"}}
	if _, err := s.Connect(t.Context(), principal, id, ConnectInput{APIAccess: &forged}); !errors.Is(err, ErrInvalid) || len(secrets) != 0 || bridge.calls != 0 {
		t.Fatal("manual integration values bypassed provider proof")
	}
	for _, in := range []ConnectInput{{ProviderProjectID: "prj_wrong", ProviderEnvironment: "production"}, {ProviderProjectID: "prj_application"}, {ProviderProjectID: "prj_application", ProviderEnvironment: "preview"}} {
		if _, err := s.Connect(t.Context(), principal, id, in); err == nil || len(secrets) != 0 {
			t.Fatal("wrong provider project/environment reached Vault")
		}
	}
	got, err := s.Connect(t.Context(), principal, id, ConnectInput{ProviderProjectID: "prj_application", ProviderEnvironment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatal("provider application access leaked")
	}
	auth.denied = true
	before := bridge.calls
	if _, err = s.Connect(t.Context(), principal, id, ConnectInput{ProviderProjectID: "prj_application", ProviderEnvironment: "production"}); err == nil || bridge.calls != before {
		t.Fatal("denied credential bridge reached provider")
	}
}

func TestChangedPublicEndpointPinBlocksVaultBeforeGlobalAndProjectResolution(t *testing.T) {
	for _, projectAccess := range []bool{false, true} {
		t.Run(fmt.Sprint(projectAccess), func(t *testing.T) {
			s, _, _, auth := fixture(t)
			rows := &endpointRows{rows: map[string]*catalogconnect.Endpoint{}}
			endpoints := &countingEndpoints{Endpoints: catalogconnect.NewService(rows, endpointSecrets{}, auth)}
			s.deps.Endpoints = endpoints
			_, _ = s.Sync(t.Context(), principal, "one")
			list, _ := s.List(t.Context(), principal)
			resource, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{DSN: "postgres://app:private@db.example/app?sslmode=require"})
			if err != nil {
				t.Fatal(err)
			}
			caller := principal
			if projectAccess {
				binding := ResourceBinding{ID: "attachment", ProjectID: "project", EnvironmentID: "production", Provider: resource.Provider, AccountIdentity: resource.AccountIdentity, ExternalID: resource.ExternalID, Incarnation: resource.Incarnation, InventoryResourceID: resource.ID, Revision: resource.Revision, EndpointID: resource.EndpointID, EndpointRevision: resource.EndpointRevision}
				calls := 0
				s.deps.Usage = func(context.Context, string) ([]ResourceBinding, error) {
					calls++
					if calls == 2 {
						rows.rows[resource.EndpointID].Revision++
					}
					return []ResourceBinding{binding}, nil
				}
				s.deps.Authorizer = projectOnlyAuth{}
				caller = security.Principal{OrgID: "org", UserID: "developer"}
				_, _, raw, resolveErr := s.ResolveProject(t.Context(), caller, "project", "production", resource.ID, resource.Revision)
				if !errors.Is(resolveErr, ErrChanged) || raw != "" || endpoints.resolves != 0 {
					t.Fatalf("stale project endpoint reached Vault: err=%v resolves=%d", resolveErr, endpoints.resolves)
				}
			} else {
				rows.rows[resource.EndpointID].Revision++
				_, _, raw, resolveErr := s.Resolve(t.Context(), caller, resource.ID, resource.Revision)
				if !errors.Is(resolveErr, ErrChanged) || raw != "" || endpoints.resolves != 0 {
					t.Fatalf("stale global endpoint reached Vault: err=%v resolves=%d", resolveErr, endpoints.resolves)
				}
			}
		})
	}
}
