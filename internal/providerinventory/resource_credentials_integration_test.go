package providerinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

type credentialBridgeAuth struct{}

func (credentialBridgeAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type credentialBridgeConnections struct{ connection *connections.Connection }

func (c credentialBridgeConnections) All(context.Context, string) ([]*connections.Connection, error) {
	v := *c.connection
	return []*connections.Connection{&v}, nil
}
func (c credentialBridgeConnections) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	v := *c.connection
	return &v, []byte("account-secret"), nil
}

type credentialBridgeInventory struct {
	state providerresources.AccountState
}

func (s *credentialBridgeInventory) Update(_ context.Context, _, _, _, _ string, _ int64, fn func(*providerresources.AccountState) error) error {
	return fn(&s.state)
}
func (s *credentialBridgeInventory) List(context.Context, string) (*providerresources.ProviderResourceList, error) {
	raw, _ := json.Marshal(s.state)
	var state providerresources.AccountState
	_ = json.Unmarshal(raw, &state)
	return &providerresources.ProviderResourceList{Resources: state.Resources, Syncs: state.Syncs}, nil
}
func (s *credentialBridgeInventory) Get(ctx context.Context, org, id string) (*providerresources.ProviderResource, error) {
	list, _ := s.List(ctx, org)
	for _, row := range list.Resources {
		if row.ID == id && row.OrgID == org {
			return row, nil
		}
	}
	return nil, providerresources.ErrNotFound
}

type credentialBridgeEndpoints map[string]*catalogconnect.Endpoint

func (s credentialBridgeEndpoints) Create(_ context.Context, row *catalogconnect.Endpoint) error {
	v := *row
	s[row.ID] = &v
	return nil
}
func (s credentialBridgeEndpoints) Get(_ context.Context, org, id string) (*catalogconnect.Endpoint, error) {
	if row := s[id]; row != nil && row.OrgID == org {
		v := *row
		return &v, nil
	}
	return nil, catalogconnect.ErrNotFound
}
func (s credentialBridgeEndpoints) List(context.Context, string) ([]*catalogconnect.Endpoint, error) {
	return nil, nil
}
func (s credentialBridgeEndpoints) Delete(_ context.Context, _, id string) error {
	delete(s, id)
	return nil
}

type credentialBridgeSecrets map[string][]byte

func (s credentialBridgeSecrets) Put(_ context.Context, key string, value []byte) error {
	s[key] = append([]byte(nil), value...)
	return nil
}
func (s credentialBridgeSecrets) Get(_ context.Context, key string) ([]byte, error) {
	return append([]byte(nil), s[key]...), nil
}
func (s credentialBridgeSecrets) Delete(_ context.Context, key string) error {
	delete(s, key)
	return nil
}

func TestMarketplaceCredentialBridgeConnectsRealReaderAndService(t *testing.T) {
	const store = `{"id":"store_db","name":"Database","type":"integration","ownerId":"team_a","externalResourceId":"project-neon","product":{"id":"prod_neon","slug":"postgres","integrationConfigurationId":"icfg_neon","integration":{"slug":"neon"}},"projectsMetadata":[{"projectId":"prj_app","environments":["production"],"environmentVariables":["DATABASE_URL"]}]}`
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer account-secret" {
			t.Error("credential bridge changed provider state or lost authorization")
		}
		switch r.URL.Path {
		case "/v2/teams/team_a":
			fmt.Fprint(w, `{"id":"team_a","name":"Production"}`)
		case "/v1/global-config":
			fmt.Fprint(w, `[]`)
		case "/v1/storage/stores":
			fmt.Fprintf(w, `{"stores":[%s]}`, store)
		case "/v1/storage/stores/store_db":
			fmt.Fprintf(w, `{"store":%s}`, store)
		case "/v10/projects/prj_app/env":
			if r.URL.Query().Get("target") != "production" || r.URL.Query().Get("decrypt") != "true" {
				t.Error("credential bridge lost the requested source environment")
			}
			fmt.Fprint(w, `{"envs":[{"key":"DATABASE_URL","value":"postgres://app:application-secret@db.example/app?sslmode=require","target":["production"]},{"key":"UNRELATED_TOKEN","value":"unrelated-secret"}]}`)
		default:
			t.Errorf("unexpected provider request %s", r.URL)
			http.Error(w, "not found", 404)
		}
	}))
	defer source.Close()
	conn := &connections.Connection{ID: "conn", OrgID: "org", Cloud: connections.Vercel, Generation: 1, Status: connections.StatusReady, Details: map[string]string{"team_id": "team_a"}}
	secrets := credentialBridgeSecrets{}
	endpoints := catalogconnect.NewService(credentialBridgeEndpoints{}, secrets, credentialBridgeAuth{})
	service := providerresources.NewService(providerresources.Deps{Store: &credentialBridgeInventory{}, Connections: credentialBridgeConnections{conn}, Reader: &Clients{Vercel: vercel.New(source.URL)}, Endpoints: endpoints, Authorizer: credentialBridgeAuth{}})
	principal := security.Principal{OrgID: "org", UserID: "admin"}
	if _, err := service.Sync(t.Context(), principal, conn.ID); err != nil {
		t.Fatal(err)
	}
	inventory, err := service.List(t.Context(), principal)
	if err != nil || len(inventory.Resources) != 1 {
		t.Fatalf("inventory failed: %v", err)
	}
	connected, err := service.Connect(t.Context(), principal, inventory.Resources[0].ID, providerresources.ConnectInput{ProviderProjectID: "prj_app", ProviderEnvironment: "production"})
	if err != nil {
		t.Fatalf("actual reader-to-service credential bridge failed: %v", err)
	}
	_, endpoint, raw, err := service.Resolve(t.Context(), principal, connected.ID, connected.Revision)
	if err != nil {
		t.Fatal(err)
	}
	access, err := catalogconnect.ParseAPIAccess(raw)
	if err != nil || access.ProjectID != "prj_app" || access.Environment != "production" || len(access.Values) != 1 || !strings.Contains(access.Values["DATABASE_URL"], "application-secret") || catalogconnect.APIEndpointBridge(endpoint) != "prj_app" {
		t.Fatal("verified source or mapped application secret was lost")
	}
	public, _ := json.Marshal(connected)
	if strings.Contains(string(public), "secret") || strings.Contains(string(public), "DATABASE_URL") {
		t.Fatal("public inventory disclosed application access")
	}
}
