//go:build integration

package integration

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/realtime"
)

// fakeCloud is the part of Google Cloud a deployment uses: Cloud
// Storage, Artifact Registry, Cloud Build and Cloud Run. A build works
// once, then succeeds; a rollout is done when it's first checked.
type fakeCloud struct {
	mu       sync.Mutex
	base     string
	uploads  map[string][]byte
	repos    map[string]bool
	builds   []map[string]any
	polls    map[string]int
	services map[string]map[string]any
	patches  int
	public   map[string]bool
	// failBuilds makes builds fail while it's set.
	failBuilds bool
	failed     map[string]bool
	// secrets are Secret Manager's versions by secret, and who may read
	// each.
	secrets map[string][][]byte
	readers map[string][]string
	// sql are Cloud SQL instances by name, with their databases and the
	// app user's password.
	sql       map[string]map[string]any
	databases map[string][]string
	passwords map[string]string
	// buckets are the buckets made besides Loomplane's own, with their
	// policies; redis are Memorystore instances, by ID; deleted lists
	// what was deleted.
	buckets    map[string]map[string]any
	bucketIAM  map[string]json.RawMessage
	redis      map[string]map[string]any
	redisPolls map[string]int
	deleted    []string
}

func newFakeCloud(base string) *fakeCloud {
	return &fakeCloud{
		base: base, uploads: map[string][]byte{}, repos: map[string]bool{}, polls: map[string]int{},
		services: map[string]map[string]any{}, public: map[string]bool{}, failed: map[string]bool{},
		secrets: map[string][][]byte{}, readers: map[string][]string{},
		sql: map[string]map[string]any{}, databases: map[string][]string{}, passwords: map[string]string{},
		buckets: map[string]map[string]any{}, bucketIAM: map[string]json.RawMessage{},
		redis: map[string]map[string]any{}, redisPolls: map[string]int{},
	}
}

const runPrefix = "/v2/projects/acme-prod/locations/us-central1/services"

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	body, _ := io.ReadAll(r.Body)
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]any{"error": map[string]any{"message": "not found"}})
	}
	switch {
	case r.Method == http.MethodPost && p == "/storage/v1/b":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if name, _ := in["name"].(string); name != "acme-prod_loomplane" {
			f.buckets[name] = in
		}
		reply(map[string]any{"name": in["name"]})
	case strings.HasPrefix(p, "/storage/v1/b/") && strings.HasSuffix(p, "/iam"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/storage/v1/b/"), "/iam")
		if r.Method == http.MethodPut {
			f.bucketIAM[name] = body
			reply(map[string]any{})
			return
		}
		if b, ok := f.bucketIAM[name]; ok {
			_, _ = w.Write(b)
			return
		}
		reply(map[string]any{"bindings": []any{map[string]any{"role": "roles/storage.legacyBucketOwner", "members": []string{"projectOwner:acme-prod"}}}})
	case strings.HasPrefix(p, "/storage/v1/b/") && strings.HasSuffix(p, "/o") && r.Method == http.MethodGet:
		reply(map[string]any{})
	case strings.HasPrefix(p, "/storage/v1/b/") && r.Method == http.MethodGet:
		if _, ok := f.buckets[strings.TrimPrefix(p, "/storage/v1/b/")]; !ok {
			notFound()
			return
		}
		reply(map[string]any{})
	case strings.HasPrefix(p, "/storage/v1/b/") && r.Method == http.MethodDelete:
		name := strings.TrimPrefix(p, "/storage/v1/b/")
		delete(f.buckets, name)
		f.deleted = append(f.deleted, "bucket:"+name)
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(p, "/v1/projects/acme-prod/locations/us-central1/instances"):
		f.memorystore(r, p, body, reply, notFound)
	case r.Method == http.MethodPost && p == "/upload/storage/v1/b/acme-prod_loomplane/o":
		f.uploads[r.URL.Query().Get("name")] = body
		reply(map[string]any{})
	case r.Method == http.MethodPost && p == "/v1/projects/acme-prod/locations/us-central1/repositories":
		f.repos[r.URL.Query().Get("repositoryId")] = true
		reply(map[string]any{"name": "op"})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/projects/acme-prod/locations/us-central1/repositories/"):
		if !f.repos[strings.TrimPrefix(p, "/v1/projects/acme-prod/locations/us-central1/repositories/")] {
			notFound()
			return
		}
		reply(map[string]any{})
	case r.Method == http.MethodPost && p == "/v1/projects/acme-prod/builds":
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		f.builds = append(f.builds, b)
		id := fmt.Sprintf("build-%d", len(f.builds))
		f.failed[id] = f.failBuilds
		reply(map[string]any{"metadata": map[string]any{"build": map[string]any{"id": id}}})
	case r.Method == http.MethodPost && p == "/v2/entries:list":
		// A build may succeed without logging.viewer. Keep this separate
		// from the permissions required to deploy.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"logging read denied"}}`))
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/projects/acme-prod/builds/"):
		id := strings.TrimPrefix(p, "/v1/projects/acme-prod/builds/")
		f.polls[id]++
		status := "WORKING"
		switch {
		case f.polls[id] > 1 && f.failed[id]:
			status = "FAILURE"
		case f.polls[id] > 1:
			status = "SUCCESS"
		}
		reply(map[string]any{"id": id, "status": status, "statusDetail": "step 0 exited 1", "logUrl": "https://console/" + id})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v2/projects/acme-prod/locations/us-central1/operations/"):
		reply(map[string]any{"name": strings.TrimPrefix(p, "/v2/"), "done": true})
	case strings.HasPrefix(p, runPrefix):
		f.run(r, p, body, reply, notFound)
	case strings.HasPrefix(p, "/v1/projects/acme-prod/instances") || strings.HasPrefix(p, "/v1/projects/acme-prod/operations/"):
		f.cloudSQL(r, p, body, reply, notFound)
	case r.Method == http.MethodGet && p == "/v3/projects/acme-prod":
		reply(map[string]any{"name": "projects/123456", "projectId": "acme-prod"})
	case r.Method == http.MethodPost && p == "/v1/projects/acme-prod/secrets":
		id := r.URL.Query().Get("secretId")
		if _, ok := f.secrets[id]; ok {
			w.WriteHeader(http.StatusConflict)
			reply(map[string]any{"error": map[string]any{"message": "exists"}})
			return
		}
		f.secrets[id] = nil
		reply(map[string]any{"name": "projects/acme-prod/secrets/" + id})
	case r.Method == http.MethodPost && strings.HasSuffix(p, ":addVersion"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/v1/projects/acme-prod/secrets/"), ":addVersion")
		var in struct {
			Payload struct {
				Data string `json:"data"`
			} `json:"payload"`
		}
		_ = json.Unmarshal(body, &in)
		data, _ := base64.StdEncoding.DecodeString(in.Payload.Data)
		f.secrets[id] = append(f.secrets[id], data)
		reply(map[string]any{"name": fmt.Sprintf("projects/123456/secrets/%s/versions/%d", id, len(f.secrets[id]))})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/projects/acme-prod/secrets/") && strings.HasSuffix(p, ":setIamPolicy"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/v1/projects/acme-prod/secrets/"), ":setIamPolicy")
		var in struct {
			Policy struct {
				Bindings []struct {
					Members []string `json:"members"`
				} `json:"bindings"`
			} `json:"policy"`
		}
		_ = json.Unmarshal(body, &in)
		f.readers[id] = in.Policy.Bindings[0].Members
		reply(map[string]any{})
	case strings.HasPrefix(p, "/run/"):
		// The deployed service itself, answering its health check.
		_, _ = w.Write([]byte("hello from " + strings.TrimPrefix(p, "/run/")))
	default:
		notFound()
	}
}

