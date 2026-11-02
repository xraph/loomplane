//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestCloudflareR2OAuthCreationSyncAndAttachment(t *testing.T) {
	const account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const token = "cloudflare-oauth-fixture-token"
	const incarnation = "2026-11-01T12:00:00Z"
	const imported = "existing-assets"
	var mu sync.Mutex
	buckets := map[string]string{imported: incarnation}
	mutations := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth2/token" {
			id, secret, ok := r.BasicAuth()
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			grant := r.PostForm.Get("grant_type")
			validGrant := grant == "authorization_code" && r.PostForm.Get("code") == "approved" && r.PostForm.Get("code_verifier") != "" || grant == "refresh_token" && r.PostForm.Get("refresh_token") == "r2-refresh-secret"
			if !ok || id != "r2-app" || secret != "r2-app-secret" || !validGrant {
				t.Error("OAuth exchange lost app authentication or PKCE")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"r2-refresh-secret","expires_in":3600,"token_type":"Bearer"}`, token)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("resource request did not use the connected OAuth grant")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		root := "/accounts/" + account
		if r.Method == http.MethodGet && r.URL.Path == root {
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"name":"R2 account"}}`, account)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, root)
		mu.Lock()
		defer mu.Unlock()
		if path == "/r2/buckets" {
			jurisdiction := r.Header.Get("cf-r2-jurisdiction")
			if r.Method == http.MethodGet {
				rows := []map[string]string{}
				if jurisdiction == "" || jurisdiction == "default" {
					for name, created := range buckets {
						rows = append(rows, map[string]string{"name": name, "creation_date": created})
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"buckets": rows}})
				return
			}
			if r.Method == http.MethodPost {
				var in struct {
					Name string `json:"name"`
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" || buckets[in.Name] != "" || jurisdiction != "" && jurisdiction != "default" {
					t.Error("bucket creation lost its reviewed name or jurisdiction")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mutations++
				buckets[in.Name] = incarnation
				fmt.Fprintf(w, `{"success":true,"result":{"name":%q,"creation_date":%q}}`, in.Name, incarnation)
				return
			}
		}
		if r.Method == http.MethodGet && strings.HasPrefix(path, "/r2/buckets/") {
			name := strings.TrimPrefix(path, "/r2/buckets/")
			if buckets[name] != "" {
				fmt.Fprintf(w, `{"success":true,"result":{"name":%q,"creation_date":%q}}`, name, buckets[name])
				return
			}
		}
		switch path {
		case "/d1/database", "/storage/kv/namespaces", "/queues", "/vectorize/v2/indexes", "/hyperdrive/configs", "/workers/durable_objects/namespaces":
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"R2-only grant"}]}`))
				return
			}
		}
		t.Errorf("unexpected provider request: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.CloudflareEndpoint = api.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	running := start(t, cfg)
	defer func() { running.stop() }()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	c.must(http.StatusOK, http.MethodPut, "/api/v1/cloud-sign-in/apps/cloudflare", connections.AppInput{ClientID: "r2-app", ClientSecret: "r2-app-secret"}, nil)
	var signIn connections.SignIn
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/sign-ins", connections.SignInInput{Cloud: connections.Cloudflare, Name: "R2 storage", Settings: map[string]string{"account_id": account, "services": "r2"}}, &signIn)
	authorize, err := url.Parse(signIn.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	q := authorize.Query()
	if q.Get("scope") != "account-settings.read workers-r2.write offline_access" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("unexpected consent request: %s", authorize)
	}
	var connected connections.Connection
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections/sign-ins/complete", connections.CompleteInput{State: q.Get("state"), Code: "approved"}, &connected)
	if connected.Status != connections.StatusReady || connected.Method != connections.MethodOAuth || connected.AccountIdentity == "" {
		t.Fatalf("R2 OAuth connection is not ready: %+v", connected)
	}
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "R2 verification", "access": "write"}, &key)
	sdk := lpclient.New(running.srv.URL, key.Secret)
	ctx, cancel := context.WithTimeout(t.Context(), 2*readyTimeout)
	defer cancel()
	createPlan := func(name, manifest string) *planning.Plan {
		t.Helper()
		project, e := sdk.CreateProject(ctx, lpclient.NewProject{Name: name})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = sdk.WaitProjectReady(ctx, project.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = sdk.SaveManifest(ctx, project.ID, manifest); e != nil {
			t.Fatal(e)
		}
		p, e := sdk.CreatePlan(ctx, project.ID, lpclient.NewPlan{Environment: "production", Catalog: true})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = sdk.WaitPlan(ctx, p.ID); e != nil {
			t.Fatal(e)
		}
		var reviewed planning.Plan
		c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+p.ID, nil, &reviewed)
		if !reviewed.Executable || len(reviewed.Actions) != 1 {
			t.Fatalf("R2 review is not executable: %+v", reviewed)
		}
		return &reviewed
	}
	approve := func(p *planning.Plan) {
		t.Helper()
		if _, e := sdk.Approve(ctx, p.ID, p.Revision, ""); e != nil {
			t.Fatal(e)
		}
		d, e := sdk.DeploymentForPlan(ctx, p.ID)
		for errors.Is(e, lpclient.ErrNotFound) {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
			d, e = sdk.DeploymentForPlan(ctx, p.ID)
		}
		if e != nil {
			t.Fatal(e)
		}
		d, e = sdk.WaitDeployment(ctx, d.ID, nil)
		if e != nil || d.Status != "succeeded" {
			t.Fatalf("R2 deployment failed: %+v %v", d, e)
		}
	}
	plan := createPlan("Managed R2", fmt.Sprintf("version: 1\nresources:\n  assets:\n    kind: object-storage\n    provider: cloudflare-workers\n    target: %s\n", connected.ID))
	mu.Lock()
	before := mutations
	mu.Unlock()
	if before != 0 || plan.Actions[0].ConnectionID != connected.ID {
		t.Fatal("planning mutated the provider or lost the reviewed account")
	}
	approve(plan)
	mu.Lock()
	after := mutations
	mu.Unlock()
	if after != 1 {
		t.Fatalf("expected one approved bucket creation, got %d", after)
	}
	var managed struct {
		Bindings []planning.Binding `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+plan.ProjectID+"/bindings?environment=production", nil, &managed)
	if len(managed.Bindings) != 1 || managed.Bindings[0].ExternalID == "" || managed.Bindings[0].PhysicalID == "" {
		t.Fatal("created R2 bucket has no persisted physical binding")
	}
	var sync providerresources.ProviderResourceSync
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/"+connected.ID+"/resource-syncs", nil, &sync)
	if sync.Status != "complete" && sync.Status != "partial" {
		t.Fatalf("R2 inventory sync failed: %+v", sync)
	}
	var list providerresources.ProviderResourceList
	c.must(http.StatusOK, http.MethodGet, "/api/v1/provider-resources", nil, &list)
	var external *providerresources.ProviderResource
	var owned *providerresources.ProviderResource
	for _, resource := range list.Resources {
		if resource.Name == imported && resource.Kind == "object-storage" {
			external = resource
		}
		if resource.Name == managed.Bindings[0].ExternalID && resource.Kind == "object-storage" {
			owned = resource
		}
	}
	if owned == nil || owned.Ownership != "loomplane" || owned.OwnershipState != "verified" || len(owned.Consumers) != 1 || owned.Consumers[0].ProjectID != plan.ProjectID {
		t.Fatalf("created R2 ownership receipt was not projected into inventory: %+v", owned)
	}
	if external == nil || external.Incarnation != incarnation || external.APIAccountID != account || external.ConnectionID != connected.ID {
		t.Fatalf("R2 inventory lost its physical/account identity: %+v", external)
	}
	var access providerresources.ProviderResource
	c.must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/provider-resources/"+external.ID+"/connection", providerresources.ConnectInput{APIAccess: &catalogconnect.APIAccess{Native: true, AccountID: strings.Repeat("b", 32)}}, nil)
	c.must(http.StatusOK, http.MethodPut, "/api/v1/provider-resources/"+external.ID+"/connection", providerresources.ConnectInput{APIAccess: &catalogconnect.APIAccess{Native: true}}, &access)
	if access.EndpointID == "" || access.EndpointRevision == 0 {
		t.Fatal("native attachment has no immutable endpoint")
	}
	attached := createPlan("R2 attachment", fmt.Sprintf("version: 1\nresources:\n  assets:\n    kind: object-storage\n    existing_resource: %s\n", external.ID))
	pin := attached.Actions[0].Resource.ExistingResource
	if pin == nil || pin.ResourceID != external.ID || pin.Incarnation != incarnation || pin.AccountIdentity != connected.AccountIdentity || pin.EndpointRevision != access.EndpointRevision {
		t.Fatal("attachment review lost its provider or endpoint identity")
	}
	approve(attached)
	var bindings struct {
		Bindings []planning.Binding `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+attached.ProjectID+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 1 || !planning.ExistingResourceBinding(&bindings.Bindings[0]) || bindings.Bindings[0].ExternalID != external.ExternalID {
		t.Fatalf("R2 attachment was not persisted: %+v", bindings.Bindings)
	}
	mu.Lock()
	final := mutations
	mu.Unlock()
	if final != 1 {
		t.Fatal("attaching an existing bucket mutated the provider")
	}
	for _, path := range []string{"/api/v1/connections/" + connected.ID, "/api/v1/provider-resources", "/api/v1/plans/" + attached.ID} {
		raw := c.raw(path)
		for _, secret := range []string{token, "r2-refresh-secret", "r2-app-secret"} {
			if strings.Contains(raw, secret) {
				t.Fatalf("public response leaked a secret at %s", path)
			}
		}
	}
	running.stop()
	running = start(t, cfg)
	c.base = running.srv.URL
	sdk = lpclient.New(running.srv.URL, key.Secret)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/provider-resources/"+external.ID, nil, &access)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+attached.ProjectID+"/bindings?environment=production", nil, &bindings)
	if access.EndpointID == "" || len(bindings.Bindings) != 1 || !planning.ExistingResourceBinding(&bindings.Bindings[0]) {
		t.Fatal("R2 inventory or attachment did not survive restart")
	}
	if _, err = sdk.SaveManifest(ctx, attached.ProjectID, "version: 1\n"); err != nil {
		t.Fatal(err)
	}
	detach, err := sdk.CreatePlan(ctx, attached.ProjectID, lpclient.NewPlan{Environment: "production", Catalog: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sdk.WaitPlan(ctx, detach.ID); err != nil {
		t.Fatal(err)
	}
	var reviewed planning.Plan
	c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+detach.ID, nil, &reviewed)
	if !reviewed.Executable || len(reviewed.Actions) != 1 || reviewed.Actions[0].Op != planning.Delete {
		t.Fatalf("attachment removal did not produce a reviewed disconnect: %+v", reviewed)
	}
	approve(&reviewed)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+attached.ProjectID+"/bindings?environment=production", nil, &bindings)
	mu.Lock()
	preserved := buckets[imported] == incarnation && mutations == 1
	mu.Unlock()
	if len(bindings.Bindings) != 0 || !preserved {
		t.Fatal("disconnecting the attachment changed the provider bucket")
	}
}
