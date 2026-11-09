package vercel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func resourceClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("teamId") != "team_a" || r.Header.Get("Authorization") != "Bearer admin-secret" {
			t.Errorf("lost resource account scope")
		}
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return New(s.URL)
}

var resourceToken = Token{Value: "admin-secret", TeamID: "team_a"}

func TestResourceInventoryPreservesProviderIdentityAndExcludesCredentials(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/storage/stores" || r.Method != http.MethodGet {
			t.Fatalf("wrong inventory endpoint %s", r.URL)
		}
		fmt.Fprint(w, `{"stores":[{"id":"store_db","name":"database","type":"integration","ownerId":"team_a","externalResourceId":"neon-project","metadata":{"password":"private"},"defaultToken":"private","product":{"id":"prod_neon","slug":"neon-postgres","integrationConfigurationId":"icfg_neon","integration":{"id":"int_neon","slug":"neon","name":"Neon"}}}]}`)
	})
	resources, err := c.ListStorageResources(context.Background(), resourceToken)
	if err != nil || len(resources) != 1 {
		t.Fatalf("inventory: %v %v", resources, err)
	}
	got := resources[0]
	if got.Product.Integration.Slug != "neon" || got.ExternalResourceID != "neon-project" || got.Product.IntegrationConfigurationID != "icfg_neon" {
		t.Fatalf("provider identity lost: %+v", got)
	}
	b, _ := json.Marshal(resources)
	if strings.Contains(string(b), "private") {
		t.Fatal("inventory included credentials")
	}
}

func TestResourceCreateUsesConsumerAPIAndExplicitBlobAccess(t *testing.T) {
	for _, tc := range []struct{ typ, path, body, response string }{
		{"blob", "/v1/storage/stores/blob", `{"name":"assets","region":"iad1","access":"private"}`, `{"store":{"id":"store_blob","name":"assets","type":"blob","ownerId":"team_a"}}`},
		{"integration", "/v1/storage/stores/integration/direct", `{"name":"assets","integrationConfigurationId":"icfg_neon","integrationProductIdOrSlug":"neon-postgres","metadata":{"region":"aws-us-east-1"},"externalId":"lp-stable","billingPlanId":"free"}`, `{"store":{"id":"store_neon","name":"assets","type":"integration","ownerId":"team_a"}}`},
		{"global-config", "/v1/global-config", `{"slug":"assets"}`, `{"id":"ecfg_config","slug":"assets","ownerId":"team_a"}`},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != tc.path {
					t.Fatalf("wrong create endpoint: %s %s", r.Method, r.URL)
				}
				var got, want map[string]any
				_ = json.NewDecoder(r.Body).Decode(&got)
				_ = json.Unmarshal([]byte(tc.body), &want)
				gb, _ := json.Marshal(got)
				wb, _ := json.Marshal(want)
				if string(gb) != string(wb) {
					t.Errorf("create body %s, want %s", gb, wb)
				}
				fmt.Fprint(w, tc.response)
			})
			opts := CreateResourceOptions{Name: "assets", Region: "iad1", Access: "private", IntegrationConfigurationID: "icfg_neon", IntegrationProductIDOrSlug: "neon-postgres", Metadata: map[string]any{"region": "aws-us-east-1"}, ExternalID: "lp-stable", BillingPlanID: "free"}
			var got Resource
			var err error
			switch tc.typ {
			case "blob":
				got, err = c.CreateBlobStore(context.Background(), resourceToken, opts)
			case "integration":
				got, err = c.CreateIntegrationStore(context.Background(), resourceToken, opts)
			default:
				got, err = c.CreateGlobalConfig(context.Background(), resourceToken, opts)
			}
			if err != nil || got.ID == "" || got.Name != "assets" {
				t.Fatalf("create: %+v %v", got, err)
			}
		})
	}
}

func TestResourceInventoryFailsClosedOnScopeMismatchOrPagination(t *testing.T) {
	for _, body := range []string{
		`{"stores":[{"id":"store_other","name":"other","ownerId":"team_b","type":"blob"}]}`,
		`{"stores":[],"pagination":{"next":123}}`,
		`{"stores":[],"cursor":"next-page"}`,
		`{"stores":[{"name":"missing id","type":"blob"}]}`,
	} {
		c := resourceClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
		if _, err := c.ListStorageResources(context.Background(), resourceToken); err == nil {
			t.Fatalf("unsafe inventory accepted: %s", body)
		}
	}
}