// memorystore is Memorystore for Redis: an instance is creating the first
// time it's read, then ready.
func (f *fakeCloud) memorystore(r *http.Request, p string, body []byte, reply func(any), notFound func()) {
	rest := strings.TrimPrefix(strings.TrimPrefix(p, "/v1/projects/acme-prod/locations/us-central1/instances"), "/")
	id, sub, _ := strings.Cut(rest, "/")
	switch {
	case r.Method == http.MethodPost && rest == "":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		f.redis[r.URL.Query().Get("instanceId")] = in
		reply(map[string]any{"name": "projects/acme-prod/locations/us-central1/operations/redis"})
	case f.redis[id] == nil:
		notFound()
	case r.Method == http.MethodGet && sub == "authString":
		reply(map[string]any{"authString": "auth-" + id})
	case r.Method == http.MethodGet:
		f.redisPolls[id]++
		state := "CREATING"
		if f.redisPolls[id] > 1 {
			state = "READY"
		}
		reply(map[string]any{"name": id, "state": state, "host": "10.0.0.5", "port": 6379})
	case r.Method == http.MethodDelete:
		delete(f.redis, id)
		f.deleted = append(f.deleted, "redis:"+id)
		reply(map[string]any{"name": "op"})
	default:
		notFound()
	}
}

