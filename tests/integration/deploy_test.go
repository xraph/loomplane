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
}

func newFakeCloud(base string) *fakeCloud {
	return &fakeCloud{
		base: base, uploads: map[string][]byte{}, repos: map[string]bool{}, polls: map[string]int{},
		services: map[string]map[string]any{}, public: map[string]bool{}, failed: map[string]bool{},
		secrets: map[string][][]byte{}, readers: map[string][]string{},
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
		reply(map[string]any{"name": "acme-prod_loomplane"})
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
		f.public[strings.TrimSuffix(strings.TrimPrefix(p, runPrefix+"/"), ":setIamPolicy")] = true
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
		reply(200, map[string]any{"user": map[string]any{"username": "ada"}})
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
