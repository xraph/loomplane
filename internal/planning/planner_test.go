package planning

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

var (
	exampleOnce  sync.Once
	exampleGraph *graph.Graph
	errExample   error
)

// polyglot is the application graph of examples/polyglot.
func polyglot(t *testing.T) *graph.Graph {
	t.Helper()
	exampleOnce.Do(func() {
		r, err := discovery.Analyze(context.Background(), os.DirFS("../../examples/polyglot"))
		if err != nil {
			errExample = err
			return
		}
		exampleGraph = graph.Build(r)
	})
	if errExample != nil {
		t.Fatal(errExample)
	}
	return exampleGraph
}

const acme = `version: 1
defaults:
  provider: cloud-run
services:
  web:
    source: apps/web
    provider: vercel
    needs: [api]
  api:
    source: services/api
    needs: [db]
    scale: {min: 1, max: 3}
  gateway:
    source: apps/gateway
    needs: [cache, api]
  worker:
    source: services/worker/cmd/worker
    provider: ecs-fargate
    environments:
      preview:
        enabled: false
resources:
  db:
    kind: postgres
    provider: cloud-sql
  cache:
    kind: redis
    provider: memorystore
`

func parse(t *testing.T, yaml string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// connected is an organization with one ready connection to each cloud,
// so tests about the plan itself never stop at choosing an account.
var connected = []ConnectionOption{
	{ID: "conn_gcp", Name: "Prod", Cloud: "gcp", Ready: true},
	{ID: "conn_vercel", Name: "Web", Cloud: "vercel", Ready: true},
	{ID: "conn_aws", Name: "AWS", Cloud: "aws", Ready: true},
	{ID: "conn_render", Name: "Render", Cloud: "render", Ready: true},
}

func compute(t *testing.T, yaml, env string, bindings ...*Binding) *Result {
	t.Helper()
	return Compute(Input{
		Environment: env, Graph: polyglot(t), Manifest: parse(t, yaml),
		Providers: providers.Registry(), Bindings: bindings, Connections: connected,
	})
}

func action(t *testing.T, r *Result, id string) Action {
	t.Helper()
	for _, a := range r.Actions {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no action %s in %+v", id, r.Actions)
	return Action{}
}

func codes(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code+":"+f.Name)
	}
	return out
}