// cloudSQL is Cloud SQL Admin. Operations are done when first checked.
func (f *fakeCloud) cloudSQL(r *http.Request, p string, body []byte, reply func(any), notFound func()) {
	done := map[string]any{"name": "op-sql", "status": "DONE"}
	rest := strings.TrimPrefix(p, "/v1/projects/acme-prod/instances")
	switch {
	case strings.HasPrefix(p, "/v1/projects/acme-prod/operations/"):
		reply(done)
	case r.Method == http.MethodPost && rest == "":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		name := in["name"].(string)
		region, _ := in["region"].(string)
		f.sql[name] = map[string]any{
			"name": name, "connectionName": "acme-prod:" + region + ":" + name,
			"project": "acme-prod", "region": region, "createTime": "2026-10-21T00:00:00Z",
			"databaseVersion": in["databaseVersion"], "state": "RUNNABLE",
			"settings": in["settings"],
		}
		reply(map[string]any{"name": "op-sql", "status": "PENDING"})
	case strings.HasSuffix(rest, "/databases") && r.Method == http.MethodPost:
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		name := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), "/databases")
		f.databases[name] = append(f.databases[name], in["name"])
		reply(done)
	case strings.HasSuffix(rest, "/users"):
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		f.passwords[strings.TrimSuffix(strings.TrimPrefix(rest, "/"), "/users")] = in["password"]
		reply(done)
	case r.Method == http.MethodGet:
		in, ok := f.sql[strings.TrimPrefix(rest, "/")]
		if !ok {
			notFound()
			return
		}
		reply(in)
	case r.Method == http.MethodDelete:
		delete(f.sql, strings.TrimPrefix(rest, "/"))
		reply(done)
	default:
		notFound()
	}
}

func (f *fakeCloud) run(r *http.Request, p string, body []byte, reply func(any), notFound func()) {
	op := map[string]any{"name": "projects/acme-prod/locations/us-central1/operations/rollout", "done": false}
	switch {
	case r.Method == http.MethodPost && p == runPrefix:
		name := r.URL.Query().Get("serviceId")
		var svc map[string]any
		_ = json.Unmarshal(body, &svc)
		svc["uri"] = f.base + "/run/" + name
		svc["terminalCondition"] = map[string]any{"state": "CONDITION_SUCCEEDED"}
		f.services[name] = svc
		reply(op)
	case strings.HasSuffix(p, ":setIamPolicy"):
		var in struct {
			Policy struct {
				Bindings []struct {
					Members []string `json:"members"`
				} `json:"bindings"`
			} `json:"policy"`
		}
		_ = json.Unmarshal(body, &in)
		name := strings.TrimSuffix(strings.TrimPrefix(p, runPrefix+"/"), ":setIamPolicy")
		delete(f.public, name)
		for _, b := range in.Policy.Bindings {
			if slices.Contains(b.Members, "allUsers") {
				f.public[name] = true
			}
		}
		reply(map[string]any{})
	default:
		name := strings.TrimPrefix(p, runPrefix+"/")
		svc, ok := f.services[name]
		switch {
		case !ok:
			notFound()
		case r.Method == http.MethodGet:
			reply(svc)
		case r.Method == http.MethodPatch:
			var next map[string]any
			_ = json.Unmarshal(body, &next)
			next["uri"], next["terminalCondition"] = svc["uri"], svc["terminalCondition"]
			f.services[name] = next
			f.patches++
			reply(op)
		case r.Method == http.MethodDelete:
			delete(f.services, name)
			reply(op)
		}
	}
}

func (f *fakeCloud) container(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.services) != 1 {
		t.Fatalf("%d cloud run services", len(f.services))
	}
	for _, svc := range f.services {
		return svc["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	}
	return nil
}

type deploymentBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error"`
	Source string `json:"source"`
	Steps  []struct {
		ActionID string `json:"action_id"`
		Status   string `json:"status"`
		Message  string `json:"message"`
		URL      string `json:"url"`
		Image    string `json:"image"`
	} `json:"steps"`
}

// approveAndWait approves a ready plan and waits for its deployment.
func approveAndWait(t *testing.T, c *client, plan planBody) deploymentBody {
	t.Helper()
	c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	var d deploymentBody
	deadline := time.Now().Add(readyTimeout)
	for d.Status == "" || d.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("deployment still %q after %s: %+v", d.Status, readyTimeout, d)
		}
		time.Sleep(100 * time.Millisecond)
		if c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &d) == http.StatusNotFound {
			d.Status = ""
		}
	}
	return d
}

const apiOnCloudRun = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
ignore:
  resources: [postgres, redis]
