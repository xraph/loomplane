package deployments

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

const doApproved = "0123456789abcdef0123456789abcdef01234567"

// fakeAppPlatform is just enough of DigitalOcean's API for the executor:
// one account's apps, whose deployments build whatever commit tip says
// the branch is at, and its databases.
type fakeAppPlatform struct {
	mu sync.Mutex
	// tip is the commit App Platform builds; liveAt is the phase at which
	// a deployment first reports it, BUILDING or ACTIVE.
	tip, liveAt string
	apps        map[string]map[string]any
	active      map[string]string // app ID → live deployment ID
	specs       []map[string]any
	created     []string // deployments Apply started
	canceled    []string
	rollbacks   []string
	deleted     []string
	databases   map[string]map[string]any
}

func newFakeAppPlatform(t *testing.T) (*fakeAppPlatform, *digitalocean.Client) {
	t.Helper()
	f := &fakeAppPlatform{tip: doApproved, liveAt: "BUILDING", apps: map[string]map[string]any{}, active: map[string]string{}, databases: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, digitalocean.New(srv.URL)
}

func (f *fakeAppPlatform) serve(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	app := func(id string) map[string]any {
		a := map[string]any{"id": id, "spec": f.apps[id], "live_url": "https://" + id + ".ondigitalocean.app"}
		if dep := f.active[id]; dep != "" {
			a["active_deployment"] = map[string]any{"id": dep, "phase": "ACTIVE"}
		}
		return a
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/apps":
		out := make([]any, 0, len(f.apps))
		for id := range f.apps {
			out = append(out, app(id))
		}
		reply(200, map[string]any{"apps": out})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/apps":
		spec := body["spec"].(map[string]any)
		id := "app-" + spec["name"].(string)
		f.apps[id], f.specs = spec, append(f.specs, spec)
		a := app(id)
		a["pending_deployment"] = map[string]any{"id": "own-" + id}
		reply(200, map[string]any{"app": a})
	case len(parts) == 3 && parts[1] == "apps" && r.Method == http.MethodPut:
		f.apps[parts[2]] = body["spec"].(map[string]any)
		f.specs = append(f.specs, f.apps[parts[2]])
		reply(200, map[string]any{"app": app(parts[2])})
	case len(parts) == 3 && parts[1] == "apps" && r.Method == http.MethodGet:
		if f.apps[parts[2]] == nil {
			reply(404, map[string]string{"id": "not_found", "message": "not found"})
			return
		}
		reply(200, map[string]any{"app": app(parts[2])})
	case len(parts) == 3 && parts[1] == "apps" && r.Method == http.MethodDelete:
		f.deleted = append(f.deleted, parts[2])
		delete(f.apps, parts[2])
		reply(200, map[string]any{"id": parts[2]})
	case len(parts) == 4 && parts[3] == "deployments" && r.Method == http.MethodPost:
		dep := "dep-" + parts[2]
		f.created = append(f.created, dep)
		reply(200, map[string]any{"deployment": map[string]any{"id": dep, "phase": "PENDING_BUILD"}})
	case len(parts) == 5 && parts[3] == "deployments" && r.Method == http.MethodGet:
		phase, hash := "BUILDING", f.tip
		if f.liveAt == "ACTIVE" {
			phase, hash = "ACTIVE", f.tip
			f.active[parts[2]] = parts[4]
		}
		if hash == doApproved && f.liveAt == "BUILDING" {
			phase = "ACTIVE"
			f.active[parts[2]] = parts[4]
		}
		reply(200, map[string]any{"deployment": map[string]any{"id": parts[4], "phase": phase,
			"services": []any{map[string]any{"name": "api", "source_commit_hash": hash}}}})
	case len(parts) == 6 && parts[5] == "cancel":
		f.canceled = append(f.canceled, parts[4])
		reply(200, map[string]any{"deployment": map[string]any{"id": parts[4], "phase": "CANCELED"}})
	case len(parts) == 4 && parts[3] == "rollback":
		f.rollbacks = append(f.rollbacks, body["deployment_id"].(string))
		reply(200, map[string]any{"deployment": map[string]any{"id": "rb"}})
	case r.Method == http.MethodGet && r.URL.Path == "/v2/databases":
		out := make([]any, 0, len(f.databases))
		for _, d := range f.databases {
			out = append(out, d)
		}
		reply(200, map[string]any{"databases": out})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/databases":
		id := "db-" + body["name"].(string)
		body["id"], body["status"] = id, "online"
		body["connection"] = map[string]any{"uri": "rediss://default:pw@" + id + ".db.ondigitalocean.com:25061"}
		f.databases[id] = body
		reply(201, map[string]any{"database": body})
	case len(parts) == 3 && parts[1] == "databases" && r.Method == http.MethodGet:
		if d := f.databases[parts[2]]; d != nil {
			reply(200, map[string]any{"database": d})
			return
		}
		reply(404, map[string]string{"id": "not_found", "message": "not found"})
	case len(parts) == 3 && parts[1] == "databases" && r.Method == http.MethodDelete:
		f.deleted = append(f.deleted, parts[2])
		delete(f.databases, parts[2])
		w.WriteHeader(http.StatusNoContent)
	default:
		reply(404, map[string]string{"id": "not_found", "message": "no route " + r.Method + " " + r.URL.Path})
	}
}

// newDO is an executor whose apps' addresses are in DNS at once.
func newDO(client *digitalocean.Client) *DigitalOcean {
	return &DigitalOcean{Client: client, Resolve: func(context.Context, string) error { return nil }}
}

func doJob(cfg *planning.ServiceConfig, ref string) *Job {
	return &Job{
		Deployment: &Deployment{OrgID: "org", ProjectID: "proj_0123456789", Environment: "production",
			Sources: []SourceArtifact{{Alias: "mono", Repository: "acme/mono", CommitSHA: doApproved}}},
		Plan:       &planning.Plan{Sources: []planning.PlanSource{{Alias: "mono", Repository: "acme/mono", Ref: ref, CommitSHA: doApproved}}},
		Action:     planning.Action{ID: "service:api", Name: "api", Service: cfg, Region: "ams"},
		Credential: []byte("dop_v1_token\n"), CommitSHA: doApproved,
		Env:        map[string]string{"DATABASE_URL": "postgres://secret", "LOG_LEVEL": "info"},
		Secrets:    map[string]bool{"DATABASE_URL": true},
		Checkpoint: func([]byte) error { return nil }, Progress: func(string, string) {},
	}
}

func apiConfig() *planning.ServiceConfig {
	return &planning.ServiceConfig{Source: "services/api", Image: "services/api/Dockerfile", Port: 8080, Public: true,
		Workload: provider.HTTPService, Execution: provider.Container, Size: &manifest.Size{CPU: 1, Memory: 1024}, Scale: &manifest.Scale{Min: 2, Max: 2}}
}

func TestDigitalOcean_deploysTheApprovedCommitFromItsBranch(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	out, err := newDO(client).Apply(t.Context(), doJob(apiConfig(), "release"))
	if err != nil {
		t.Fatal(err)
	}
	if out.ExternalID != "app-api-production-23456789" || out.Revision != "dep-app-api-production-23456789" || out.URL != "https://app-api-production-23456789.ondigitalocean.app" {
		t.Fatalf("outcome %+v", out)
	}
	spec := f.specs[0]
	svc := spec["services"].([]any)[0].(map[string]any)
	gh := svc["github"].(map[string]any)
	if spec["region"] != "ams" || gh["repo"] != "acme/mono" || gh["branch"] != "release" || gh["deploy_on_push"] != false {
		t.Fatalf("spec %v", spec)
	}
	if svc["dockerfile_path"] != "services/api/Dockerfile" || svc["source_dir"] != "/services/api" || svc["http_port"] != float64(8080) ||
		svc["instance_size_slug"] != "apps-s-1vcpu-1gb" || svc["instance_count"] != float64(2) {
		t.Fatalf("service %v", svc)
	}
	envs := map[string]map[string]any{}
	for _, e := range svc["envs"].([]any) {
		envs[e.(map[string]any)["key"].(string)] = e.(map[string]any)
	}
	if envs["DATABASE_URL"]["type"] != "SECRET" || envs["LOG_LEVEL"]["type"] != "GENERAL" || envs["PORT"]["value"] != "8080" {
		t.Fatalf("envs %v", envs)
	}
	// App Platform's own deployment of the new app was cancelled; the one
	// that ran is the one Loomplane started and checked.
	if len(f.canceled) != 1 || !strings.HasPrefix(f.canceled[0], "own-") || len(f.created) != 1 {
		t.Fatalf("canceled %v, created %v", f.canceled, f.created)
	}
}

func TestDigitalOcean_aSourceWithoutABranchBuildsMain(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	if _, err := newDO(client).Apply(t.Context(), doJob(apiConfig(), "")); err != nil {
		t.Fatal(err)
	}
	gh := f.specs[0]["services"].([]any)[0].(map[string]any)["github"].(map[string]any)
	if gh["branch"] != "main" {
		t.Fatalf("branch %v", gh["branch"])
	}
}

func TestDigitalOcean_refusesACommitNobodyApproved(t *testing.T) {
	const pushed = "ffffffffffffffffffffffffffffffffffffffff"
	t.Run("cancelled while it builds", func(t *testing.T) {
		f, client := newFakeAppPlatform(t)
		f.tip = pushed
		_, err := newDO(client).Apply(t.Context(), doJob(apiConfig(), "main"))
		if err == nil || !strings.Contains(err.Error(), "cancelled it before it went live") || !strings.Contains(err.Error(), "ffffffffffff") {
			t.Fatalf("err %v", err)
		}
		if len(f.canceled) != 2 || f.canceled[1] != f.created[0] || len(f.rollbacks)+len(f.deleted) != 0 {
			t.Fatalf("canceled %v rollbacks %v deleted %v", f.canceled, f.rollbacks, f.deleted)
		}
	})
	t.Run("rolled back when it went live", func(t *testing.T) {
		f, client := newFakeAppPlatform(t)
		job := doJob(apiConfig(), "main")
		name := DigitalOceanName(job)
		f.apps["app-"+name] = map[string]any{"name": name}
		f.active["app-"+name] = "dep-approved-before"
		f.tip, f.liveAt = pushed, "ACTIVE"
		job.Binding = &planning.Binding{ExternalID: "app-" + name}
		_, err := newDO(client).Apply(t.Context(), job)
		if err == nil || !strings.Contains(err.Error(), "rolled the app back") {
			t.Fatalf("err %v", err)
		}
		if len(f.rollbacks) != 1 || f.rollbacks[0] != "dep-approved-before" || len(f.deleted) != 0 {
			t.Fatalf("rollbacks %v deleted %v", f.rollbacks, f.deleted)
		}
	})
	t.Run("a new app it went live on is deleted", func(t *testing.T) {
		f, client := newFakeAppPlatform(t)
		f.tip, f.liveAt = pushed, "ACTIVE"
		_, err := newDO(client).Apply(t.Context(), doJob(apiConfig(), "main"))
		if err == nil || !strings.Contains(err.Error(), "deleted it") {
			t.Fatalf("err %v", err)
		}
		if len(f.deleted) != 1 || len(f.apps) != 0 {
			t.Fatalf("deleted %v apps %v", f.deleted, f.apps)
		}
	})
}

func TestDigitalOcean_workersPrivateServicesAndStaticSites(t *testing.T) {
	job := doJob(&planning.ServiceConfig{Source: "worker", Ecosystem: "node", Start: "node worker.js", Workload: provider.Worker, Execution: provider.Container}, "")
	spec := doAppSpec(job, job.Action.Service, "acme/mono", "main")
	if len(spec.Workers) != 1 || spec.Workers[0].RunCommand != "node worker.js" || spec.Workers[0].SourceDir != "/worker" || len(spec.Services) != 0 {
		t.Fatalf("worker %+v", spec)
	}
	job.Action.Service = &planning.ServiceConfig{Source: "api", Ecosystem: "go", Port: 9000, Workload: provider.HTTPService, Execution: provider.Container}
	spec = doAppSpec(job, job.Action.Service, "acme/mono", "main")
	if svc := spec.Services[0]; svc.HTTPPort != 0 || len(svc.InternalPorts) != 1 || svc.InternalPorts[0] != 9000 {
		t.Fatalf("private service %+v", svc)
	}
	if doAddress(job.Action.Service, &digitalocean.App{LiveURL: "https://x"}) != "" {
		t.Fatal("a private service got a public address")
	}
	job.Action.Service = &planning.ServiceConfig{Source: "web", Framework: "next", Workload: provider.StaticSite, Execution: provider.Static}
	spec = doAppSpec(job, job.Action.Service, "acme/mono", "main")
	if len(spec.StaticSites) != 1 || spec.StaticSites[0].OutputDir != "out" || spec.StaticSites[0].InstanceSizeSlug != "" {
		t.Fatalf("static site %+v", spec)
	}
}

func TestDigitalOcean_aDockerfileThatBuildsFromTheRoot(t *testing.T) {
	// Loomplane's own: deployments/docker/Dockerfile, built from the root.
	cfg := &planning.ServiceConfig{Source: ".", Image: "deployments/docker/Dockerfile", Context: ".", Port: 8780, Public: true,
		Workload: provider.HTTPService, Execution: provider.Container}
	svc := doAppSpec(doJob(cfg, ""), cfg, "xraph/loomplane", "main").Services[0]
	if svc.SourceDir != "/" || svc.DockerfilePath != "deployments/docker/Dockerfile" || svc.HTTPPort != 8780 {
		t.Fatalf("service %+v", svc)
	}
}

func TestDigitalOceanName_fitsAppPlatform(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)
	a := doName("checkout-service-with-a-long-name-staging-0123abcd")
	b := doName("checkout-service-with-a-long-name-preview-0123abcd")
	for _, n := range []string{a, b, doName("API_Gateway"), doName("9lives-prod"), doName("x")} {
		if !valid.MatchString(n) && n != "x" {
			t.Errorf("%q isn't a valid App Platform name", n)
		}
	}
	if a == b || len(a) > doNameMax {
		t.Fatalf("long names %q %q", a, b)
	}
	if doName("API_Gateway") != "api-gateway" || doName("9lives-prod") != "lp-9lives-prod" {
		t.Fatalf("got %q %q", doName("API_Gateway"), doName("9lives-prod"))
	}
}

