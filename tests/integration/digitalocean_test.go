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

// fakeDigitalOcean stands in for DigitalOcean's API. It refuses any other
// token, and answers the way App Platform does: an app starts a
// deployment of its own when it's made, every deployment builds whatever
// commit tip says the branch is at, and databases take a poll to come
// online.
type fakeDigitalOcean struct {
	mu        sync.Mutex
	srv       *httptest.Server
	token     string
	tip       string
	apps      map[string]map[string]any // app ID → spec
	canceled  []string
	stopped   map[string]bool // cancelled deployments, which stay cancelled
	started   []string
	polls     map[string]int
	databases map[string]map[string]any
}

func newFakeDigitalOcean(t *testing.T, token string) *fakeDigitalOcean {
	t.Helper()
	f := &fakeDigitalOcean{token: token, tip: exampleSHA, apps: map[string]map[string]any{}, stopped: map[string]bool{}, polls: map[string]int{}, databases: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDigitalOcean) serve(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if strings.HasPrefix(r.URL.Path, "/site/") {
		w.WriteHeader(http.StatusOK) // a deployed public app, for the health check
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		reply(http.StatusUnauthorized, map[string]string{"id": "Unauthorized", "message": "Unable to authenticate you"})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	app := func(id string) map[string]any {
		return map[string]any{"id": id, "spec": f.apps[id], "live_url": f.srv.URL + "/site/" + id}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/account":
		reply(http.StatusOK, map[string]any{"account": map[string]any{"uuid": "acct-1", "email": "ada@example.com", "status": "active",
			"team": map[string]any{"uuid": "team-acme", "name": "Acme"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/v2/apps":
		out := []any{}
		for id := range f.apps {
			out = append(out, app(id))
		}
		reply(http.StatusOK, map[string]any{"apps": out, "links": map[string]any{}})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/apps":
		spec := body["spec"].(map[string]any)
		id := "app-" + spec["name"].(string)
		f.apps[id] = spec
		a := app(id)
		a["pending_deployment"] = map[string]any{"id": "own-" + id}
		reply(http.StatusOK, map[string]any{"app": a})
	case len(parts) == 3 && parts[1] == "apps" && r.Method == http.MethodGet:
		if f.apps[parts[2]] == nil {
			reply(http.StatusNotFound, map[string]string{"id": "not_found", "message": "not found"})
			return
		}
		reply(http.StatusOK, map[string]any{"app": app(parts[2])})
	case len(parts) == 3 && parts[1] == "apps" && r.Method == http.MethodPut:
		f.apps[parts[2]] = body["spec"].(map[string]any)
		reply(http.StatusOK, map[string]any{"app": app(parts[2])})
	case len(parts) == 4 && parts[3] == "deployments" && r.Method == http.MethodPost:
		dep := fmt.Sprintf("dep-%s-%d", parts[2], len(f.started)+1)
		f.started = append(f.started, dep)
		reply(http.StatusOK, map[string]any{"deployment": map[string]any{"id": dep, "phase": "PENDING_BUILD"}})
	case len(parts) == 5 && parts[3] == "deployments" && r.Method == http.MethodGet:
		f.polls[parts[4]]++
		phase, hash := "PENDING_BUILD", ""
		if f.polls[parts[4]] > 1 {
			phase, hash = "BUILDING", f.tip
		}
		if f.polls[parts[4]] > 2 {
			phase = "ACTIVE"
		}
		if f.stopped[parts[4]] {
			phase = "CANCELED"
		}
		comp := []any{map[string]any{"name": "x", "source_commit_hash": hash}}
		reply(http.StatusOK, map[string]any{"deployment": map[string]any{"id": parts[4], "phase": phase, "services": comp}})
	case len(parts) == 6 && parts[5] == "cancel":
		f.canceled, f.stopped[parts[4]] = append(f.canceled, parts[4]), true
		reply(http.StatusOK, map[string]any{"deployment": map[string]any{"id": parts[4], "phase": "CANCELED"}})
	case r.Method == http.MethodGet && r.URL.Path == "/v2/databases":
		out := []any{}
		for _, d := range f.databases {
			out = append(out, d)
		}
		reply(http.StatusOK, map[string]any{"databases": out})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/databases":
		id := fmt.Sprintf("db-%d", len(f.databases)+1)
		d := map[string]any{"id": id, "name": body["name"], "engine": body["engine"], "version": body["version"],
			"region": body["region"], "size": body["size"], "status": "creating"}
		f.databases[id] = d
		reply(http.StatusCreated, map[string]any{"database": d})
	case len(parts) == 3 && parts[1] == "databases" && r.Method == http.MethodGet:
		d, ok := f.databases[parts[2]]
		if !ok {
			reply(http.StatusNotFound, map[string]string{"id": "not_found", "message": "not found"})
			return
		}
		d["status"] = "online"
		d["connection"] = map[string]any{"uri": "postgresql://doadmin:pw@" + parts[2] + ".db.ondigitalocean.com:25060/defaultdb?sslmode=require"}
		reply(http.StatusOK, map[string]any{"database": d})
	default:
		reply(http.StatusNotFound, map[string]string{"id": "not_found", "message": "no route " + r.Method + " " + r.URL.Path})
	}
}

const digitalOceanManifest = `version: 1
defaults:
  provider: digitalocean
  region: ams
services:
  gateway:
    source: apps/gateway
    needs: [api]
  api:
    source: services/api
    needs: [db]
resources:
  db:
    kind: postgres
    version: "16"
ignore:
  resources: [redis, kafka]
`

// digitalOceanProject signs up, makes a project of acme/mono, analyses it
// and connects DigitalOcean, returning the client and the project's base
// path.
func digitalOceanProject(t *testing.T, do *fakeDigitalOcean, token string) (*client, string, func()) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.DigitalOceanEndpoint = do.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)

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

	// A wrong token is refused; the right one settles on its team.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "digitalocean", "name": "DigitalOcean", "credential": "dop_v1_wrong"}, nil)
	var conn struct {
		Status  string            `json:"status"`
		Account string            `json:"account"`
		Details map[string]string `json:"details"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "digitalocean", "name": "DigitalOcean", "credential": token}, &conn)
	if conn.Status != "ready" || conn.Account != "Acme" || conn.Details["team_uuid"] != "team-acme" {
		t.Fatalf("connection %+v", conn)
	}
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": digitalOceanManifest}, nil)
	return c, base, r.stop
}

func TestDeploy_aGatewayAndItsAPIWithPostgresOnAppPlatform(t *testing.T) {
	const token = "dop_v1_0123456789abcdef"
	do := newFakeDigitalOcean(t, token)
	c, base, stop := digitalOceanProject(t, do, token)
	defer stop()

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

	do.mu.Lock()
	defer do.mu.Unlock()
	specs := map[string]map[string]any{}
	for _, spec := range do.apps {
		specs[strings.SplitN(spec["name"].(string), "-", 2)[0]] = spec
	}
	api, gw := specs["api"], specs["gateway"]
	if api == nil || gw == nil {
		t.Fatalf("apps %+v", do.apps)
	}
	apiSvc := api["services"].([]any)[0].(map[string]any)
	gwSvc := gw["services"].([]any)[0].(map[string]any)
	// Both build main of acme/mono, with deploy on push off, in Amsterdam:
	// the API from its Dockerfile, the gateway with App Platform's
	// buildpacks.
	for name, svc := range map[string]map[string]any{"api": apiSvc, "gateway": gwSvc} {
		gh := svc["github"].(map[string]any)
		if gh["repo"] != "acme/mono" || gh["branch"] != "main" || gh["deploy_on_push"] != false {
			t.Fatalf("%s source %v", name, gh)
		}
	}
	if api["region"] != "ams" || apiSvc["dockerfile_path"] != "services/api/Dockerfile" || apiSvc["http_port"] == nil {
		t.Fatalf("api %v", api)
	}
	if gwSvc["dockerfile_path"] != nil || gwSvc["source_dir"] != "/apps/gateway" {
		t.Fatalf("gateway %v", gwSvc)
	}
	// App Platform's own first deployment of each app was cancelled, and
	// the one Loomplane started ran the approved commit.
	if len(do.canceled) != 2 || len(do.started) != 2 {
		t.Fatalf("canceled %v, started %v", do.canceled, do.started)
	}
	// The API got the database's address, as a secret; the gateway got
	// the API's public URL.
	envs := func(svc map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, e := range svc["envs"].([]any) {
			out[e.(map[string]any)["key"].(string)] = e.(map[string]any)
		}
		return out
	}
	if dbURL := envs(apiSvc)["DATABASE_URL"]; dbURL == nil || dbURL["type"] != "SECRET" || !strings.HasPrefix(dbURL["value"].(string), "postgresql://doadmin:pw@db-1.") {
		t.Fatalf("api env %v", envs(apiSvc))
	}
	if apiURL := envs(gwSvc)["API_URL"]; apiURL == nil || !strings.HasPrefix(apiURL["value"].(string), do.srv.URL+"/site/app-api-") {
		t.Fatalf("gateway env %v", envs(gwSvc))
	}
	if db := do.databases["db-1"]; db["engine"] != "pg" || db["version"] != "16" || db["region"] != "ams3" {
		t.Fatalf("database %+v", db)
	}
}

func TestDeploy_appPlatformRefusesACommitPushedAfterApproval(t *testing.T) {
	const token = "dop_v1_0123456789abcdef"
	do := newFakeDigitalOcean(t, token)
	c, base, stop := digitalOceanProject(t, do, token)
	defer stop()

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	// Someone pushes to main between the plan and its approval.
	do.mu.Lock()
	do.tip = "0000000000000000000000000000000000000001"
	do.mu.Unlock()
	d := approveAndWait(t, c, plan)
	if d.Status != "failed" {
		t.Fatalf("deployment %+v", d)
	}
	failed := ""
	for _, s := range d.Steps {
		if s.Status == "failed" {
			failed = s.Message
		}
	}
	if !strings.Contains(failed, "not the approved commit") || !strings.Contains(failed, "cancelled it before it went live") || strings.Contains(failed, "deleted") {
		t.Fatalf("failed step says %q (steps %+v)", failed, d.Steps)
	}
	do.mu.Lock()
	defer do.mu.Unlock()
	// The deployment Loomplane started was cancelled, along with App
	// Platform's own.
	for _, dep := range do.started {
		found := false
		for _, cancelled := range do.canceled {
			found = found || cancelled == dep
		}
		if !found {
			t.Fatalf("deployment %s wasn't cancelled (canceled %v)", dep, do.canceled)
		}
	}
}