`

func TestDeploy_aGoServiceRunsOnCloudRun(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	google.deploy = cloud
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	// A variable for api, which the deployment hands it.
	var envs struct {
		Environments []struct{ Name string } `json:"environments"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	c.must(http.StatusCreated, http.MethodPost, base+"/variables",
		map[string]any{"key": "GREETING", "value": "hello", "environment": "production"}, nil)
	const secretValue = "s3cr3t-token-of-api"
	c.must(http.StatusCreated, http.MethodPost, base+"/variables",
		map[string]any{"key": "API_TOKEN", "value": secretValue, "secret": true, "environment": "production"}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiOnCloudRun}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 1 {
		t.Fatalf("plan %+v", plan)
	}

	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" || len(d.Steps) != 1 || d.Steps[0].Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	step := d.Steps[0]
	if !strings.HasPrefix(step.URL, google.srv.URL+"/run/") ||
		!strings.HasPrefix(step.Image, "us-central1-docker.pkg.dev/acme-prod/loomplane/api-") || !strings.HasSuffix(step.Image, ":"+exampleSHA[:12]) {
		t.Fatalf("step %+v", step)
	}

	// The commit went to Cloud Build as GitHub served it, and the
	// Dockerfile was built from its own directory.
	cloud.mu.Lock()
	upload := cloud.uploads["sources/"+exampleSHA+".tar.gz"]
	build := cloud.builds[0]
	cloud.mu.Unlock()
	if len(upload) == 0 || string(upload) != string(exampleTarball(t)) {
		t.Fatalf("uploaded %d bytes, not the tarball", len(upload))
	}
	steps := build["steps"].([]any)[0].(map[string]any)
	if steps["dir"] != "acme-mono-fedcba9/services/api" || build["images"].([]any)[0] != step.Image {
		t.Fatalf("build %+v", build)
	}
	ctr := cloud.container(t)
	ports := ctr["ports"].([]any)[0].(map[string]any)
	if ctr["image"] != step.Image || ports["containerPort"] != float64(8080) {
		t.Fatalf("container %+v", ctr)
	}
	env := ctr["env"].([]any)
	if !slices.ContainsFunc(env, func(e any) bool {
		m := e.(map[string]any)
		return m["name"] == "GREETING" && m["value"] == "hello"
	}) {
		t.Fatalf("api didn't get its variable: %+v", env)
	}
	// The secret reaches the service through Secret Manager, readable by
	// its runtime account only, and never as a value.
	var ref map[string]any
	for _, e := range env {
		if m := e.(map[string]any); m["name"] == "API_TOKEN" {
			if _, plain := m["value"]; plain {
				t.Fatalf("API_TOKEN set as a value: %+v", m)
			}
			ref = m["valueSource"].(map[string]any)["secretKeyRef"].(map[string]any)
		}
	}
	secretID, _ := ref["secret"].(string)
	cloud.mu.Lock()
	versions, readers := cloud.secrets[secretID], cloud.readers[secretID]
	cloud.mu.Unlock()
	if ref["version"] != "1" || len(versions) != 1 || string(versions[0]) != secretValue ||
		!slices.Equal(readers, []string{"serviceAccount:123456-compute@developer.gserviceaccount.com"}) {
		t.Fatalf("secret %v: versions %q, readers %v", ref, versions, readers)
	}
	// Ctrl Plane stores what it's given in plain JSON, so it's given no
	// secret.
	pg, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close(context.Background())
	for _, table := range []string{"cp_instances", "cp_releases", "cp_deployments"} {
		var n int
		if err := pg.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" t WHERE t::text LIKE '%' || $1 || '%'", secretValue).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			t.Fatalf("%s holds the secret in %d rows", table, n)
		}
	}
	cloud.mu.Lock()
	public := len(cloud.public)
	cloud.mu.Unlock()
	if public != 1 {
		t.Fatal("api wasn't made public")
	}

	// Planning again finds it deployed: nothing to change.
	var again planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &again)
	waitPlanned(t, c, &again)
	if again.Status != "ready" || len(again.Actions) != 1 || again.Actions[0].Op != "keep" {
		t.Fatalf("second plan %+v", again)
	}

	// A change rolls out through Ctrl Plane's deploy, as an update to the
	// same service.
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest",
		map[string]string{"yaml": strings.Replace(apiOnCloudRun, "    provider: cloud-run\n", "    provider: cloud-run\n    scale: {min: 1, max: 4}\n", 1)}, nil)
	var update planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &update)
	waitPlanned(t, c, &update)
	if update.Status != "ready" || update.Actions[0].Op != "update" {
		t.Fatalf("update plan %+v", update)
	}
	d2 := approveAndWait(t, c, update)
	if d2.Status != "succeeded" || d2.Steps[0].URL != step.URL {
		t.Fatalf("update deployment %+v", d2)
	}
	cloud.mu.Lock()
	patches, services := cloud.patches, len(cloud.services)
	var scaling map[string]any
	for _, svc := range cloud.services {
		scaling, _ = svc["template"].(map[string]any)["scaling"].(map[string]any)
	}
	cloud.mu.Unlock()
	if patches != 1 || services != 1 || scaling["maxInstanceCount"] != float64(4) {
		t.Fatalf("patches %d, services %d, scaling %v", patches, services, scaling)
	}
}

// fakeVercelDeploys is the part of Vercel a deployment uses. It checks
// each file against its SHA-1, refuses a deployment naming a file it
// hasn't got, builds once, then is ready, aliased to a path on itself so
// the health check reaches it.
type fakeVercelDeploys struct {
	mu       sync.Mutex
	srv      *httptest.Server
	token    string
	files    map[string]int
	projects map[string]map[string]any
	env      map[string]any
	deployed []map[string]any
	polls    int
}