func TestResourceCredentialsSelectOnlyConnectedResourceAndEnvironment(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/storage/stores/store_blob":
			fmt.Fprint(w, `{"store":{"id":"store_blob","type":"blob","name":"assets","ownerId":"team_a","projectsMetadata":[{"projectId":"prj_app","environments":["production"],"environmentVariables":["BLOB_READ_WRITE_TOKEN"]}]}}`)
		case "/v10/projects/prj_app/env":
			if r.URL.Query().Get("decrypt") != "true" || r.URL.Query().Get("target") != "production" {
				t.Error("missing scoped decrypt query")
			}
			fmt.Fprint(w, `{"envs":[{"id":"env_blob","key":"BLOB_READ_WRITE_TOKEN","value":"vercel_blob_rw_blob_application-secret","target":["production"]},{"id":"env_other","key":"DATABASE_URL","value":"unrelated-secret"}]}`)
		default:
			t.Fatalf("unexpected credential request %s", r.URL)
		}
	})
	got, err := c.ReadStorageResourceCredentials(context.Background(), resourceToken, "store_blob", "prj_app", "production")
	if err != nil || len(got.Values) != 1 || got.Values["BLOB_READ_WRITE_TOKEN"] != "vercel_blob_rw_blob_application-secret" {
		t.Fatalf("resource credentials: %v", err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "secret") || strings.Contains(fmt.Sprintf("%+v %#v", got, got), "secret") {
		t.Fatal("credential result reveals secrets")
	}
}

func TestResourceBlobApplicationTokenMustMatchItsStore(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/storage/stores/store_blob" {
			fmt.Fprint(w, `{"store":{"id":"store_blob","type":"blob","projectsMetadata":[{"projectId":"prj_app","environments":["production"],"environmentVariables":["BLOB_READ_WRITE_TOKEN"]}]}}`)
			return
		}
		fmt.Fprint(w, `{"envs":[{"key":"BLOB_READ_WRITE_TOKEN","value":"vercel_blob_rw_other_secret","target":["production"]}]}`)
	})
	if _, err := c.ReadStorageResourceCredentials(context.Background(), resourceToken, "store_blob", "prj_app", "production"); err == nil {
		t.Fatal("another blob store's token was accepted")
	}
}

func TestResourceScopedGlobalConfigReadToken(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/global-config/ecfg_config/token" {
			t.Fatalf("wrong read token endpoint: %s", r.URL)
		}
		fmt.Fprint(w, `{"id":"token_read","token":"read-secret"}`)
	})
	got, err := c.CreateGlobalConfigReadToken(context.Background(), resourceToken, "ecfg_config", "loomplane")
	if err != nil || got.TokenID != "token_read" || got.Values["GLOBAL_CONFIG"] != "https://global-config.vercel.com/ecfg_config?token=read-secret" {
		t.Fatalf("scoped read token: %v", err)
	}
}

func TestResourceLifecycleDeleteAndConnectUseTypeSpecificPaths(t *testing.T) {
	var calls []string
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == "POST" {
			var body struct {
				ProjectID    string   `json:"projectId"`
				Environments []string `json:"envVarEnvironments"`
				Type         string   `json:"type"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.ProjectID != "prj_app" || body.Type != "integration" || len(body.Environments) != 1 || body.Environments[0] != "production" {
				t.Errorf("wrong connection body: %+v", body)
			}
		}
		fmt.Fprint(w, `{}`)
	})
	ctx := context.Background()
	if err := c.ConnectStorageResource(ctx, resourceToken, "store_blob", ConnectResourceOptions{ProjectID: "prj_app", Environments: []string{"production"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.DisconnectStorageResource(ctx, resourceToken, "store_blob", "spc_owned"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteBlobStore(ctx, resourceToken, "store_blob"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteIntegrationStore(ctx, resourceToken, "store_neon"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteGlobalConfig(ctx, resourceToken, "ecfg_config"); err != nil {
		t.Fatal(err)
	}
	want := "POST /v1/storage/stores/store_blob/connections,DELETE /v1/storage/stores/store_blob/connections/spc_owned,DELETE /v1/storage/stores/blob/store_blob,DELETE /v1/storage/stores/integration/store_neon,DELETE /v1/global-config/ecfg_config"
	if strings.Join(calls, ",") != want {
		t.Fatalf("lifecycle calls: %v", calls)
	}
}

func TestResourceRuntimeIsolationRefusesMatchingProjectVariable(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"envs":[{"key":"DATABASE_URL","target":["production"]}]}`)
	})
	if err := c.VerifyRuntimeResourceIsolation(context.Background(), resourceToken, "prj_app", Production, []string{"DATABASE_URL"}); err == nil {
		t.Fatal("resource credential already persisted in project settings was accepted")
	}
}

func TestResourceAdminCredentialNeverFollowsRedirects(t *testing.T) {
	for _, action := range []string{"inventory", "account"} {
		t.Run(action, func(t *testing.T) {
			visited := false
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { visited = true; fmt.Fprint(w, `{}`) }))
			defer sink.Close()
			redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
			}))
			defer redirector.Close()
			c := New(redirector.URL)
			var err error
			if action == "inventory" {
				_, err = c.ListStorageResources(context.Background(), resourceToken)
			} else {
				_, err = c.Check(context.Background(), resourceToken)
			}
			if visited || err == nil {
				t.Fatal("administrative API request followed an untrusted redirect")
			}
		})
	}
}