func TestDigitalOcean_redisIsValkeyInTheRegionsDatacenter(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	kept := map[string]string{}
	job := doJob(nil, "")
	job.Action = planning.Action{ID: "resource:cache", Name: "cache", Resource: &planning.ResourceConfig{Kind: "redis", Version: "7", Region: "fra"}}
	job.Keep = func(_ context.Context, field string, v []byte) error { kept[field] = string(v); return nil }
	d := newDO(client)
	out, err := d.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	db := f.databases[strings.TrimPrefix(out.ExternalID, "db/")]
	if db == nil || db["engine"] != "valkey" || db["region"] != "fra1" || db["version"] != nil || db["size"] != "db-s-1vcpu-1gb" {
		t.Fatalf("database %v (outcome %+v)", db, out)
	}
	if !strings.HasPrefix(kept[ResourceURL], "rediss://") {
		t.Fatalf("kept %v", kept)
	}
	obs, err := d.Observe(t.Context(), nil, job.Credential, &planning.Binding{ExternalID: out.ExternalID})
	if err != nil || !obs.Exists || obs.Revision != "valkey" {
		t.Fatalf("observe %+v, %v", obs, err)
	}
	forgot := false
	job.Forget = func(context.Context) error { forgot = true; return nil }
	if err := d.Remove(t.Context(), job, &planning.Binding{ExternalID: out.ExternalID}); err != nil || !forgot || len(f.databases) != 0 {
		t.Fatalf("remove %v forgot %v databases %v", err, forgot, f.databases)
	}
	if obs, err := d.Observe(t.Context(), nil, job.Credential, &planning.Binding{ExternalID: "app-gone"}); err != nil || obs.Exists {
		t.Fatalf("observe a deleted app %+v, %v", obs, err)
	}
}