func newFakeVercelDeploys(t *testing.T, token string) *fakeVercelDeploys {
	t.Helper()
	f := &fakeVercelDeploys{token: token, files: map[string]int{}, projects: map[string]map[string]any{}, env: map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeVercelDeploys) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/site/") {
		_, _ = w.Write([]byte("<html>web</html>"))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"Not authorized"}}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	switch {
	case p == "/v2/user":
		reply(200, map[string]any{"user": map[string]any{"id": "user_ada", "username": "ada"}})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v9/projects/"):
		prj, ok := f.projects[strings.TrimPrefix(p, "/v9/projects/")]
		if !ok {
			reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "Project not found"}})
			return
		}
		reply(200, prj)
	case r.Method == http.MethodPost && p == "/v10/projects":
		var prj map[string]any
		_ = json.Unmarshal(body, &prj)
		prj["id"] = "prj_web"
		f.projects[prj["name"].(string)], f.projects["prj_web"] = prj, prj
		reply(200, prj)
	case r.Method == http.MethodPost && p == "/v10/projects/prj_web/env":
		var vars []map[string]any
		_ = json.Unmarshal(body, &vars)
		for _, v := range vars {
			f.env[v["key"].(string)] = v["value"]
		}
		reply(200, map[string]any{})
	case r.Method == http.MethodPost && p == "/v2/files":
		sum := sha1.Sum(body)
		if hex.EncodeToString(sum[:]) != r.Header.Get("x-vercel-digest") {
			reply(400, map[string]any{"error": map[string]any{"code": "bad_digest", "message": "digest mismatch"}})
			return
		}
		f.files[r.Header.Get("x-vercel-digest")] = len(body)
		reply(200, map[string]any{})
	case r.Method == http.MethodPost && p == "/v13/deployments":
		var d map[string]any
		_ = json.Unmarshal(body, &d)
		var missing []string
		for _, file := range d["files"].([]any) {
			if _, ok := f.files[file.(map[string]any)["sha"].(string)]; !ok {
				missing = append(missing, file.(map[string]any)["sha"].(string))
			}
		}
		if len(missing) > 0 {
			reply(400, map[string]any{"error": map[string]any{"code": "missing_files", "message": "Missing files", "missing": missing}})
			return
		}
		f.deployed = append(f.deployed, d)
		reply(200, map[string]any{"id": "dpl_1", "url": "web-abc.vercel.app", "readyState": "QUEUED"})
	case r.Method == http.MethodGet && p == "/v13/deployments/dpl_1":
		f.polls++
		if f.polls < 2 {
			reply(200, map[string]any{"id": "dpl_1", "readyState": "BUILDING"})
			return
		}
		reply(200, map[string]any{"id": "dpl_1", "readyState": "READY", "aliasAssigned": true,
			"alias": []string{f.srv.URL + "/site/web"}, "url": "web-abc.vercel.app"})
	case r.Method == http.MethodGet && p == "/v3/deployments/dpl_1/events":
		for _, id := range []string{"build-a", "build-b"} {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "stdout", "created": 10000, "payload": map[string]any{"id": id, "deploymentId": "dpl_1", "text": "build output " + f.token}})
		}
	default:
		reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "Not found"}})
	}
}

const webAndAPIManifest = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
  web:
    source: node:apps/web
    provider: vercel
    needs: [api]
ignore:
  resources: [postgres, redis]
