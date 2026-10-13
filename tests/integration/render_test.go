//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// fakeRender stands in for Render's API. It refuses any other key, and
// answers the way Render's REST API does: lists of wrapped objects, a
// deploy that builds before it's live, and datastores that take a poll to
// become available.
type fakeRender struct {
	mu       sync.Mutex
	srv      *httptest.Server
	key      string
	services map[string]map[string]any
	env      map[string]map[string]string
	deploys  map[string][]string // service ID → commits deployed
	canceled []string
	polls    map[string]int
	stores   map[string]map[string]any
}

func newFakeRender(t *testing.T, key string) *fakeRender {
	t.Helper()
	f := &fakeRender{
		key: key, services: map[string]map[string]any{}, env: map[string]map[string]string{},
		deploys: map[string][]string{}, polls: map[string]int{}, stores: map[string]map[string]any{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRender) serve(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if strings.HasPrefix(r.URL.Path, "/site/") {
		w.WriteHeader(http.StatusOK) // a deployed public service, for the health check
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		reply(http.StatusUnauthorized, map[string]string{"message": "unauthorized"})
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/owners":
		reply(http.StatusOK, []any{map[string]any{"owner": map[string]string{"id": "tea-acme", "name": "Acme", "type": "team"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/services":
		out := []any{}
		for _, s := range f.services {
			if s["name"] == r.URL.Query().Get("name") {
				out = append(out, map[string]any{"service": s})
			}
		}
		reply(http.StatusOK, out)
	case r.Method == http.MethodPost && r.URL.Path == "/services":
		if body["ownerId"] != "tea-acme" || body["autoDeploy"] != "no" || body["repo"] != "https://github.com/acme/mono" {
			reply(http.StatusBadRequest, map[string]string{"message": fmt.Sprintf("unexpected service %v", body)})
			return
		}
		name := body["name"].(string)
		id := fmt.Sprintf("srv-%d", len(f.services)+1)
		s := map[string]any{"id": id, "name": name, "slug": name + "-x1", "type": body["type"], "repo": body["repo"],
			"rootDir": body["rootDir"], "spec": body["serviceDetails"], "serviceDetails": map[string]any{}}
		if body["type"] == "web_service" {
			s["serviceDetails"] = map[string]any{"url": f.srv.URL + "/site/" + name}
		}
		f.services[id] = s
		env := map[string]string{}
		for _, e := range body["envVars"].([]any) {
			kv := e.(map[string]any)
			env[kv["key"].(string)] = kv["value"].(string)
		}
		f.env[id] = env
		reply(http.StatusCreated, map[string]any{"service": s, "deployId": "dep-initial-" + id})
	case len(parts) == 2 && parts[0] == "services" && r.Method == http.MethodGet:
		s, ok := f.services[parts[1]]
		if !ok {
			reply(http.StatusNotFound, map[string]string{"message": "not found"})
			return
		}
		reply(http.StatusOK, s)
	case len(parts) == 2 && parts[0] == "services" && r.Method == http.MethodPatch:
		s := f.services[parts[1]]
		s["spec"], s["rootDir"] = body["serviceDetails"], body["rootDir"]
		reply(http.StatusOK, s)
	case len(parts) == 2 && parts[0] == "services" && r.Method == http.MethodDelete:
		delete(f.services, parts[1])
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 3 && parts[2] == "env-vars" && r.Method == http.MethodPut:
		// The body is a list, which the map decode above dropped.
		reply(http.StatusOK, []any{})
	case len(parts) == 3 && parts[2] == "deploys" && r.Method == http.MethodPost:
		commit, _ := body["commitId"].(string)
		f.deploys[parts[1]] = append(f.deploys[parts[1]], commit)
		reply(http.StatusCreated, map[string]any{"id": fmt.Sprintf("dep-%s-%d", parts[1], len(f.deploys[parts[1]])), "status": "build_in_progress"})
	case len(parts) == 3 && parts[2] == "deploys" && r.Method == http.MethodGet:
		n := len(f.deploys[parts[1]])
		reply(http.StatusOK, []any{map[string]any{"deploy": map[string]any{"id": fmt.Sprintf("dep-%s-%d", parts[1], n), "status": "live"}}})
	case len(parts) == 5 && parts[4] == "cancel":
		f.canceled = append(f.canceled, parts[3])
		reply(http.StatusOK, map[string]any{"id": parts[3], "status": "canceled"})
	case len(parts) == 4 && parts[2] == "deploys" && r.Method == http.MethodGet:
		f.polls[parts[3]]++
		status := "build_in_progress"
		if f.polls[parts[3]] > 1 {
			status = "live"
		}
		reply(http.StatusOK, map[string]any{"id": parts[3], "status": status})
	case (parts[0] == "postgres" || parts[0] == "key-value") && len(parts) == 1 && r.Method == http.MethodGet:
		out := []any{}
		for _, d := range f.stores {
			if d["name"] == r.URL.Query().Get("name") && d["kind"] == parts[0] {
				out = append(out, map[string]any{"postgres": d})
			}
		}
		reply(http.StatusOK, out)
	case (parts[0] == "postgres" || parts[0] == "key-value") && len(parts) == 1 && r.Method == http.MethodPost:
		id := "dpg-" + fmt.Sprint(len(f.stores)+1)
		d := map[string]any{"id": id, "name": body["name"], "status": "creating", "kind": parts[0], "version": body["version"], "region": body["region"]}
		f.stores[id] = d
		reply(http.StatusCreated, d)
	case (parts[0] == "postgres" || parts[0] == "key-value") && len(parts) == 2 && r.Method == http.MethodGet:
		d, ok := f.stores[parts[1]]
		if !ok {
			reply(http.StatusNotFound, map[string]string{"message": "not found"})
			return
		}
		d["status"] = "available"
		reply(http.StatusOK, d)
	case (parts[0] == "postgres" || parts[0] == "key-value") && len(parts) == 3 && parts[2] == "connection-info":
		reply(http.StatusOK, map[string]string{"internalConnectionString": "postgres://shop:pw@" + parts[1] + "-a/shop"})
	default:
		reply(http.StatusNotFound, map[string]string{"message": "no route " + r.Method + " " + r.URL.Path})
	}
}

const renderManifest = `version: 1
defaults:
  provider: render
services:
  gateway:
    source: apps/gateway
    needs: [api]
  api:
    source: services/api
    public: false
    needs: [db]
resources:
  db:
    kind: postgres
    version: "16"
ignore:
  resources: [redis, kafka]
`

func TestDeploy_aGatewayReachesItsPrivateAPIAndPostgresOnRender(t *testing.T) {
	const renderKey = "rnd_0123456789abcdef"
	rd := newFakeRender(t, renderKey)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.RenderEndpoint = rd.srv.URL
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

	// A wrong key is refused; the right one settles on its one workspace.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "render", "name": "Render", "credential": "rnd_wrong"}, nil)
	var conn struct {
		Status  string            `json:"status"`
		Account string            `json:"account"`
		Details map[string]string `json:"details"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "render", "name": "Render", "credential": renderKey}, &conn)
	if conn.Status != "ready" || conn.Account != "Acme" || conn.Details["owner_id"] != "tea-acme" {
		t.Fatalf("connection %+v", conn)
	}
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": renderManifest}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 3 {
		t.Fatalf("plan %+v", plan)
	}
	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" || len(d.Steps) != 3 {
		t.Fatalf("deployment %+v", d)
	}

	rd.mu.Lock()
	defer rd.mu.Unlock()
	byName := map[string]map[string]any{}
	ids := map[string]string{}
	for id, s := range rd.services {
		name := strings.SplitN(s["name"].(string), "-", 2)[0]
		byName[name], ids[name] = s, id
	}
	api, gw := byName["api"], byName["gateway"]
	if api == nil || gw == nil {
		t.Fatalf("services %+v", rd.services)
	}
	// The API is private, built from its Dockerfile; the gateway is a
	// public Node service started the way its package.json says.
	apiSpec, gwSpec := api["spec"].(map[string]any), gw["spec"].(map[string]any)
	if api["type"] != "private_service" || apiSpec["runtime"] != "docker" ||
		apiSpec["envSpecificDetails"].(map[string]any)["dockerfilePath"] != "./services/api/Dockerfile" {
		t.Fatalf("api %+v", api)
	}
	if gw["type"] != "web_service" || gwSpec["runtime"] != "node" || gw["rootDir"] != "apps/gateway" ||
		gwSpec["envSpecificDetails"].(map[string]any)["startCommand"] != "npm start" {
		t.Fatalf("gateway %+v", gw)
	}
	// Render's own first deploy of each was cancelled, and each ran the
	// commit the plan approved instead.
	if len(rd.canceled) != 2 {
		t.Fatalf("canceled %v", rd.canceled)
	}
	for name, id := range ids {
		if commits := rd.deploys[id]; len(commits) != 1 || len(commits[0]) != 40 {
			t.Fatalf("%s deployed %v", name, commits)
		}
	}
	// The API got the database's private address; the gateway got the
	// API's name on Render's private network.
	if !strings.HasPrefix(rd.env[ids["api"]]["DATABASE_URL"], "postgres://shop:pw@dpg-1-a/") {
		t.Fatalf("api env %v", rd.env[ids["api"]])
	}
	if want := "http://" + api["slug"].(string) + ":8080"; rd.env[ids["gateway"]]["API_URL"] != want {
		t.Fatalf("gateway env %v, want API_URL %s", rd.env[ids["gateway"]], want)
	}
	if db := rd.stores["dpg-1"]; db["version"] != "16" || db["region"] != "oregon" {
		t.Fatalf("database %+v", db)
	}
}