func TestDigitalOcean_waitsForANewAppsAddressInDNS(t *testing.T) {
	notYet := &net.DNSError{Err: "no such host", IsNotFound: true}
	t.Run("returns once the name resolves", func(t *testing.T) {
		_, client := newFakeAppPlatform(t)
		var asked []string
		d := &DigitalOcean{Client: client, DNSPoll: time.Millisecond, Resolve: func(_ context.Context, host string) error {
			asked = append(asked, host)
			if len(asked) < 3 {
				return notYet
			}
			return nil
		}}
		var said []string
		job := doJob(apiConfig(), "main")
		job.Progress = func(_, m string) { said = append(said, m) }
		out, err := d.Apply(t.Context(), job)
		if err != nil || out.URL == "" {
			t.Fatalf("apply: %+v, %v", out, err)
		}
		if len(asked) != 3 || asked[0] != strings.TrimPrefix(out.URL, "https://") {
			t.Fatalf("asked %v for %s", asked, out.URL)
		}
		if !slices.ContainsFunc(said, func(m string) bool { return strings.Contains(m, "publish") }) {
			t.Fatalf("progress %v", said)
		}
	})
	t.Run("gives up with a way forward", func(t *testing.T) {
		_, client := newFakeAppPlatform(t)
		d := &DigitalOcean{Client: client, DNSPoll: time.Millisecond, DNSWait: 20 * time.Millisecond,
			Resolve: func(context.Context, string) error { return notYet }}
		_, err := d.Apply(t.Context(), doJob(apiConfig(), "main"))
		if err == nil || !strings.Contains(err.Error(), "isn't in DNS") || !strings.Contains(err.Error(), "Retry") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("doesn't wait when public DNS is out of reach", func(t *testing.T) {
		_, client := newFakeAppPlatform(t)
		calls := 0
		d := &DigitalOcean{Client: client, DNSPoll: time.Millisecond, Resolve: func(context.Context, string) error {
			calls++
			return &net.DNSError{Err: "i/o timeout", IsTimeout: true}
		}}
		if _, err := d.Apply(t.Context(), doJob(apiConfig(), "main")); err != nil || calls != 1 {
			t.Fatalf("err %v after %d lookups", err, calls)
		}
	})
	t.Run("an address without a name needs no lookup", func(t *testing.T) {
		d := &DigitalOcean{Resolve: func(context.Context, string) error { t.Fatal("looked up an IP"); return nil }}
		for _, u := range []string{"", "http://127.0.0.1:1234/site/app", "http://localhost:8080"} {
			if err := d.waitForDNS(t.Context(), doJob(apiConfig(), "main"), u); err != nil {
				t.Fatalf("%q: %v", u, err)
			}
		}
	})
}