`

func TestDeploy_aNextAppOnVercelFindsItsGoAPIOnCloudRun(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	google.deploy = cloud
	const vercelToken = "vcl_0123456789abcdef"
	vc := newFakeVercelDeploys(t, vercelToken)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Clouds.VercelEndpoint = vc.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "vercel", "name": "Web", "credential": vercelToken}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": webAndAPIManifest}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 2 {
		t.Fatalf("plan %+v", plan)
	}

	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" || len(d.Steps) != 2 {
		t.Fatalf("deployment %+v", d)
	}
	var retained realtime.LogPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID+"/logs", nil, &retained)
	if len(retained.Logs) != 2 || retained.Logs[0].ProviderID == retained.Logs[1].ProviderID || strings.Contains(retained.Logs[0].Message, vercelToken) {
		t.Fatalf("managed output replay/redaction: %+v", retained)
	}
	var snapshot realtime.Snapshot
	c.must(http.StatusOK, http.MethodGet, "/api/v1/realtime/snapshot?deployment_ids="+d.ID, nil, &snapshot)
	denied := false
	for _, record := range snapshot.Records {
		if record.Type == "build.collection" {
			var collection realtime.Collection
			if err := json.Unmarshal(record.Data, &collection); err != nil {
				t.Fatal(err)
			}
			if collection.OperationID == "build-1" && collection.State == "unavailable" {
				denied = true
			}
		}
	}
	if !denied {
		t.Fatalf("log permission failure did not remain independent: %+v", snapshot.Records)
	}
	api, web := d.Steps[0], d.Steps[1]
	if api.ActionID != "service:api" || web.ActionID != "service:web" || web.URL != vc.srv.URL+"/site/web" {
		t.Fatalf("steps %+v", d.Steps)
	}

	vc.mu.Lock()
	defer vc.mu.Unlock()
	// web knows where api answers, before Vercel builds it.
	if vc.env["API_URL"] != api.URL {
		t.Fatalf("web's API_URL is %v, want %s", vc.env["API_URL"], api.URL)
	}
	prj := vc.projects["prj_web"]
	if prj["framework"] != "nextjs" || prj["rootDirectory"] != "apps/web" {
		t.Fatalf("vercel project %+v", prj)
	}
	// The whole repository went up, paths relative to its root, so the
	// workspace's shared packages are there when Vercel builds.
	files := vc.deployed[0]["files"].([]any)
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.(map[string]any)["file"].(string))
	}
	for _, want := range []string{"apps/web/package.json", "packages/ui/package.json", "pnpm-workspace.yaml"} {
		if !slices.Contains(names, want) {
			t.Fatalf("%s wasn't deployed: %v", want, names)
		}
	}
	if vc.deployed[0]["target"] != "production" {
		t.Fatalf("deployed to %v", vc.deployed[0]["target"])
	}
}

func TestDeploy_aFailedDeploymentIsRetriedFromWhereItStopped(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	cloud.failBuilds = true
	google.deploy = cloud
	const vercelToken = "vcl_0123456789abcdef"
	vc := newFakeVercelDeploys(t, vercelToken)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Clouds.VercelEndpoint = vc.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "vercel", "name": "Web", "credential": vercelToken}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": webAndAPIManifest}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	d := approveAndWait(t, c, plan)
	if d.Status != "failed" || !strings.Contains(d.Error, "step 0 exited 1") ||
		d.Steps[0].Status != "failed" || d.Steps[1].Status != "skipped" {
		t.Fatalf("deployment %+v", d)
	}
	// Retrying something that hasn't failed is refused; so is retrying as
	// a developer, which the role catalog covers elsewhere.
	c.must(http.StatusNotFound, http.MethodPost, "/api/v1/deployments/dep_nope/retry", nil, nil)

	// The build is fixed; retrying runs the failed step with a new build,
	// then the step it skipped.
	cloud.mu.Lock()
	cloud.failBuilds = false
	cloud.mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, &d)
	if d.Status != "running" || d.Steps[0].Status != "pending" || d.Steps[1].Status != "pending" {
		t.Fatalf("reopened %+v", d)
	}
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, nil)
	deadline := time.Now().Add(readyTimeout)
	for d.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("retry still running: %+v", d)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID, nil, &d)
	}
	if d.Status != "succeeded" || d.Steps[0].Status != "succeeded" || d.Steps[1].Status != "succeeded" || d.Steps[1].URL == "" {
		t.Fatalf("retried %+v", d)
	}
	cloud.mu.Lock()
	builds := len(cloud.builds)
	cloud.mu.Unlock()
	if builds != 2 {
		t.Fatalf("%d builds, want the failed one and the retry", builds)
	}

	// The organization's activity counts the deployment once, retried.
	var act struct {
		Days        int `json:"days"`
		Deployments struct {
			Succeeded     int `json:"succeeded"`
			Failed        int `json:"failed"`
			Retried       int `json:"retried"`
			MedianSeconds int `json:"median_seconds"`
		} `json:"deployments"`
		Plans struct {
			Approved int `json:"approved"`
		} `json:"plans"`
		Analyses struct {
			Succeeded int `json:"succeeded"`
		} `json:"analyses"`
		Drift struct {
			Deployed int `json:"deployed"`
		} `json:"drift"`
		Daily []struct {
			Date      string `json:"date"`
			Succeeded int    `json:"succeeded"`
		} `json:"daily"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/activity?days=7", nil, &act)
	today := time.Now().UTC().Format(time.DateOnly)
	if act.Days != 7 || act.Deployments.Succeeded != 1 || act.Deployments.Failed != 0 || act.Deployments.Retried != 1 ||
		act.Plans.Approved != 1 || act.Analyses.Succeeded == 0 || act.Drift.Deployed != 2 ||
		len(act.Daily) != 7 || act.Daily[6].Date != today || act.Daily[6].Succeeded != 1 {
		t.Fatalf("activity %+v", act)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodGet, "/api/v1/activity?days=soon", nil, nil)
}

type bindingBody struct {
	Name         string `json:"name"`
	Drift        string `json:"drift"`
	DriftMessage string `json:"drift_message"`
	ObservedAt   string `json:"observed_at"`
	Revision     string `json:"revision"`
	CommitSHA    string `json:"commit_sha"`
}