func TestCompute_fresh(t *testing.T) {
	r := compute(t, acme, "production")
	if len(r.Problems) > 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	if r.Executable {
		t.Error("executable, though no provider has an adapter yet")
	}
	if r.Summary != (Summary{Create: 6}) {
		t.Errorf("summary = %+v", r.Summary)
	}

	order := make([]string, 0, len(r.Actions))
	for _, a := range r.Actions {
		order = append(order, a.ID)
	}
	// Resources first, then each service after what it needs.
	want := []string{"resource:cache", "resource:db", "service:api", "service:worker", "service:gateway", "service:web"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}

	api := action(t, r, "service:api")
	if api.Engine != provider.CtrlPlane || api.Creates != "Cloud Run service" || api.Region != "us-central1" || api.Step != 2 {
		t.Errorf("api = %+v", api)
	}
	if api.Service.Port != 8080 || api.Service.Image != "services/api/Dockerfile" || api.Service.Scale.Max != 3 {
		t.Errorf("api config = %+v", api.Service)
	}
	if len(api.Service.Env) != 1 || api.Service.Env[0] != (EnvLink{Key: "DATABASE_URL", From: "db", Subject: ResourceSubject}) {
		t.Errorf("api env = %+v", api.Service.Env)
	}

	web := action(t, r, "service:web")
	if web.Engine != provider.Loomplane || web.Service.Execution != provider.Serverless || web.Region != "" || web.Service.Port != 0 {
		t.Errorf("web = %+v", web)
	}
	if web.Step != 3 || !slices.Equal(web.DependsOn, []string{"service:api"}) {
		t.Errorf("web runs at step %d after %v", web.Step, web.DependsOn)
	}

	gateway := action(t, r, "service:gateway")
	if !slices.Equal(gateway.DependsOn, []string{"resource:cache", "service:api"}) {
		t.Errorf("gateway depends on %v", gateway.DependsOn)
	}
	keys := []string{gateway.Service.Env[0].Key, gateway.Service.Env[1].Key}
	if !slices.Equal(keys, []string{"API_URL", "REDIS_URL"}) {
		t.Errorf("gateway env = %+v", gateway.Service.Env)
	}

	worker := action(t, r, "service:worker")
	if worker.Service.Public || worker.Service.Port != 0 || worker.Creates != "ECS service on Fargate" {
		t.Errorf("worker = %+v", worker.Service)
	}
	db := action(t, r, "resource:db")
	if db.Engine != provider.Loomplane || db.Resource.Version != "17" || db.Creates != "Cloud SQL for PostgreSQL instance" {
		t.Errorf("db = %+v", db)
	}

	got := codes(r.Warnings)
	for _, w := range []string{
		// The gateway's Dockerfile-less Node server has no port on record.
		"port-assumed:gateway",
		// Analysis found these, and the manifest leaves them out.
		"service-left-out:docs", "service-left-out:ingest", "service-left-out:backfill",
		// Cloud Run, Vercel, Cloud SQL and ECS have adapters; Memorystore
		// doesn't yet.
		"provider-unavailable:memorystore",
		// The worker uses Redis per analysis, but the cache is on another
		// cloud, so it needs nothing.
		"unlinked-resource:worker",
	} {
		if !slices.Contains(got, w) {
			t.Errorf("no warning %s in %v", w, got)
		}
	}
	for _, ok := range []string{"provider-unavailable:cloud-run", "provider-unavailable:vercel", "provider-unavailable:cloud-sql", "provider-unavailable:ecs-fargate"} {
		if slices.Contains(got, ok) {
			t.Errorf("%s has an adapter, but was warned about: %v", ok, got)
		}
	}
	// web uses Postgres per analysis but only needs api: worth saying.
	if !slices.Contains(got, "unlinked-resource:web") {
		t.Errorf("web's Postgres use went unmentioned: %v", got)
	}
}

func TestCompute_environmentOverrides(t *testing.T) {
	r := compute(t, acme, "preview")
	for _, a := range r.Actions {
		if a.Name == "worker" {
			t.Errorf("worker is off in preview, but planned: %+v", a)
		}
	}
}

func TestCompute_problems(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"worker on cloud run", "version: 1\nservices:\n  w: {source: services/worker/cmd/worker, provider: cloud-run}", "provider-cant-run:w"},
		{"no provider", "version: 1\nservices:\n  api: {source: services/api}", "no-provider:api"},
		{"unknown provider", "version: 1\nservices:\n  api: {source: services/api, provider: heroku}", "unknown-provider:api"},
		{"missing source", "version: 1\nservices:\n  api: {source: services/billing, provider: cloud-run}", "service-not-found:api"},
		{"shared directory", "version: 1\nservices:\n  ingest: {source: crates/ingest, provider: render}", "ambiguous-source:ingest"},
		{"vercel can't provision", "version: 1\nresources:\n  cache: {kind: redis, provider: vercel}", "provider-cant-provision:cache"},
		{"nobody provisions kafka", "version: 1\nresources:\n  bus: {kind: kafka, provider: render}", "provider-cant-provision:bus"},
		{"unknown version", "version: 1\nresources:\n  db: {kind: postgres, provider: rds, version: '9.6'}", "unknown-version:db"},
		{"needs a worker", "version: 1\ndefaults: {provider: render}\nservices:\n  api: {source: services/api, needs: [w]}\n  w: {source: services/worker/cmd/worker}", "need-worker:api"},
		{"needs something off", "version: 1\ndefaults: {provider: render}\nservices:\n  api: {source: services/api, needs: [db]}\nresources:\n  db:\n    kind: postgres\n    environments: {production: {enabled: false}}", "need-off:api"},
		{"private across clouds", "version: 1\nservices:\n  web: {source: apps/web, provider: vercel, needs: [api]}\n  api: {source: services/api, provider: ecs-fargate, public: false}", "private-across-clouds:web"},
		{"private with no network", "version: 1\ndefaults: {provider: render}\nservices:\n  gw: {source: apps/gateway, needs: [api]}\n  api: {source: services/api, public: false}", "private-unreachable:gw"},
		{"tool kind", "version: 1\nservices:\n  api: {source: services/api, provider: render, kind: worker}\n  w: {source: services/worker/cmd/worker, provider: cloud-run, kind: http-service}", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := compute(t, tt.yaml, "production")
			got := codes(r.Problems)
			if tt.want == "" {
				if len(got) > 0 {
					t.Fatalf("problems %v", got)
				}
				return
			}
			if !slices.Contains(got, tt.want) {
				t.Fatalf("problems %v, want %s", got, tt.want)
			}
			if r.Executable {
				t.Error("a plan with problems is executable")
			}
		})
	}
}