func TestResourceCredentialsRejectWrongCustomEnvironmentAndRedactedValues(t *testing.T) {
	for _, env := range []string{
		`{"id":"env_db","key":"DATABASE_URL","value":"private","customEnvironmentIds":["env_other"]}`,
		`{"id":"env_db","key":"DATABASE_URL","value":"","customEnvironmentIds":["env_staging"]}`,
		`{"id":"env_db","key":"DATABASE_URL","value":"admin-secret","customEnvironmentIds":["env_staging"]}`,
	} {
		c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/storage/stores/store_db" {
				fmt.Fprint(w, `{"store":{"id":"store_db","type":"integration","projectsMetadata":[{"projectId":"prj_app","environments":["env_staging"],"environmentVariables":["DATABASE_URL"]}]}}`)
				return
			}
			if r.URL.Query().Get("customEnvironmentId") != "env_staging" || r.URL.Query().Get("target") != "" {
				t.Fatal("custom environment scope was lost")
			}
			fmt.Fprint(w, `{"envs":[`+env+`]}`)
		})
		if _, err := c.ReadStorageResourceCredentials(context.Background(), resourceToken, "store_db", "prj_app", "env_staging"); err == nil {
			t.Fatalf("unsafe credential response accepted: %s", env)
		}
	}
}

func TestResourceGlobalConfigMetadataAndTokenRevocation(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/global-config":
			fmt.Fprint(w, `[{"id":"ecfg_config","slug":"flags","ownerId":"team_a","tokens":[{"token":"private"}],"items":{"password":"private"}}]`)
		case "GET /v1/global-config/ecfg_config":
			fmt.Fprint(w, `{"id":"ecfg_config","slug":"flags","ownerId":"team_a"}`)
		case "DELETE /v1/global-config/ecfg_config/tokens":
			var body struct {
				IDs []string `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.IDs) != 1 || body.IDs[0] != "token_created" {
				t.Fatalf("revoked wrong token: %+v", body)
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Fatalf("unexpected API request: %s %s", r.Method, r.URL)
		}
	})
	ctx := context.Background()
	listed, err := c.ListGlobalConfigs(ctx, resourceToken)
	if err != nil || len(listed) != 1 || listed[0].Type != "global-config" || listed[0].Name != "flags" {
		t.Fatalf("global config metadata: %+v %v", listed, err)
	}
	b, _ := json.Marshal(listed)
	if strings.Contains(string(b), "private") {
		t.Fatal("global config inventory leaked application data")
	}
	got, err := c.GetGlobalConfig(ctx, resourceToken, "ecfg_config")
	if err != nil || got.ID != "ecfg_config" {
		t.Fatalf("global config lookup: %+v %v", got, err)
	}
	if err := c.RevokeGlobalConfigReadToken(ctx, resourceToken, "ecfg_config", "token_created"); err != nil {
		t.Fatal(err)
	}
}

func TestResourceErrorsDoNotRevealUpstreamSecrets(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"credential blob-secret was rejected"}}`)
	})
	_, err := c.ListStorageResources(context.Background(), resourceToken)
	if err == nil || strings.Contains(err.Error(), "blob-secret") {
		t.Fatalf("unsafe resource error: %v", err)
	}
}

func TestResourceCredentialsRejectAmbiguousVariableNames(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/storage/stores/store_db" {
			fmt.Fprint(w, `{"store":{"id":"store_db","type":"integration","projectsMetadata":[{"projectId":"prj_app","environments":["production"],"environmentVariables":["DATABASE_URL"]}]}}`)
			return
		}
		fmt.Fprint(w, `{"envs":[{"id":"env_a","key":"DATABASE_URL","value":"first","target":["production"]},{"id":"env_b","key":"DATABASE_URL","value":"second","target":["production"]}]}`)
	})
	if _, err := c.ReadStorageResourceCredentials(context.Background(), resourceToken, "store_db", "prj_app", "production"); err == nil {
		t.Fatal("ambiguous resource credential was accepted")
	}
}

func TestResourceProjectRejectsMismatchedOwner(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"prj_bridge","accountId":"team_other"}`)
	})
	if err := c.VerifyResourceProject(context.Background(), resourceToken, "prj_bridge", "team_a"); err == nil {
		t.Fatal("resource credential bridge accepted another account's project")
	}
}

func TestResourceDeploymentUsesSeparateRuntimeAndBuildEnvironment(t *testing.T) {
	c := resourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v13/deployments" {
			t.Fatalf("unexpected deployment request %s", r.URL)
		}
		var body struct {
			Env   map[string]string `json:"env"`
			Build struct {
				Env map[string]string `json:"env"`
			} `json:"build"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Env["DATABASE_URL"] != "app-secret" || body.Build.Env["DATABASE_URL"] != "" || body.Build.Env["NPM_TOKEN"] != "build-secret" {
			t.Fatal("runtime resource access crossed the build boundary")
		}
		fmt.Fprint(w, `{"id":"dpl_app"}`)
	})
	_, err := c.CreateDeploymentWithRuntimeEnv(context.Background(), resourceToken, &Project{ID: "prj_app", Name: "app"}, Production, nil, nil, map[string]string{"DATABASE_URL": "app-secret"}, map[string]string{"NPM_TOKEN": "build-secret"})
	if err != nil {
		t.Fatal(err)
	}
}