// waitDrift polls the environment's bindings until api's drift is want.
func waitDrift(t *testing.T, c *client, base, want string) bindingBody {
	const name = "api"
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for {
		var list struct {
			Bindings []bindingBody `json:"bindings"`
		}
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &list)
		for _, b := range list.Bindings {
			if b.Name == name && b.Drift == want && b.ObservedAt != "" {
				return b
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never drifted to %q: %+v", name, want, list.Bindings)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestDeploy_driftIsFoundAndPlannedBack(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	google.deploy = cloud
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Drift.Interval = time.Second
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiOnCloudRun}, nil)

	plan := func() planBody {
		var p planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &p)
		waitPlanned(t, c, &p)
		return p
	}
	if d := approveAndWait(t, c, plan()); d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	b := waitDrift(t, c, base, "")
	if b.CommitSHA != exampleSHA || !strings.Contains(b.Revision, "/loomplane/api-") {
		t.Fatalf("binding %+v", b)
	}

	// Someone deploys another image by hand.
	cloud.mu.Lock()
	for _, svc := range cloud.services {
		svc["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "gcr.io/elsewhere/api:hotfix"
	}
	cloud.mu.Unlock()
	b = waitDrift(t, c, base, "changed")
	if !strings.Contains(b.DriftMessage, "gcr.io/elsewhere/api:hotfix is live") {
		t.Fatalf("binding %+v", b)
	}
	if p := plan(); p.Actions[0].Op != "update" {
		t.Fatalf("plan after a change %+v", p)
	}

	// Then someone deletes it.
	cloud.mu.Lock()
	for name := range cloud.services {
		delete(cloud.services, name)
	}
	cloud.mu.Unlock()
	waitDrift(t, c, base, "missing")
	p := plan()
	if p.Actions[0].Op != "create" {
		t.Fatalf("plan after a deletion %+v", p)
	}
	// Approving it deploys api again, and the next check finds it as
	// deployed.
	if d := approveAndWait(t, c, p); d.Status != "succeeded" {
		t.Fatalf("redeployment %+v", d)
	}
	cloud.mu.Lock()
	services := len(cloud.services)
	cloud.mu.Unlock()
	if services != 1 {
		t.Fatalf("%d services after redeploying", services)
	}
	waitDrift(t, c, base, "")
}

const apiWithPostgres = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
ignore:
  resources: [redis]
`

func TestDeploy_aGoServiceGetsItsCloudSQLDatabase(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	google.deploy = cloud
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiWithPostgres}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 2 || plan.Actions[0].ID != "resource:db" {
		t.Fatalf("plan %+v", plan)
	}
	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" || d.Steps[0].ActionID != "resource:db" || d.Steps[0].Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}

	cloud.mu.Lock()
	if len(cloud.sql) != 1 {
		t.Fatalf("%d cloud sql instances", len(cloud.sql))
	}
	var instance string
	for name := range cloud.sql {
		instance = name
	}
	conn := "acme-prod:us-central1:" + instance
	password := cloud.passwords[instance]
	if password == "" || !slices.Equal(cloud.databases[instance], []string{"db"}) || cloud.sql[instance]["databaseVersion"] != "POSTGRES_17" {
		t.Fatalf("instance %s: databases %v, version %v", instance, cloud.databases[instance], cloud.sql[instance]["databaseVersion"])
	}
	var svc map[string]any
	for _, s := range cloud.services {
		svc = s
	}
	tmpl := svc["template"].(map[string]any)
	vol := tmpl["volumes"].([]any)[0].(map[string]any)
	if vol["cloudSqlInstance"].(map[string]any)["instances"].([]any)[0] != conn {
		t.Fatalf("volumes %+v", tmpl["volumes"])
	}
	ctr := tmpl["containers"].([]any)[0].(map[string]any)
	if ctr["volumeMounts"].([]any)[0].(map[string]any)["mountPath"] != "/cloudsql" {
		t.Fatalf("mounts %+v", ctr["volumeMounts"])
	}
	var secretID string
	for _, e := range ctr["env"].([]any) {
		if m := e.(map[string]any); m["name"] == "DATABASE_URL" {
			if _, plain := m["value"]; plain {
				t.Fatalf("DATABASE_URL set as a value: %+v", m)
			}
			secretID = m["valueSource"].(map[string]any)["secretKeyRef"].(map[string]any)["secret"].(string)
		}
	}
	got := string(cloud.secrets[secretID][0])
	cloud.mu.Unlock()
	want := "postgresql://app:" + password + "@/db?host=%2Fcloudsql%2F" + strings.ReplaceAll(conn, ":", "%3A")
	if got != want {
		t.Fatalf("DATABASE_URL is %q, want %q", got, want)
	}

	// Planning again finds both as deployed.
	var again planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &again)
	waitPlanned(t, c, &again)
	for _, a := range again.Actions {
		if a.Op != "keep" {
			t.Fatalf("second plan %+v", again)
		}
	}

	// Moving the environment to another Google Cloud connection would
	// leave db's data behind, so the plan refuses, and api starts again
	// through the new one.
	var other struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Other", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, &other)
	c.must(http.StatusOK, http.MethodPut, base+"/environments/production/connections/gcp", map[string]string{"connection_id": other.ID}, nil)
	var moved planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &moved)
	waitPlanned(t, c, &moved)
	if moved.Executable || len(moved.Problems) != 1 || moved.Problems[0].Code != "connection-moved" ||
		!strings.Contains(moved.Problems[0].Message, "Choose Prod for Google Cloud again") {
		t.Fatalf("plan through another connection %+v", moved)
	}
	for _, a := range moved.Actions {
		if want := map[string]string{"resource:db": "keep", "service:api": "replace"}[a.ID]; a.Op != want {
			t.Fatalf("plan through another connection %+v", moved)
		}
	}
	cloud.mu.Lock()
	instances := len(cloud.sql)
	cloud.mu.Unlock()
	if instances != 1 {
		t.Fatalf("%d cloud sql instances", instances)
	}
}

const apiWithBucketAndRedis = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
    needs: [files, cache]
resources:
  files:
    kind: object-storage
    provider: cloud-storage
  cache:
    kind: redis
    provider: memorystore
ignore:
  resources: [postgres]
`