// bound records what a plan's action would leave deployed, the way a
// deployment will.
func bound(t *testing.T, a Action) *Binding {
	t.Helper()
	var cfg any = a.Service
	if a.Subject == ResourceSubject {
		cfg = a.Resource
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &Binding{
		ID: "bnd_" + a.Name, Subject: a.Subject, Name: a.Name, Provider: a.Provider, Region: a.Region,
		Engine: a.Engine, ExternalID: "ext-" + a.Name, Config: data,
	}
}

func deployed(t *testing.T, r *Result) []*Binding {
	t.Helper()
	out := make([]*Binding, 0, len(r.Actions))
	for _, a := range r.Actions {
		out = append(out, bound(t, a))
	}
	return out
}

func TestCompute_againstWhatsDeployed(t *testing.T) {
	first := compute(t, acme, "production")
	live := deployed(t, first)

	t.Run("unchanged", func(t *testing.T) {
		r := compute(t, acme, "production", live...)
		if r.Summary != (Summary{Keep: 6}) {
			t.Errorf("summary = %+v", r.Summary)
		}
	})

	t.Run("scaled", func(t *testing.T) {
		r := compute(t, strings.Replace(acme, "max: 3", "max: 5", 1), "production", live...)
		api := action(t, r, "service:api")
		if api.Op != Update || len(api.Changes) != 1 || api.Changes[0].Field != "scale" {
			t.Fatalf("api = %s %+v", api.Op, api.Changes)
		}
		if !strings.Contains(api.Changes[0].To, `"max":5`) {
			t.Errorf("change = %+v", api.Changes[0])
		}
	})

	t.Run("moved", func(t *testing.T) {
		yaml := strings.Replace(acme, "provider: memorystore", "provider: elasticache", 1)
		yaml = strings.Replace(yaml, "provider: ecs-fargate\n", "provider: render\n", 1)
		r := compute(t, yaml, "production", live...)
		if op := action(t, r, "service:worker").Op; op != Replace {
			t.Errorf("worker %s", op)
		}
		if op := action(t, r, "resource:cache").Op; op != Replace {
			t.Errorf("cache %s", op)
		}
		if !slices.Contains(codes(r.Warnings), "data-loss:cache") {
			t.Errorf("replacing a store of data without a warning: %v", codes(r.Warnings))
		}
	})

	t.Run("removed and turned off", func(t *testing.T) {
		yaml := strings.Replace(acme, "  gateway:\n    source: apps/gateway\n    needs: [cache, api]\n", "", 1)
		r := compute(t, yaml, "preview", live...)
		gw := action(t, r, "service:gateway")
		if gw.Op != Delete || gw.Creates != "Cloud Run service" || !strings.Contains(gw.Reason, "no longer has") {
			t.Errorf("gateway = %+v", gw)
		}
		w := action(t, r, "service:worker")
		if w.Op != Delete || !strings.Contains(w.Reason, "off in preview") {
			t.Errorf("worker = %+v", w)
		}
		if last := r.Actions[len(r.Actions)-1]; last.Subject != ServiceSubject || last.Op != Delete {
			t.Errorf("deletes don't come last: %+v", last)
		}
	})

	t.Run("broken isn't deleted", func(t *testing.T) {
		yaml := strings.Replace(acme, "provider: vercel", "provider: verccel", 1)
		r := compute(t, yaml, "production", live...)
		if !slices.Contains(codes(r.Problems), "unknown-provider:web") {
			t.Fatalf("problems %v", codes(r.Problems))
		}
		for _, a := range r.Actions {
			if a.Name == "web" {
				t.Errorf("a typo would delete web: %+v", a)
			}
		}
	})
}

func TestCompute_findings(t *testing.T) {
	yaml := "version: 1\ndefaults: {provider: render}\nservices:\n  api: {source: services/api}\n"
	got := codes(compute(t, yaml, "production").Warnings)
	if !slices.Contains(got, "unprovided-resource:postgres") {
		t.Errorf("Postgres use went unmentioned: %v", got)
	}
	ignored := codes(compute(t, yaml+"ignore: {resources: [postgres, redis]}\n", "production").Warnings)
	if slices.ContainsFunc(ignored, func(c string) bool { return strings.HasSuffix(c, "resource:postgres") }) {
		t.Errorf("ignored, but still mentioned: %v", ignored)
	}

	g := *polyglot(t)
	g.Resources = slices.Clone(g.Resources)
	for i := range g.Resources {
		g.Resources[i].NeedsConfirmation = true
	}
	r := Compute(Input{Environment: "production", Graph: &g, Manifest: parse(t, yaml), Providers: providers.Registry(), Connections: connected})
	if !slices.Contains(codes(r.Warnings), "unconfirmed-resource:postgres") {
		t.Errorf("weak finding not marked unconfirmed: %v", codes(r.Warnings))
	}
}

func TestCompute_sameKindTwice(t *testing.T) {
	yaml := `version: 1
defaults: {provider: render}
services:
  api: {source: services/api, needs: [db, analytics]}
resources:
  db: {kind: postgres}
  analytics: {kind: postgres}
`
	api := action(t, compute(t, yaml, "production"), "service:api")
	keys := []string{api.Service.Env[0].Key, api.Service.Env[1].Key}
	if !slices.Equal(keys, []string{"ANALYTICS_URL", "DB_URL"}) {
		t.Errorf("keys = %v", keys)
	}
}

func TestCompute_privateLinks(t *testing.T) {
	// On ECS, services in an environment reach each other by name,
	// whether or not the one they need is public.
	ecs := compute(t, `version: 1
defaults: {provider: ecs-fargate}
services:
  gateway: {source: apps/gateway, needs: [api]}
  api: {source: services/api, public: false}
ignore: {resources: [postgres, redis]}
`, "production")
	if len(ecs.Problems) > 0 {
		t.Fatalf("problems %v", codes(ecs.Problems))
	}
	if link := action(t, ecs, "service:gateway").Service.Env[0]; link.Address != "http://api:8080" {
		t.Errorf("gateway reaches api at %+v", link)
	}

	// On Cloud Run, a private service needs an ID token, which the plan
	// says, and the link is its URL.
	run := compute(t, `version: 1
defaults: {provider: cloud-run}
services:
  gateway: {source: apps/gateway, needs: [api]}
  api: {source: services/api, public: false}
ignore: {resources: [postgres, redis]}
`, "production")
	if len(run.Problems) > 0 || !slices.Contains(codes(run.Warnings), "authenticated-call:gateway") {
		t.Fatalf("problems %v, warnings %v", codes(run.Problems), codes(run.Warnings))
	}
	if link := action(t, run, "service:gateway").Service.Env[0]; link.Address != "" {
		t.Errorf("a Cloud Run link has an address: %+v", link)
	}
}

func TestCompute_deterministic(t *testing.T) {
	a, err := Digest(compute(t, acme, "production"))
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		b, err := Digest(compute(t, acme, "production"))
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatal("the same input planned twice gave different plans")
		}
	}
}
