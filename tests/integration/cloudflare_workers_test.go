//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
)

func TestCloudflareWorkerDeploysNativeAttachmentAndRemovesOnlyItsScript(t *testing.T) {
	const account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const token = "cloudflare-worker-fixture-token-01234567890123456789"
	const bucket = "external-assets"
	const created = "2026-11-01T12:00:00Z"
	const version = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	const privateFile = "PRIVATE_FILE_MUST_NOT_BE_UPLOADED"
	var mu sync.Mutex
	var script string
	var upload cloudflare.WorkerUpload
	uploads, deletes := 0, 0
	exists := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("Worker request lost the connected account credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		root := "/accounts/" + account
		path := strings.TrimPrefix(r.URL.Path, root)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && path == "/tokens/verify":
			fmt.Fprint(w, `{"success":true,"result":{"id":"fixture-token","status":"active"}}`)
		case r.Method == http.MethodGet && path == "":
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"name":"Worker account"}}`, account)
		case r.Method == http.MethodGet && path == "/r2/buckets":
			rows := []map[string]string{}
			if jurisdiction := r.Header.Get("cf-r2-jurisdiction"); jurisdiction == "" || jurisdiction == "default" {
				rows = append(rows, map[string]string{"name": bucket, "creation_date": created})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"buckets": rows}})
		case r.Method == http.MethodGet && path == "/r2/buckets/"+bucket:
			fmt.Fprintf(w, `{"success":true,"result":{"name":%q,"creation_date":%q}}`, bucket, created)
		case r.Method == http.MethodGet && path == "/workers/durable_objects/namespaces":
			fmt.Fprint(w, `{"success":true,"result":[]}`)
		case r.Method == http.MethodGet && path == "/workers/scripts":
			rows := []map[string]string{}
			if exists {
				rows = append(rows, map[string]string{"id": script, "created_on": created})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": rows})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/script-settings"):
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"success":false}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"tags": upload.Tags}})
		case r.Method == http.MethodPut && strings.HasPrefix(path, "/workers/scripts/"):
			reader, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			parts := map[string]string{}
			for {
				part, partErr := reader.NextPart()
				if errors.Is(partErr, io.EOF) {
					break
				}
				if partErr != nil {
					t.Error(partErr)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				body, readErr := io.ReadAll(part)
				if readErr != nil {
					t.Error(readErr)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				parts[part.FormName()] = string(body)
			}
			if err = json.Unmarshal([]byte(parts["metadata"]), &upload); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if parts["worker.js"] == "" || parts["answer.js"] == "" || strings.Contains(fmt.Sprint(parts), privateFile) {
				t.Error("upload lost committed modules or included a private file")
			}
			script = strings.TrimPrefix(path, "/workers/scripts/")
			uploads++
			exists = true
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"created_on":%q}}`, script, created)
		case r.Method == http.MethodGet && path == "/workers/scripts/"+script+"/deployments":
			fmt.Fprintf(w, `{"success":true,"result":{"deployments":[{"id":%q,"versions":[{"version_id":%q,"percentage":100}]}]}}`, version, version)
		case r.Method == http.MethodGet && path == "/workers/subdomain":
			fmt.Fprint(w, `{"success":true,"result":{"subdomain":"fixture"}}`)
		case r.Method == http.MethodPost && path == "/workers/scripts/"+script+"/subdomain":
			fmt.Fprint(w, `{"success":true,"result":{"enabled":true}}`)
		case r.Method == http.MethodDelete && path == "/workers/scripts/"+script:
			deletes++
			exists = false
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && (path == "/d1/database" || path == "/storage/kv/namespaces" || path == "/queues" || path == "/vectorize/v2/indexes" || path == "/hyperdrive/configs"):
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"fixture permission denied"}]}`)
		default:
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.CloudflareEndpoint = api.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{"acme/worker": tarball(t, "acme-worker-1234567", map[string]string{
		"package.json": `{"name":"worker","type":"module","main":"worker.js","scripts":{"start":"wrangler dev"}}`,
		"worker.js":    `import {answer} from './answer.js'; export default {async fetch(request,env){return new Response((await env.ASSETS.get('hello'))?.body ?? answer)}};`,
		"answer.js":    `export const answer='ready';`,
		".env":         privateFile,
	})}).URL
	running := start(t, cfg)
	defer func() { running.stop() }()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connected connections.Connection
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "cloudflare", "name": "Worker account", "credential": token, "settings": map[string]string{"account_id": account, "services": "r2"}}, &connected)
	if connected.Status != connections.StatusReady {
		t.Fatalf("Worker account is not ready: %+v", connected)
	}
	var synced providerresources.ProviderResourceSync
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/"+connected.ID+"/resource-syncs", nil, &synced)
	var resources providerresources.ProviderResourceList
	c.must(http.StatusOK, http.MethodGet, "/api/v1/provider-resources", nil, &resources)
	var external *providerresources.ProviderResource
	for _, resource := range resources.Resources {
		if resource.Name == bucket {
			external = resource
			break
		}
	}
	if external == nil {
		t.Fatalf("external bucket was not discovered: %+v", synced)
	}
	c.must(http.StatusOK, http.MethodPut, "/api/v1/provider-resources/"+external.ID+"/connection", providerresources.ConnectInput{APIAccess: &catalogconnect.APIAccess{Native: true}}, nil)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Worker native attachment", "repository_url": "https://github.com/acme/worker"}, &project)
	analyze(t, cfg, c, project.ID)
	base := "/api/v1/projects/" + project.ID
	manifest := fmt.Sprintf("version: 1\nservices:\n  app:\n    source: node:.\n    kind: http-service\n    provider: cloudflare-workers\n    target: %s\n    worker: {main: worker.js, compatibility_date: '2026-11-01'}\n    needs: [assets]\nresources:\n  assets: {kind: object-storage, existing_resource: %s}\n", connected.ID, external.ID)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || len(plan.Actions) != 2 {
		t.Fatalf("Worker attachment plan is not executable: %+v", plan)
	}
	mu.Lock()
	before := uploads + deletes
	mu.Unlock()
	if before != 0 {
		t.Fatal("planning mutated the Worker before approval")
	}
	deployed := approveAndWait(t, c, plan)
	if deployed.Status != "succeeded" {
		t.Fatalf("Worker deployment failed: %+v", deployed)
	}
	mu.Lock()
	uploaded := upload
	uploadCount := uploads
	workerName := script
	mu.Unlock()
	if uploadCount != 1 || uploaded.MainModule != "worker.js" || uploaded.CompatibilityDate != "2026-11-01" {
		t.Fatalf("Worker upload lost the reviewed module or runtime: %+v", uploaded)
	}
	native := false
	for _, binding := range uploaded.Bindings {
		if binding.Name == "ASSETS" {
			native = binding.Type == "r2_bucket" && binding.BucketName == bucket && binding.Jurisdiction == "default"
		}
		if strings.Contains(binding.Text, token) {
			t.Fatal("Worker runtime received the cloud account credential")
		}
	}
	if !native {
		t.Fatal("Worker did not receive the approved native R2 binding")
	}
	var bound struct {
		Bindings []planning.Binding `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bound)
	serviceFound, attachmentFound := false, false
	for _, binding := range bound.Bindings {
		if binding.Subject == planning.ServiceSubject {
			serviceFound = binding.ExternalID == workerName && binding.PhysicalID != "" && binding.Revision == version && len(binding.OwnedResources) != 0
		}
		if planning.ExistingResourceBinding(&binding) {
			attachmentFound = binding.ExternalID == external.ExternalID
		}
	}
	if !serviceFound || !attachmentFound {
		t.Fatalf("Worker ownership or external attachment not persisted: %+v", bound.Bindings)
	}
	running.stop()
	running = start(t, cfg)
	c.base = running.srv.URL
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bound)
	if len(bound.Bindings) != 2 {
		t.Fatal("Worker and attachment bindings did not survive restart")
	}
	for _, path := range []string{base + "/bindings?environment=production", "/api/v1/connections/" + connected.ID, "/api/v1/plans/" + plan.ID} {
		if strings.Contains(c.raw(path), token) {
			t.Fatal("public deployment metadata leaked the cloud account credential")
		}
	}
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nservices:\n  app: {source: node:., enabled: false}\n"}, nil)
	var removal planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &removal)
	waitPlanned(t, c, &removal)
	if !removal.Executable || len(removal.Actions) != 2 {
		t.Fatalf("Worker disconnect plan is not executable: %+v", removal)
	}
	removed := approveAndWait(t, c, removal)
	if removed.Status != "succeeded" {
		t.Fatalf("Worker disconnect failed: %+v", removed)
	}
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bound)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/provider-resources/"+external.ID, nil, &external)
	mu.Lock()
	deletedCount, finalUploads, stillExists := deletes, uploads, exists
	mu.Unlock()
	if deletedCount != 1 || finalUploads != 1 || stillExists || len(bound.Bindings) != 0 || external.ExternalID == "" || external.Incarnation != created || external.Ownership != "external" {
		t.Fatal("disconnect did not remove only the owned Worker and its attachment")
	}
}