func TestDeploy_aCloudRunServiceGetsABucketAndMemorystore(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	google.deploy = cloud
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiWithBucketAndRedis}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 3 {
		t.Fatalf("plan %+v", plan)
	}
	if d := approveAndWait(t, c, plan); d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}

	cloud.mu.Lock()
	var bucket string
	var spec map[string]any
	for name, b := range cloud.buckets {
		bucket, spec = name, b
	}
	iam := spec["iamConfiguration"].(map[string]any)
	if !strings.HasPrefix(bucket, "lp-files-") || iam["publicAccessPrevention"] != "enforced" {
		t.Fatalf("bucket %s %v", bucket, spec)
	}
	// The runtime account may use it, and the bucket's other bindings stay.
	policy := string(cloud.bucketIAM[bucket])
	if !strings.Contains(policy, "roles/storage.objectAdmin") || !strings.Contains(policy, "serviceAccount:123456-compute@developer.gserviceaccount.com") ||
		!strings.Contains(policy, "projectOwner:acme-prod") {
		t.Fatalf("bucket policy %s", policy)
	}
	if len(cloud.redis) != 1 {
		t.Fatalf("redis %v", cloud.redis)
	}
	var redisID string
	for id, in := range cloud.redis {
		redisID = id
		if in["authEnabled"] != true || in["authorizedNetwork"] != "projects/acme-prod/global/networks/default" {
			t.Fatalf("redis %v", in)
		}
	}
	// api reaches the cache through the default network, and gets both
	// addresses from Secret Manager.
	var svc map[string]any
	for _, s := range cloud.services {
		svc = s
	}
	tmpl := svc["template"].(map[string]any)
	vpc, _ := tmpl["vpcAccess"].(map[string]any)
	if vpc == nil || vpc["egress"] != "PRIVATE_RANGES_ONLY" {
		t.Fatalf("api's template %v", tmpl)
	}
	got := map[string]string{}
	for _, e := range tmpl["containers"].([]any)[0].(map[string]any)["env"].([]any) {
		m := e.(map[string]any)
		if src, ok := m["valueSource"].(map[string]any); ok {
			id := src["secretKeyRef"].(map[string]any)["secret"].(string)
			got[m["name"].(string)] = string(cloud.secrets[id][len(cloud.secrets[id])-1])
		}
	}
	cloud.mu.Unlock()
	if got["STORAGE_URL"] != "gs://"+bucket || got["REDIS_URL"] != "redis://:auth-"+redisID+"@10.0.0.5:6379" {
		t.Fatalf("api's secrets %v", got)
	}

	var again planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &again)
	waitPlanned(t, c, &again)
	for _, a := range again.Actions {
		if a.Op != "keep" {
			t.Fatalf("second plan %+v", again)
		}
	}

	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nservices:\n  api:\n    source: go:services/api\n    provider: cloud-run\nignore:\n  resources: [postgres, redis]\n"}, nil)
	var removal planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &removal)
	waitPlanned(t, c, &removal)
	if d := approveAndWait(t, c, removal); d.Status != "succeeded" {
		t.Fatalf("removal %+v", d)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if !slices.Contains(cloud.deleted, "bucket:"+bucket) || !slices.Contains(cloud.deleted, "redis:"+redisID) {
		t.Fatalf("deleted %v", cloud.deleted)
	}
}
