package providerinventory

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providerresources"
)

func TestResolveResourceAccessPreservesIntegrationScopeAndSelectsMappedSecrets(t *testing.T) {
	for _, wrongScope := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongScope), func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer account-secret" {
					t.Error("credential bridge performed an unexpected provider operation")
				}
				switch r.URL.Path {
				case "/v2/teams/team_a":
					fmt.Fprint(w, `{"id":"team_a","name":"Production"}`)
				case "/v1/storage/stores/store_db":
					if r.URL.Query().Get("teamId") != "team_a" {
						t.Error("resource request lost verified team")
					}
					fmt.Fprint(w, `{"store":{"id":"store_db","name":"Database","type":"integration","ownerId":"team_a","externalResourceId":"project-neon","product":{"id":"prod_neon","slug":"postgres","integrationConfigurationId":"icfg_neon","integration":{"slug":"neon"}},"projectsMetadata":[{"projectId":"prj_app","environments":["production"],"environmentVariables":["DATABASE_URL"]}]}}`)
				case "/v10/projects/prj_app/env":
					if r.URL.Query().Get("target") != "production" || r.URL.Query().Get("decrypt") != "true" {
						t.Error("resource request lost project environment")
					}
					fmt.Fprint(w, `{"envs":[{"key":"DATABASE_URL","value":"postgres://app:application-secret@db.example/app?sslmode=require","target":["production"]},{"key":"OTHER_SECRET","value":"unrelated-secret"}]}`)
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.Error(w, "not found", 404)
				}
			}))
			defer source.Close()
			client := &Clients{Vercel: vercel.New(source.URL)}
			connection := &connections.Connection{ID: "conn", OrgID: "org", Cloud: connections.Vercel, Generation: 1, Status: connections.StatusReady, Details: map[string]string{"team_id": "team_a"}}
			identity := connections.AccountIdentity(connection.Cloud, "", connection.Details)
			selected := &providerresources.ProviderResource{ID: "resource", OrgID: "org", ConnectionID: "conn", ConnectionGeneration: 1, AccountIdentity: identity, Provider: "vercel", Kind: "vercel-integration", ExternalID: "store_db", Incarnation: "store_db", Scope: "teams/team_a/integrations/neon/postgres/project-neon"}
			if wrongScope {
				selected.Scope = "teams/team_a/integrations/another/postgres/project-neon"
			}
			got, err := client.ResolveResourceAccess(t.Context(), connection, []byte("account-secret"), selected, "prj_app", "production")
			if wrongScope {
				if err == nil {
					t.Fatal("backing integration substitution survived")
				}
				return
			}
			if err != nil || len(got.Values) != 1 || !strings.Contains(got.Values["DATABASE_URL"], "application-secret") {
				t.Fatal("selected mapped application access missing")
			}
		})
	}
}
