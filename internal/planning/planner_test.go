package planning

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	pd "github.com/xraph/loomplane/pkg/discovery"
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
	if !r.Executable {
		t.Error("not executable, though every provider it uses has an adapter")
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
	if len(api.Service.Env) != 1 || !reflect.DeepEqual(api.Service.Env[0], EnvLink{Key: "DATABASE_URL", From: "db", Subject: ResourceSubject}) {
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
		// The worker uses Redis per analysis, but the cache is on another
		// cloud, so it needs nothing.
		"unlinked-resource:worker",
	} {
		if !slices.Contains(got, w) {
			t.Errorf("no warning %s in %v", w, got)
		}
	}
	for _, ok := range []string{"provider-unavailable:cloud-run", "provider-unavailable:vercel", "provider-unavailable:cloud-sql", "provider-unavailable:ecs-fargate", "provider-unavailable:memorystore"} {
		if slices.Contains(got, ok) {
			t.Errorf("%s has an adapter, but was warned about: %v", ok, got)
		}
	}
	// A target with no adapter yet can be planned, for review only.
	reg := providers.Registry()
	if err := reg.Register(provider.Provider{
		Name: "later", Title: "Later", Cloud: "later", Model: provider.Instance, Note: "Its adapter comes later.",
		Runs: []provider.Runs{{Workload: provider.HTTPService, Execution: provider.Container, Creates: "thing"}},
	}); err != nil {
		t.Fatal(err)
	}
	later := Compute(Input{
		Environment: "production", Graph: polyglot(t), Providers: reg, Connections: connected,
		Manifest: parse(t, "version: 1\ndefaults: {provider: later}\nservices:\n  api: {source: services/api}\nignore: {resources: [postgres, redis]}\n"),
	})
	if later.Executable || !slices.Contains(codes(later.Warnings), "provider-unavailable:later") {
		t.Errorf("later: executable %v, warnings %v", later.Executable, codes(later.Warnings))
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
		{"private with no network", "version: 1\ndefaults: {provider: vercel}\nservices:\n  web: {source: apps/web, needs: [docs]}\n  docs: {source: apps/docs, public: false}", "private-unreachable:web"},
		{"render without a dockerfile", "version: 1\nservices:\n  ingest: {source: crates/ingest/src/main.rs, provider: render}", "render-needs-dockerfile:ingest"},
		{"azure without a dockerfile", "version: 1\nservices:\n  ingest: {source: crates/ingest/src/main.rs, provider: azure-container-apps}", "azure-needs-dockerfile:ingest"},
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
		// What a replace says is what a deployment does: the new one
		// first, and the old one once it's up.
		want := "Replacing cache provisions a new, empty one, then deletes the old one with its data once the new one is up. Loomplane doesn't move data between them."
		for _, w := range r.Warnings {
			if w.Code == "data-loss" && w.Name == "cache" && w.Message != want {
				t.Errorf("warning %q, want %q", w.Message, want)
			}
		}
		reason := "worker moves from ecs-fargate to render. The new one starts first, and the old one is removed once it's up."
		if got := action(t, r, "service:worker").Reason; got != reason {
			t.Errorf("reason %q, want %q", got, reason)
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

// federated is polyglot as "mono", beside a Next.js site in a repository
// of its own that uses @acme/kit, which a third repository, "kit",
// publishes. Polyglot already publishes an @acme/ui, and a name two
// repositories publish gets no edge, so kit's package has its own. extra
// adds more repositories.
func federated(t *testing.T, extra ...graph.Part) *graph.Graph {
	t.Helper()
	site := graph.Build(&pd.Report{Units: []pd.Unit{{
		ID: "node:.", Ecosystem: pd.Node, Name: "site", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "site", Path: ".", Frameworks: []pd.Framework{{Name: "next", Kind: pd.Web, Confidence: pd.Likely}}}},
		Requires:    []pd.Requirement{{Name: "@acme/kit", Version: "^1.0.0", Evidence: pd.Evidence{Path: "package.json", Line: 9}}},
	}}})
	kit := graph.Build(&pd.Report{Units: []pd.Unit{{ID: "node:.", Ecosystem: pd.Node, Name: "@acme/kit", Path: "."}}})
	return graph.Compose(append([]graph.Part{
		{Source: graph.SourceRef{Alias: "mono", Repository: "acme/mono", CommitSHA: strings.Repeat("a", 40)}, Graph: polyglot(t)},
		{Source: graph.SourceRef{Alias: "site", Repository: "acme/site", CommitSHA: strings.Repeat("b", 40)}, Graph: site},
		{Source: graph.SourceRef{Alias: "kit", Repository: "acme/kit", CommitSHA: strings.Repeat("c", 40)}, Graph: kit},
	}, extra...))
}

const federatedYAML = `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [db]
  site:
    repo: site
    source: .
    provider: vercel
    needs: [api]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`

func computeFed(t *testing.T, yaml string, held []string, bindings ...*Binding) *Result {
	t.Helper()
	h := map[string]bool{}
	for _, a := range held {
		h[a] = true
	}
	return Compute(Input{
		Environment: "production", Graph: federated(t), Manifest: parse(t, yaml),
		Providers: providers.Registry(), Bindings: bindings, Held: h, Connections: connected,
	})
}

func TestCompute_matchesWithinARepository(t *testing.T) {
	r := computeFed(t, federatedYAML, nil)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	site := action(t, r, "service:site")
	if site.Service.Repo != "site" || site.Service.CommitSHA != strings.Repeat("b", 40) {
		t.Fatalf("site %+v", site.Service)
	}
	if api := action(t, r, "service:api"); api.Service.Repo != "mono" {
		t.Fatalf("api %+v", api.Service)
	}

	tests := []struct{ name, yaml, want string }{
		{"no repo with three", strings.Replace(federatedYAML, "    repo: site\n", "", 1), "ambiguous-repo:site"},
		{"unknown repo", strings.Replace(federatedYAML, "repo: site", "repo: nope", 1), "unknown-repo:site"},
		{"wrong repo", strings.Replace(federatedYAML, "repo: mono", "repo: site", 1), "service-not-found:api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeFed(t, tt.yaml, nil)
			if !slices.Contains(codes(got.Problems), tt.want) {
				t.Fatalf("problems %v, want %s", codes(got.Problems), tt.want)
			}
		})
	}
	r = computeFed(t, strings.Replace(federatedYAML, "repo: mono", "repo: site", 1), nil)
	for _, p := range r.Problems {
		if p.Code == "service-not-found" && !strings.Contains(p.Message, "site@bbbbbbb") {
			t.Fatalf("the message should name the commit: %s", p.Message)
		}
	}
}

const federatedWithoutSite = `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`

func TestCompute_heldServicesStayAsDeployed(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))

	// The site's repository is held, so a change to it waits.
	changed := strings.Replace(federatedYAML, "    provider: vercel\n", "    provider: vercel\n    public: false\n", 1)
	r := computeFed(t, changed, []string{"site"}, live...)
	site := action(t, r, "service:site")
	if site.Op != Keep || !strings.Contains(site.Reason, "Held") || !site.Service.Public {
		t.Fatalf("site %+v", site)
	}
	if !slices.Contains(codes(r.Warnings), "held:site") {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}

	// Gone from the manifest, a held service still isn't deleted.
	r = computeFed(t, federatedWithoutSite, []string{"site"}, live...)
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:site" }) {
		t.Fatalf("a held service was planned: %+v", r.Actions)
	}
	if !slices.Contains(codes(r.Warnings), "held:site") {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
}

func TestCompute_aServiceNeedingAHeldServiceThatRunsNowhereIsAProblem(t *testing.T) {
	r := computeFed(t, federatedYAML, []string{"mono"})
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:api" }) {
		t.Fatal("a held service that runs nowhere was created")
	}
	if !slices.Contains(codes(r.Warnings), "held:api") || !slices.Contains(codes(r.Problems), "need-held:site") {
		t.Fatalf("warnings %v, problems %v", codes(r.Warnings), codes(r.Problems))
	}
}

func TestCompute_movingAPackageNamesWhatItsHeldUsersAre(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	r := computeFed(t, federatedYAML, []string{"site"}, live...)
	var impact *Finding
	for i := range r.Warnings {
		if r.Warnings[i].Code == "cross-repo-impact" {
			impact = &r.Warnings[i]
		}
	}
	if impact == nil || impact.Name != "@acme/kit" || !strings.Contains(impact.Message, "site in site") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	// Nothing held, nothing to warn about.
	if slices.ContainsFunc(computeFed(t, federatedYAML, nil, live...).Warnings, func(f Finding) bool { return f.Code == "cross-repo-impact" }) {
		t.Fatal("a plan that moves everything warned about impact")
	}
}

func warning(r *Result, code, name string) *Finding {
	for i := range r.Warnings {
		if r.Warnings[i].Code == code && r.Warnings[i].Name == name {
			return &r.Warnings[i]
		}
	}
	return nil
}

func TestCompute_aHeldServicesManifestProblemWaits(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	r := computeFed(t, strings.Replace(federatedYAML, "provider: vercel", "provider: nope", 1), []string{"site"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	site := action(t, r, "service:site")
	if site.Op != Keep || site.Provider != "vercel" || site.Service.Provider != "vercel" {
		t.Fatalf("site %+v", site)
	}
	held := warning(r, "held", "site")
	if held == nil || !strings.Contains(held.Message, "manifest changes wait") || !strings.Contains(held.Message, "a problem to fix first") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	if api := action(t, r, "service:api"); api.Op != Keep {
		t.Fatalf("api %+v", api)
	}
}

func TestCompute_aHeldServiceMovedToAnotherCloudStaysWhereItRuns(t *testing.T) {
	catalog := providers.Catalog()
	list := make([]provider.Provider, 0, len(catalog))
	for _, p := range catalog {
		if p.Name == "cloud-run" || p.Name == "vercel" || p.Name == "render" {
			p.Available, p.Note = true, ""
		}
		list = append(list, p)
	}
	reg, err := provider.NewRegistry(list...)
	if err != nil {
		t.Fatal(err)
	}
	in := func(yaml string, held map[string]bool, bindings []*Binding) Input {
		return Input{
			Environment: "production", Graph: federated(t), Manifest: parse(t, yaml), Providers: reg,
			Bindings: bindings, Held: held,
			Connections: []ConnectionOption{
				{ID: "conn_g", Name: "Prod", Cloud: "gcp", Ready: true},
				{ID: "conn_v", Name: "Web", Cloud: "vercel", Ready: true},
			},
		}
	}
	live := deployed(t, Compute(in(federatedYAML, nil, nil)))
	moved := strings.Replace(federatedYAML, "provider: vercel", "provider: render", 1)

	// Moving everything, render needs a connection nobody has.
	if r := Compute(in(moved, nil, live)); !slices.Contains(findingCodes(r.Problems), "no-connection") {
		t.Fatalf("problems %v", codes(r.Problems))
	}

	r := Compute(in(moved, map[string]bool{"site": true}, live))
	if slices.Contains(findingCodes(r.Problems), "no-connection") {
		t.Fatalf("a held service asked for a connection: %v", codes(r.Problems))
	}
	site := action(t, r, "service:site")
	if site.Op != Keep || site.Provider != "vercel" || site.Creates != "Vercel project" {
		t.Fatalf("site %+v", site)
	}
	if r.Connections["vercel"] != "conn_v" || r.Connections["render"] != "" {
		t.Fatalf("connections %v", r.Connections)
	}
	if warning(r, "held", "site") == nil {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
}

// withGateway adds polyglot's gateway, which has no port on record.
const withGateway = `  gateway:
    repo: mono
    source: apps/gateway
`

func TestCompute_aHeldServiceGetsNoWarningsFromItsManifest(t *testing.T) {
	yaml := strings.Replace(federatedYAML, "resources:\n", withGateway+"resources:\n", 1)
	first := computeFed(t, yaml, nil)
	if warning(first, "port-assumed", "gateway") == nil {
		t.Fatalf("the test needs gateway's port assumed: %v", codes(first.Warnings))
	}
	r := computeFed(t, yaml, []string{"mono"}, deployed(t, first)...)
	if warning(r, "port-assumed", "gateway") != nil {
		t.Fatalf("a held service was configured from its manifest: %v", codes(r.Warnings))
	}
	if gw := action(t, r, "service:gateway"); gw.Op != Keep || gw.Service.Port != DefaultPort {
		t.Fatalf("gateway %+v", gw)
	}
	if warning(r, "held", "gateway") != nil {
		t.Fatalf("an unchanged held service warned: %v", codes(r.Warnings))
	}
}

func TestCompute_aHeldServiceKeepsTheLinksItWasDeployedWith(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	yaml := strings.Replace(federatedYAML, "    needs: [db]\n", "    needs: [db, gateway]\n", 1)
	yaml = strings.Replace(yaml, "resources:\n", withGateway+"resources:\n", 1)
	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	if warning(r, "held", "gateway") == nil || warning(r, "held", "api") == nil {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
	api, site := action(t, r, "service:api"), action(t, r, "service:site")
	if api.Op != Keep || !slices.Equal(api.DependsOn, []string{"resource:db"}) || len(api.Service.Env) != 1 {
		t.Fatalf("api %+v", api)
	}
	if api.Step < 2 || site.Step <= api.Step || site.Op != Keep {
		t.Fatalf("api runs at step %d, site at %d (%s)", api.Step, site.Step, site.Op)
	}
}

func TestCompute_anOlderBindingOfAHeldProjectIsntDeleted(t *testing.T) {
	g := graph.Compose([]graph.Part{
		{Source: graph.SourceRef{Alias: "mono", Repository: "acme/mono", CommitSHA: strings.Repeat("a", 40)}, Graph: polyglot(t)},
	})
	in := func(yaml string, held map[string]bool, bindings []*Binding) Input {
		return Input{Environment: "production", Graph: g, Manifest: parse(t, yaml), Providers: providers.Registry(), Bindings: bindings, Held: held}
	}
	live := deployed(t, Compute(in("version: 1\ndefaults: {provider: cloud-run}\nservices:\n  api: {source: services/api}\n", nil, nil)))
	// Written before services knew their repository.
	var cfg map[string]any
	if err := json.Unmarshal(live[0].Config, &cfg); err != nil || cfg["repo"] != "mono" {
		t.Fatalf("binding %s", live[0].Config)
	}
	delete(cfg, "repo")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	live[0].Config = data

	gone := "version: 1\nresources:\n  db: {kind: postgres, provider: cloud-sql}\n"
	if r := Compute(in(gone, nil, live)); action(t, r, "service:api").Op != Delete {
		t.Fatal("the test needs api deleted when nothing is held")
	}
	r := Compute(in(gone, map[string]bool{"mono": true}, live))
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:api" }) {
		t.Fatalf("an older binding of a held project was planned: %+v", r.Actions)
	}
	if warning(r, "held", "api") == nil {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
}

func TestCompute_anOlderBindingIsntDeletedWhileAnyRepositoryIsHeld(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	// site's binding was written before services knew their repository.
	var site *Binding
	for _, b := range live {
		if b.Subject == ServiceSubject && b.Name == "site" {
			site = b
		}
	}
	if site == nil {
		t.Fatalf("no site binding in %+v", live)
	}
	var cfg map[string]any
	if err := json.Unmarshal(site.Config, &cfg); err != nil || cfg["repo"] != "site" {
		t.Fatalf("binding %s", site.Config)
	}
	delete(cfg, "repo")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	site.Config = data

	if r := computeFed(t, federatedWithoutSite, nil, live...); action(t, r, "service:site").Op != Delete {
		t.Fatal("the test needs site deleted when nothing is held")
	}
	// Only mono is held, but nothing says which repository site came from,
	// so a scoped plan can't rule out that it's a held one.
	r := computeFed(t, federatedWithoutSite, []string{"mono"}, live...)
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:site" && a.Op == Delete }) {
		t.Fatalf("an older binding was deleted by a scoped plan: %+v", r.Actions)
	}
	if warning(r, "held", "site") == nil {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
}

func TestCompute_unknownRepoWithoutRepositories(t *testing.T) {
	r := compute(t, "version: 1\nservices:\n  api: {repo: mono, source: services/api, provider: cloud-run}\n", "production")
	if len(r.Problems) != 1 || r.Problems[0].Code != "unknown-repo" || !strings.Contains(r.Problems[0].Message, "this project has no repositories") {
		t.Fatalf("problems %+v", r.Problems)
	}
}

func TestCompute_onlyHeldServicesAreImpacted(t *testing.T) {
	// A library in a repository of its own that uses kit's package.
	lib := graph.Build(&pd.Report{Units: []pd.Unit{{
		ID: "node:.", Ecosystem: pd.Node, Name: "@acme/lib", Path: ".",
		Requires: []pd.Requirement{{Name: "@acme/kit", Version: "^1.0.0", Evidence: pd.Evidence{Path: "package.json", Line: 4}}},
	}}})
	g := federated(t, graph.Part{Source: graph.SourceRef{Alias: "lib", Repository: "acme/lib", CommitSHA: strings.Repeat("d", 40)}, Graph: lib})
	if !slices.ContainsFunc(g.Edges, func(e graph.Edge) bool { return e.From == "pkg:lib:node:." && e.Kind == graph.Uses }) {
		t.Fatalf("the test needs lib to use kit: %+v", g.Edges)
	}
	r := Compute(Input{
		Environment: "production", Graph: g, Manifest: parse(t, federatedYAML),
		Providers: providers.Registry(), Held: map[string]bool{"lib": true}, Connections: connected,
	})
	if slices.ContainsFunc(r.Warnings, func(f Finding) bool { return f.Code == "cross-repo-impact" }) {
		t.Fatalf("a held package counted as a user: %+v", r.Warnings)
	}
}

func TestCompute_turningAHeldServiceOffWaits(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	off := strings.Replace(federatedYAML, "    needs: [db]\n", "    needs: [db]\n    environments:\n      production:\n        enabled: false\n", 1)
	r := computeFed(t, off, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	if api := action(t, r, "service:api"); api.Op != Keep {
		t.Fatalf("api %+v", api)
	}
	site := action(t, r, "service:site")
	if site.Op != Keep || !slices.ContainsFunc(site.Service.Env, func(e EnvLink) bool { return e.Key == "API_URL" && e.From == "api" }) {
		t.Fatalf("site %s %+v", site.Op, site.Service.Env)
	}
	if held := warning(r, "held", "api"); held == nil || !strings.Contains(held.Message, "manifest changes wait") {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
}

func TestCompute_movingAHeldServicesSourceWaits(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	tests := []struct{ name, source, problem string }{
		{"not found", "services/apiv2", "Analysis found no service at services/apiv2"},
		{"ambiguous", "crates/ingest", "More than one service is at crates/ingest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := strings.Replace(federatedYAML, "source: services/api", "source: "+tt.source, 1)
			r := computeFed(t, yaml, []string{"mono"}, live...)
			if len(r.Problems) != 0 {
				t.Fatalf("problems %v", codes(r.Problems))
			}
			if api := action(t, r, "service:api"); api.Op != Keep || api.Service.Source != "services/api" {
				t.Fatalf("api %+v", api)
			}
			if held := warning(r, "held", "api"); held == nil || !strings.Contains(held.Message, tt.problem) {
				t.Fatalf("warnings %+v", r.Warnings)
			}
			if warning(r, "service-left-out", "api") != nil {
				t.Fatalf("a held service was left out: %v", codes(r.Warnings))
			}
		})
	}
}

func TestCompute_gateways(t *testing.T) {
	const onECS = `version: 1
defaults: {provider: ecs-fargate}
services:
  api: {source: services/api, public: false}
  web: {source: apps/web, provider: vercel}
gateways:
  edge:
    routes:
      - {path: /api, to: api, strip_prefix: true}
      - {path: /, to: web}
ignore: {resources: [postgres, redis]}
`
	r := compute(t, onECS, "production")
	if len(r.Problems) > 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	edge := action(t, r, "service:edge")
	api, web := action(t, r, "service:api"), action(t, r, "service:web")
	if edge.Step <= max(api.Step, web.Step) || !edge.Service.Public || edge.Service.Image != "Dockerfile" {
		t.Fatalf("edge %+v", edge)
	}
	// api is private, reached inside the environment; web by its URL.
	if len(edge.Service.Env) != 2 || edge.Service.Env[0].Address != "http://api:8080" || edge.Service.Env[1].Address != "" {
		t.Fatalf("edge's links %+v", edge.Service.Env)
	}
	if len(edge.Service.Gateway) != 2 || edge.Service.Gateway[0].Env != "API_URL" || !edge.Service.Gateway[0].StripPrefix {
		t.Fatalf("edge's routes %+v", edge.Service.Gateway)
	}

	// A gateway's code is Loomplane's: a new commit alone doesn't change it.
	at := func(commit string, live []*Binding) *Result {
		return Compute(Input{
			Environment: "production", Graph: atCommit(polyglot(t), commit), Manifest: parse(t, onECS), Providers: providers.Registry(),
			Bindings: live, Connections: connected,
		})
	}
	live := deployed(t, at("aaaaaaaaaaaa", nil))
	for _, b := range live {
		b.CommitSHA = "aaaaaaaaaaaa"
	}
	next := at("bbbbbbbbbbbb", live)
	if a := action(t, next, "service:edge"); a.Op != Keep {
		t.Errorf("edge on a new commit: %s %+v", a.Op, a.Changes)
	}
	if a := action(t, next, "service:api"); a.Op != Update || len(a.Changes) != 1 || a.Changes[0].Field != "commit" {
		t.Errorf("api on a new commit: %s %+v", a.Op, a.Changes)
	}

	for _, tt := range []struct{ name, yaml, want string }{
		{"on vercel", "version: 1\nservices:\n  web: {source: apps/web, provider: vercel}\ngateways:\n  edge: {provider: vercel, routes: [{path: /, to: web}]}", "gateway-unsupported:edge"},
		{"private on cloud run", "version: 1\ndefaults: {provider: cloud-run}\nservices:\n  api: {source: services/api, public: false}\ngateways:\n  edge: {routes: [{path: /, to: api}]}\nignore: {resources: [postgres, redis]}", "gateway-cant-reach:edge"},
		{"to a worker", "version: 1\ndefaults: {provider: ecs-fargate}\nservices:\n  w: {source: services/worker/cmd/worker}\ngateways:\n  edge: {routes: [{path: /, to: w}]}", "need-worker:edge"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := codes(compute(t, tt.yaml, "production").Problems); !slices.Contains(got, tt.want) {
				t.Fatalf("problems %v, want %s", got, tt.want)
			}
		})
	}
}

func TestCompute_whatAHeldServiceUsesIsntDeleted(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	yaml := strings.Replace(federatedYAML, "    needs: [db]\n", "", 1)
	yaml = strings.Replace(yaml, "resources:\n  db:\n    kind: postgres\n    provider: cloud-sql\n", "", 1)
	if r := computeFed(t, yaml, nil, live...); action(t, r, "resource:db").Op != Delete {
		t.Fatal("the test needs db deleted when nothing is held")
	}

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	db := action(t, r, "resource:db")
	if db.Op != Keep || db.Resource == nil || db.Resource.Kind != "postgres" {
		t.Fatalf("db %+v", db)
	}
	held := warning(r, "held", "db")
	if held == nil || !strings.Contains(held.Message, "api in mono") || !strings.Contains(held.Message, "still uses it") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	api := action(t, r, "service:api")
	if !slices.Equal(api.DependsOn, []string{"resource:db"}) || api.Step <= db.Step {
		t.Fatalf("api runs at step %d after %v, db at %d", api.Step, api.DependsOn, db.Step)
	}
}

func TestCompute_whatARemovedHeldServiceUsesIsntDeleted(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	// api and db are both gone, and site no longer needs api.
	yaml := "version: 1\nservices:\n  site:\n    repo: site\n    source: .\n    provider: vercel\n"
	if r := computeFed(t, yaml, nil, live...); action(t, r, "resource:db").Op != Delete {
		t.Fatal("the test needs db deleted when nothing is held")
	}

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	for _, a := range r.Actions {
		if (a.ID == "service:api" || a.ID == "resource:db") && a.Op == Delete {
			t.Fatalf("deleted %s, which a held service still runs on: %+v", a.ID, a)
		}
	}
	if db := action(t, r, "resource:db"); db.Op != Keep {
		t.Fatalf("db %+v", db)
	}
	if warning(r, "held", "api") == nil {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
	if held := warning(r, "held", "db"); held == nil || !strings.Contains(held.Message, "api in mono") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

func TestCompute_whatAHeldServiceUsesIsRetainedThroughWhatsKeptForIt(t *testing.T) {
	// api, in mono, needs site, in site, which needs db.
	first := `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [site]
  site:
    repo: site
    source: .
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`
	live := deployed(t, computeFed(t, first, nil))
	// site and db go, and api's manifest entry no longer needs site, but
	// api is held, so it runs on with what it was deployed against.
	yaml := "version: 1\ndefaults:\n  provider: cloud-run\nservices:\n  api:\n    repo: mono\n    source: services/api\n"
	if r := computeFed(t, yaml, nil, live...); action(t, r, "resource:db").Op != Delete {
		t.Fatal("the test needs db deleted when nothing is held")
	}

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	for _, id := range []string{"service:site", "resource:db"} {
		if a := action(t, r, id); a.Op != Keep {
			t.Fatalf("%s %+v", id, a)
		}
	}
	if warning(r, "held", "site") == nil {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
	if held := warning(r, "held", "db"); held == nil || !strings.Contains(held.Message, "api in mono (through site)") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

func TestCompute_aServiceDeployedFromAHeldRepositoryStaysHeldWhenItsRepoMoves(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	moved := strings.Replace(federatedYAML, "repo: mono", "repo: site", 1)
	r := computeFed(t, moved, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	api := action(t, r, "service:api")
	if api.Op != Keep || !strings.Contains(api.Reason, "Held") || api.Service.Repo != "mono" {
		t.Fatalf("api %+v", api)
	}
	held := warning(r, "held", "api")
	if held == nil || !strings.Contains(held.Message, "manifest changes wait") || !strings.Contains(held.Message, "move to site") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	for _, a := range r.Actions {
		if a.Op == Create || a.Op == Replace {
			t.Fatalf("a plan that holds api planned %s %s", a.Op, a.ID)
		}
	}

	// site's binding was written before services knew their repository,
	// so while anything is held it can't be told apart from a held one.
	for _, b := range live {
		if b.Subject != ServiceSubject || b.Name != "site" {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		delete(cfg, "repo")
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		b.Config = data
	}
	r = computeFed(t, federatedYAML, []string{"mono"}, live...)
	if site := action(t, r, "service:site"); site.Op != Keep || !strings.Contains(site.Reason, "Held") {
		t.Fatalf("site %+v", site)
	}
	if held := warning(r, "held", "site"); held == nil || !strings.Contains(held.Message, "every repository") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

func TestCompute_severalHeldUsersOfSomethingAgreeWithIt(t *testing.T) {
	// api and site both need db, and both are held, in different
	// repositories. site runs on Cloud Run here, since a service on Vercel
	// can't reach a database on Google Cloud.
	both := strings.Replace(federatedYAML, "needs: [api]", "needs: [api, db]", 1)
	both = strings.Replace(both, "    provider: vercel\n", "", 1)
	live := deployed(t, computeFed(t, both, nil))
	yaml := strings.Replace(federatedYAML, "    needs: [db]\n", "", 1)
	yaml = strings.Replace(yaml, "resources:\n  db:\n    kind: postgres\n    provider: cloud-sql\n", "", 1)
	if r := computeFed(t, yaml, nil, live...); action(t, r, "resource:db").Op != Delete {
		t.Fatal("the test needs db deleted when nothing is held")
	}

	r := computeFed(t, yaml, []string{"mono", "site"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	db := action(t, r, "resource:db")
	if db.Op != Keep || !strings.Contains(db.Reason, "api in mono and site in site, which this plan holds back, still use it.") {
		t.Fatalf("db %+v", db)
	}
	held := warning(r, "held", "db")
	if held == nil || !strings.Contains(held.Message, "api in mono and site in site, which this plan holds back, still use it, so") {
		t.Fatalf("warnings %+v", r.Warnings)
	}

	// One user still takes the singular.
	r = computeFed(t, yaml, []string{"mono"}, live...)
	if db := action(t, r, "resource:db"); !strings.Contains(db.Reason, "api in mono, which this plan holds back, still uses it.") {
		t.Fatalf("db %+v", db)
	}
}

func TestCompute_aHeldCycleOfRemovedServicesNamesNoServiceAsItsOwnUser(t *testing.T) {
	// api and site use each other once deployed.
	live := deployed(t, computeFed(t, federatedYAML, nil))
	for _, b := range live {
		if b.Subject != ServiceSubject || b.Name != "api" {
			continue
		}
		var cfg ServiceConfig
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Env = append(cfg.Env, EnvLink{Key: "SITE_URL", From: "site", Subject: ServiceSubject})
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		b.Config = data
	}
	// Both are gone from the manifest, and mono is held.
	r := computeFed(t, "version: 1\n", []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	for _, a := range r.Actions {
		if a.Op == Delete {
			t.Fatalf("deleted %s, which a held service still runs on or with: %+v", a.ID, a)
		}
	}
	for _, id := range []string{"service:site", "resource:db"} {
		if a := action(t, r, id); a.Op != Keep {
			t.Fatalf("%s %+v", id, a)
		}
	}
	site := warning(r, "held", "site")
	if site == nil || !strings.Contains(site.Message, "api in mono, which this plan holds back, still uses it") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	for _, f := range r.Warnings {
		if f.Name == "api" && (strings.Contains(f.Message, "through") || strings.Contains(f.Message, "api in mono")) {
			t.Fatalf("api is named as its own user: %+v", f)
		}
	}
	// api is held already, so nothing retains it for itself.
	for _, a := range r.Actions {
		if a.ID == "service:api" {
			t.Fatalf("api was planned as %+v", a)
		}
	}
}

func TestCompute_aServiceMovedIntoAHeldRepositoryStaysWhereItWasDeployed(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	// site runs from site, and the manifest now puts it in mono, held.
	moved := strings.Replace(federatedYAML, "repo: site", "repo: mono", 1)
	r := computeFed(t, moved, []string{"mono"}, live...)
	want := "site runs from site, but the manifest moves it to mono, which this plan holds back, so it stays as deployed and doesn't pick up site's new commit until a plan moves mono."
	site := action(t, r, "service:site")
	if site.Op != Keep || !strings.Contains(site.Reason, want) {
		t.Fatalf("site %+v", site)
	}
	held := warning(r, "held", "site")
	if held == nil || !strings.Contains(held.Message, want) || strings.Contains(held.Message, "site is in mono") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

// edgeYAML is federatedYAML with a gateway in front of api, in mono, and
// site, in site.
const edgeYAML = federatedYAML + `gateways:
  edge:
    routes:
      - {path: /api, to: api}
      - {path: /, to: site}
`

func TestCompute_aGatewayIsNeverHeld(t *testing.T) {
	// site doesn't need api here, so only edge could keep api running.
	base := strings.Replace(edgeYAML, "    needs: [api]\n", "", 1)
	first := computeFed(t, base, nil)
	if len(first.Problems) != 0 {
		t.Fatalf("problems %v", codes(first.Problems))
	}
	live := deployed(t, first)

	// A gateway comes from no repository, so a scoped plan deletes one the
	// manifest dropped, the way a full plan does.
	noEdge := base[:strings.Index(base, "gateways:")]
	r := computeFed(t, noEdge, []string{"site"}, live...)
	if edge := action(t, r, "service:edge"); edge.Op != Delete {
		t.Fatalf("edge %+v", edge)
	}
	if warning(r, "held", "edge") != nil {
		t.Fatalf("warnings %+v", r.Warnings)
	}

	// Nor is it a held service that keeps what it routes to: api, in mono,
	// which the plan moves, goes with its route.
	noAPI := strings.Replace(base, "      - {path: /api, to: api}\n", "", 1)
	noAPI = strings.Replace(noAPI, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)
	r = computeFed(t, noAPI, []string{"site"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	if api := action(t, r, "service:api"); api.Op != Delete {
		t.Fatalf("api %+v", api)
	}
	edge := action(t, r, "service:edge")
	if edge.Op != Update || len(edge.Service.Gateway) != 1 || edge.Service.Gateway[0].To != "site" {
		t.Fatalf("edge %+v", edge)
	}
}

func TestCompute_aHeldServiceUsingAGatewayThatsStillOnLeavesItToTheGateway(t *testing.T) {
	live := deployed(t, computeFed(t, edgeYAML, nil))
	// api was deployed with edge's address. needs can't name a gateway, so
	// the link is written into api's binding.
	for _, b := range live {
		if b.Subject != ServiceSubject || b.Name != "api" {
			continue
		}
		var cfg ServiceConfig
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Env = append(cfg.Env, EnvLink{Key: "EDGE_URL", From: "edge", Subject: ServiceSubject})
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		b.Config = data
	}

	r := computeFed(t, edgeYAML, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	var edges []Action
	for _, a := range r.Actions {
		if a.ID == "service:edge" {
			edges = append(edges, a)
		}
	}
	if len(edges) != 1 || edges[0].Service == nil || len(edges[0].Service.Gateway) != 2 || strings.Contains(edges[0].Reason, "Kept") {
		t.Fatalf("edge actions %+v", edges)
	}
	// The deployed edge is the gateway the manifest has, so nothing
	// replaces it.
	if w := warning(r, "held", "edge"); w != nil {
		t.Fatalf("warning %+v", w)
	}
}

func TestCompute_aGatewayRouteToAHeldServiceThatRunsNowhereIsAProblem(t *testing.T) {
	// Nothing is deployed, and the plan holds site, so site isn't created
	// and edge has nothing to send / to.
	base := strings.Replace(edgeYAML, "    needs: [api]\n", "", 1)
	r := computeFed(t, base, []string{"site"})
	if got := codes(r.Problems); !slices.Contains(got, "need-held:edge") {
		t.Fatalf("problems %v", got)
	}
	for _, p := range r.Problems {
		if p.Code == "need-held" && (p.Path != "gateways.edge.routes[1]" || !strings.Contains(p.Message, "edge routes to site, which is in site") ||
			!strings.Contains(p.Message, "add site to the scope")) {
			t.Fatalf("problem %+v", p)
		}
	}
}

// drifted is live with one binding marked as drifted.
func drifted(live []*Binding, name, drift, message string) []*Binding {
	out := make([]*Binding, 0, len(live))
	for _, b := range live {
		c := *b
		if c.Name == name {
			c.Drift, c.DriftMessage = drift, message
		}
		out = append(out, &c)
	}
	return out
}

func TestCompute_aHeldServiceDeletedOutsideLoomplaneThatsNeededIsAProblem(t *testing.T) {
	live := drifted(deployed(t, computeFed(t, federatedYAML, nil)), "api", DriftMissing, "")

	// site, which the plan moves, needs api, which it holds and can't
	// rebuild.
	r := computeFed(t, federatedYAML, []string{"mono"}, live...)
	if api := action(t, r, "service:api"); api.Op != Keep {
		t.Fatalf("api %+v", api)
	}
	var found *Finding
	for i := range r.Problems {
		if r.Problems[i].Code == "held-missing" && r.Problems[i].Name == "api" {
			found = &r.Problems[i]
		}
	}
	want := "api was deleted outside Loomplane, and mono, which it comes from, is held, so this plan can't bring it back. Add mono to the scope."
	if found == nil || found.Message != want {
		t.Fatalf("problems %+v", r.Problems)
	}

	// A gateway's route needs it too.
	viaEdge := strings.Replace(edgeYAML, "    needs: [api]\n", "", 1)
	live = drifted(deployed(t, computeFed(t, viaEdge, nil)), "api", DriftMissing, "")
	r = computeFed(t, viaEdge, []string{"mono"}, live...)
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-missing:api"}) {
		t.Fatalf("problems %v", got)
	}

	// What's kept for a held service is the same: api, held, uses db,
	// which the manifest dropped.
	noDB := strings.Replace(federatedYAML, "    needs: [db]\n", "", 1)
	noDB = strings.Replace(noDB, "resources:\n  db:\n    kind: postgres\n    provider: cloud-sql\n", "", 1)
	live = drifted(deployed(t, computeFed(t, federatedYAML, nil)), "db", DriftMissing, "")
	r = computeFed(t, noDB, []string{"mono"}, live...)
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-missing:db"}) || !strings.Contains(r.Problems[0].Message, "this plan keeps it for mono") {
		t.Fatalf("problems %+v", r.Problems)
	}
}

func TestCompute_driftOnAHeldServiceNothingNeedsIsAWarning(t *testing.T) {
	first := deployed(t, computeFed(t, federatedYAML, nil))
	// site no longer needs api.
	yaml := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)

	for _, tt := range []struct{ name, drift, message, want string }{
		{"missing", DriftMissing, "", "api was deleted outside Loomplane, and mono, which it comes from, is held, so it stays gone until a plan that moves mono brings it back."},
		{"changed", DriftChanged, "another revision is live", "api was changed outside Loomplane (another revision is live), and mono, which it comes from, is held, so it stays that way until a plan that moves mono deploys it as planned again."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := computeFed(t, yaml, []string{"mono"}, drifted(first, "api", tt.drift, tt.message)...)
			if len(r.Problems) != 0 {
				t.Fatalf("problems %v", codes(r.Problems))
			}
			if api := action(t, r, "service:api"); api.Op != Keep {
				t.Fatalf("api %+v", api)
			}
			if w := warning(r, "held-drifted", "api"); w == nil || w.Message != tt.want {
				t.Fatalf("warnings %+v", r.Warnings)
			}
		})
	}
}

func TestCompute_driftOnAHeldServiceTheManifestDroppedIsReported(t *testing.T) {
	first := deployed(t, computeFed(t, federatedYAML, nil))
	// The manifest drops api, and site no longer needs it, but api is in
	// mono, which the plan holds, so nothing deletes it.
	noAPI := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	noAPI = strings.Replace(noAPI, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)

	for _, tt := range []struct{ name, drift, message, want, held string }{
		{
			"missing", DriftMissing, "",
			"api was deleted outside Loomplane and the manifest no longer has it, so it stays gone, and a plan that moves mono clears what's left of it.",
			"The manifest no longer has api, but it's in mono, which this plan holds back, so this plan doesn't remove it. It was deleted outside Loomplane, so it stays gone.",
		},
		{
			"changed", DriftChanged, "another revision is live",
			"api was changed outside Loomplane (another revision is live) and the manifest no longer has it, so it stays that way until a plan that moves mono removes it.",
			"The manifest no longer has api, but it's in mono, which this plan holds back, so it keeps running.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := computeFed(t, noAPI, []string{"mono"}, drifted(first, "api", tt.drift, tt.message)...)
			if len(r.Problems) != 0 {
				t.Fatalf("problems %v", codes(r.Problems))
			}
			for _, a := range r.Actions {
				if a.ID == "service:api" && a.Op == Delete {
					t.Fatalf("deleted api, which mono holds: %+v", a)
				}
			}
			if w := warning(r, "held-drifted", "api"); w == nil || w.Message != tt.want {
				t.Fatalf("warnings %+v", r.Warnings)
			}
			if w := warning(r, "held", "api"); w == nil || w.Message != tt.held {
				t.Fatalf("warnings %+v", r.Warnings)
			}
		})
	}
}

func TestCompute_aHeldServiceTheManifestDroppedThatsDeletedAndNeededIsAProblem(t *testing.T) {
	live := drifted(deployed(t, computeFed(t, federatedYAML, nil)), "api", DriftMissing, "")
	// The manifest drops api, and site's entry no longer needs it, but the
	// plan holds site too, so site runs on with the link to api it was
	// deployed with.
	noAPI := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	noAPI = strings.Replace(noAPI, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)

	r := computeFed(t, noAPI, []string{"mono", "site"}, live...)
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-missing:api"}) {
		t.Fatalf("problems %v", got)
	}
	want := "api was deleted outside Loomplane and the manifest no longer has it, so this plan doesn't bring it back, yet something in this plan still uses it. Put it back in the manifest and add mono to the scope, or stop using it."
	if r.Problems[0].Message != want || r.Problems[0].Path != "services.api" {
		t.Fatalf("problem %+v", r.Problems[0])
	}

	// What a dropped held service still uses is needed the same way: api
	// runs on, and db, kept for it, was deleted.
	onlySite := "version: 1\nservices:\n  site:\n    repo: site\n    source: .\n    provider: vercel\n"
	live = drifted(deployed(t, computeFed(t, federatedYAML, nil)), "db", DriftMissing, "")
	r = computeFed(t, onlySite, []string{"mono"}, live...)
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-missing:db"}) || !strings.Contains(r.Problems[0].Message, "this plan keeps it for mono") {
		t.Fatalf("problems %+v", r.Problems)
	}
}

func TestCompute_aRetainedSubjectDeletedOutsideLoomplaneStaysGone(t *testing.T) {
	first := deployed(t, computeFed(t, federatedYAML, nil))
	// The manifest drops api and db, but api is in mono, which the plan
	// holds, so db is kept for it.
	onlySite := "version: 1\nservices:\n  site:\n    repo: site\n    source: .\n    provider: vercel\n"

	for _, tt := range []struct{ name, drift, want string }{
		{"missing", DriftMissing, "db is gone from the manifest, but api in mono, which this plan holds back, still uses it, so this plan doesn't remove it. It was deleted outside Loomplane, so it stays gone."},
		{"no drift", "", "db is gone from the manifest, but api in mono, which this plan holds back, still uses it, so it keeps running."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := computeFed(t, onlySite, []string{"mono"}, drifted(first, "db", tt.drift, "")...)
			if db := action(t, r, "resource:db"); db.Op != Keep {
				t.Fatalf("db %+v", db)
			}
			if w := warning(r, "held", "db"); w == nil || w.Message != tt.want {
				t.Fatalf("warnings %+v", r.Warnings)
			}
		})
	}
}

func TestCompute_driftOnAHeldServiceUnderAGatewaysNameIsReported(t *testing.T) {
	// The manifest drops the service api and has a gateway called api that's
	// off here. That gateway isn't the api deployed from mono, which the
	// plan holds, so to that api the manifest no longer has it.
	live := drifted(deployed(t, computeFed(t, federatedYAML, nil)), "api", DriftMissing, "")
	yaml := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	yaml = strings.Replace(yaml, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)
	yaml += "gateways:\n  api:\n    routes:\n      - {path: /, to: site}\n    environments:\n      production:\n        enabled: false\n"

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	want := "api was deleted outside Loomplane and the manifest no longer has it, so it stays gone, and a plan that moves mono clears what's left of it."
	if w := warning(r, "held-drifted", "api"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	want = "The manifest no longer has api, but it's in mono, which this plan holds back, so this plan doesn't remove it. It was deleted outside Loomplane, so it stays gone."
	if w := warning(r, "held", "api"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

func TestCompute_whatADeletedDroppedHeldServiceUsedIsntNeeded(t *testing.T) {
	first := deployed(t, computeFed(t, federatedYAML, nil))
	// The manifest drops api and db, and mono is held, so db is kept for
	// api. Both were deleted outside Loomplane, so nothing that runs needs
	// db, and its drift is a warning, not a problem.
	onlySite := "version: 1\nservices:\n  site:\n    repo: site\n    source: .\n    provider: vercel\n"
	live := drifted(drifted(first, "api", DriftMissing, ""), "db", DriftMissing, "")

	r := computeFed(t, onlySite, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	want := "db was deleted outside Loomplane, and this plan keeps it for mono, which it holds back, so it stays gone, and a plan that moves mono clears what's left of it."
	if w := warning(r, "held-drifted", "db"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	want = "db was changed outside Loomplane (another revision is live), and this plan keeps it for mono, which it holds back, so it stays that way until a plan that moves mono removes it."
	r = computeFed(t, onlySite, []string{"mono"}, drifted(first, "db", DriftChanged, "another revision is live")...)
	if w := warning(r, "held-drifted", "db"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

func TestCompute_driftOnADroppedServiceOfAnUnknownRepositoryNamesEveryRepository(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	// site's binding was written before services knew their repository.
	for _, b := range live {
		if b.Subject != ServiceSubject || b.Name != "site" {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		delete(cfg, "repo")
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		b.Config = data
	}

	r := computeFed(t, federatedWithoutSite, []string{"mono"}, drifted(live, "site", DriftMissing, "")...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	want := "site was deleted outside Loomplane and the manifest no longer has it, so it stays gone, and only a plan of every repository clears what's left of it."
	if w := warning(r, "held-drifted", "site"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	want = "The manifest no longer has site, but it was deployed before Loomplane recorded which repository a service is from, and this plan holds some back, so this plan doesn't remove it. It was deleted outside Loomplane, so it stays gone. Plan every repository to remove it."
	if w := warning(r, "held", "site"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

func TestCompute_whatADeletedHeldServiceUsedIsntNeeded(t *testing.T) {
	first := deployed(t, computeFed(t, federatedYAML, nil))
	// api stays in the manifest, held, but no longer needs db, which the
	// manifest drops, and site no longer needs api. api runs on with the
	// link to db it was deployed with, so db is kept for it, but both were
	// deleted outside Loomplane, so nothing that runs needs db.
	yaml := strings.Replace(federatedYAML, "    needs: [db]\n", "", 1)
	yaml = strings.Replace(yaml, "    needs: [api]\n", "", 1)
	yaml = strings.Replace(yaml, "resources:\n  db:\n    kind: postgres\n    provider: cloud-sql\n", "", 1)
	live := drifted(drifted(first, "api", DriftMissing, ""), "db", DriftMissing, "")

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	if api := action(t, r, "service:api"); api.Op != Keep {
		t.Fatalf("api %+v", api)
	}
	want := "db was deleted outside Loomplane, and this plan keeps it for mono, which it holds back, so it stays gone, and a plan that moves mono clears what's left of it."
	if w := warning(r, "held-drifted", "db"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	if warning(r, "held-drifted", "api") == nil {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
}

func TestCompute_driftOnAHeldServiceThatsOffSaysAPlanRemovesIt(t *testing.T) {
	first := deployed(t, computeFed(t, federatedYAML, nil))
	// api is off here and site no longer needs it.
	off := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	off = strings.Replace(off, "    needs: [db]\n", "    needs: [db]\n    environments:\n      production:\n        enabled: false\n", 1)
	live := drifted(first, "api", DriftMissing, "")
	// A plan that moves mono removes it rather than bringing it back.
	if api := action(t, computeFed(t, off, nil, live...), "service:api"); api.Op != Delete {
		t.Fatalf("the test needs api deleted when nothing is held: %+v", api)
	}

	for _, tt := range []struct{ name, drift, message, want string }{
		{"missing", DriftMissing, "", "api was deleted outside Loomplane, and mono, which it comes from, is held, so it stays gone. It's off in production, so a plan that moves mono clears what's left of it."},
		{"changed", DriftChanged, "another revision is live", "api was changed outside Loomplane (another revision is live), and mono, which it comes from, is held, so it stays that way. It's off in production, so a plan that moves mono removes it."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := computeFed(t, off, []string{"mono"}, drifted(first, "api", tt.drift, tt.message)...)
			if len(r.Problems) != 0 {
				t.Fatalf("problems %+v", r.Problems)
			}
			if api := action(t, r, "service:api"); api.Op != Keep {
				t.Fatalf("api %+v", api)
			}
			if w := warning(r, "held-drifted", "api"); w == nil || w.Message != tt.want {
				t.Fatalf("warnings %+v", r.Warnings)
			}
		})
	}
}

func TestCompute_aRetainedServiceUnderAGatewaysNameIsGoneFromTheManifest(t *testing.T) {
	// api, in mono, needs site, in site, which needs db.
	first := `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [site]
  site:
    repo: site
    source: .
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`
	live := deployed(t, computeFed(t, first, nil))
	// The manifest drops the service site and db, and has a gateway called
	// site that's off here. That gateway isn't the site api still uses.
	yaml := "version: 1\ndefaults:\n  provider: cloud-run\nservices:\n  api:\n    repo: mono\n    source: services/api\n" +
		"gateways:\n  site:\n    routes:\n      - {path: /, to: api}\n    environments:\n      production:\n        enabled: false\n"

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	if site := action(t, r, "service:site"); site.Op != Keep {
		t.Fatalf("site %+v", site)
	}
	want := "site is gone from the manifest, but api in mono, which this plan holds back, still uses it, so it keeps running."
	if w := warning(r, "held", "site"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
}

func TestCompute_aGatewayTakingARetainedServicesNameIsPlannedOnce(t *testing.T) {
	// api, in mono, needs site, in site, which needs db.
	first := `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [site]
  site:
    repo: site
    source: .
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`
	live := deployed(t, computeFed(t, first, nil))
	// The manifest drops the service site and db, and has a gateway called
	// site that's on here, so the gateway takes the place of what api, held,
	// was deployed to use.
	yaml := "version: 1\ndefaults:\n  provider: cloud-run\nservices:\n  api:\n    repo: mono\n    source: services/api\n" +
		"gateways:\n  site:\n    routes:\n      - {path: /, to: api}\n"

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	var sites []Action
	for _, a := range r.Actions {
		if a.ID == "service:site" {
			sites = append(sites, a)
		}
	}
	if len(sites) != 1 || sites[0].Op == Keep || sites[0].Service == nil || len(sites[0].Service.Gateway) != 1 {
		t.Fatalf("site actions %+v", sites)
	}
	want := "site is a gateway in the manifest now, so it replaces the site that api in mono, which this plan holds back, was deployed to use."
	if w := warning(r, "held", "site"); w == nil || w.Message != want {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	// Nothing that runs on uses the site that needed db any more.
	if db := action(t, r, "resource:db"); db.Op != Delete {
		t.Fatalf("db %+v", db)
	}
}

func TestCompute_aGatewayTakingADroppedHeldServicesNameIsPlannedOnce(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	// The manifest drops api, which is in mono, and has a gateway called
	// api that's on here, so the gateway takes its place.
	yaml := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	yaml = strings.Replace(yaml, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)
	yaml += "gateways:\n  api:\n    routes:\n      - {path: /, to: site}\n"

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	var apis []Action
	for _, a := range r.Actions {
		if a.ID == "service:api" {
			apis = append(apis, a)
		}
	}
	if len(apis) != 1 || apis[0].Op == Keep || apis[0].Op == Delete || apis[0].Service == nil || len(apis[0].Service.Gateway) != 1 {
		t.Fatalf("api actions %+v", apis)
	}
	var held []string
	for _, w := range r.Warnings {
		if w.Code == "held" && w.Name == "api" {
			held = append(held, w.Message)
		}
	}
	want := "api is a gateway in the manifest now, so it replaces api in mono, which this plan holds back."
	if !slices.Equal(held, []string{want}) {
		t.Fatalf("held warnings %q", held)
	}
}

func TestCompute_aGatewayReplacingADroppedHeldServiceAnotherUsedWarnsOnce(t *testing.T) {
	// web, in mono beside api, needs api.
	first := strings.Replace(federatedYAML, "resources:\n", "  web:\n    repo: mono\n    source: apps/web\n    provider: vercel\n    needs: [api]\n"+"resources:\n", 1)
	live := deployed(t, computeFed(t, first, nil))
	// The manifest drops api and has a gateway called api that's on here,
	// while web, held, runs on with the link to api it was deployed with.
	yaml := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	yaml = strings.Replace(yaml, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)
	yaml = strings.Replace(yaml, "resources:\n", "  web:\n    repo: mono\n    source: apps/web\n    provider: vercel\n"+"resources:\n", 1)
	yaml += "gateways:\n  api:\n    routes:\n      - {path: /, to: site}\n"

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	var held []string
	for _, w := range r.Warnings {
		if w.Code == "held" && w.Name == "api" {
			held = append(held, w.Message)
		}
	}
	want := "api is a gateway in the manifest now, so it replaces api in mono, which this plan holds back. web in mono, which this plan holds back too, was deployed to use the api it replaces."
	if !slices.Equal(held, []string{want}) {
		t.Fatalf("held warnings %q", held)
	}
}

func TestCompute_aHeldServiceDeletedOutsideLoomplaneIsNeededByWhatsCreatedAgain(t *testing.T) {
	// site, which the plan moves, was deleted too, so it's created again,
	// and it still needs api, which the plan holds and can't rebuild.
	first := deployed(t, computeFed(t, federatedYAML, nil))
	live := drifted(drifted(first, "api", DriftMissing, ""), "site", DriftMissing, "")

	r := computeFed(t, federatedYAML, []string{"mono"}, live...)
	if site := action(t, r, "service:site"); site.Op != Create {
		t.Fatalf("site %+v", site)
	}
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-missing:api"}) {
		t.Fatalf("problems %v", got)
	}
}

func TestCompute_aHeldServiceThatsOffDeletedAndNeededIsAProblem(t *testing.T) {
	// api is off here, and site, held too, runs on with the link to api it
	// was deployed with.
	off := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	off = strings.Replace(off, "    needs: [db]\n", "    needs: [db]\n    environments:\n      production:\n        enabled: false\n", 1)
	live := drifted(deployed(t, computeFed(t, federatedYAML, nil)), "api", DriftMissing, "")

	r := computeFed(t, off, []string{"mono", "site"}, live...)
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-missing:api"}) {
		t.Fatalf("problems %v", got)
	}
	want := "api was deleted outside Loomplane, and mono, which it comes from, is held, so this plan can't bring it back. It's off in production, so turn it on and add mono to the scope."
	if r.Problems[0].Message != want {
		t.Fatalf("problem %+v", r.Problems[0])
	}
}

func TestCompute_aHeldServiceUsingAReplacedServiceKeepsTheOldAddress(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	// api, which the plan moves, moves region, so it's replaced, and site,
	// held, was deployed with the old api's address.
	moved := strings.Replace(federatedYAML, "    source: services/api\n", "    source: services/api\n    region: europe-west1\n", 1)

	r := computeFed(t, moved, []string{"site"}, live...)
	if api := action(t, r, "service:api"); api.Op != Replace {
		t.Fatalf("api %+v", api)
	}
	if site := action(t, r, "service:site"); site.Op != Keep {
		t.Fatalf("site %+v", site)
	}
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-address:site"}) {
		t.Fatalf("problems %v", got)
	}
	want := "site in site, which this plan holds back, uses api's address, and this plan replaces api, so site keeps the old one's address, which the deployment removes once the new one is up. Add site to the scope."
	if p := r.Problems[0]; p.Message != want || p.Path != "services.site" || p.Subject != ServiceSubject {
		t.Fatalf("problem %+v", p)
	}

	// Moving site too gives it the new address.
	r = computeFed(t, moved, nil, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	if site := action(t, r, "service:site"); site.Op != Update {
		t.Fatalf("site %+v", site)
	}
}

func TestCompute_whatUsesAReplacedDependencyIsDeployedAgain(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))

	// api moves region, so it's replaced, and site, unchanged, needs its
	// new address, so it's deployed again after it.
	moved := strings.Replace(federatedYAML, "    source: services/api\n", "    source: services/api\n    region: europe-west1\n", 1)
	r := computeFed(t, moved, nil, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	api, site := action(t, r, "service:api"), action(t, r, "service:site")
	if api.Op != Replace {
		t.Fatalf("api %+v", api)
	}
	if site.Op != Update || site.Reason != "api moves, so site gets its new address." || site.Step <= api.Step {
		t.Fatalf("site %+v, api at step %d", site, api.Step)
	}

	// A resource replaced gives what uses it a new address too.
	moved = strings.Replace(federatedYAML, "    provider: cloud-sql\n", "    provider: cloud-sql\n    region: europe-west1\n", 1)
	r = computeFed(t, moved, nil, live...)
	db, api := action(t, r, "resource:db"), action(t, r, "service:api")
	if db.Op != Replace || api.Op != Update || api.Reason != "db moves, so api gets its new address." || api.Step <= db.Step {
		t.Fatalf("db %+v, api %+v", db, api)
	}
	if site := action(t, r, "service:site"); site.Op != Keep {
		t.Fatalf("site %+v", site)
	}

	// api deleted outside Loomplane is created again, at a new address.
	r = computeFed(t, federatedYAML, nil, drifted(live, "api", DriftMissing, "")...)
	if site := action(t, r, "service:site"); action(t, r, "service:api").Op != Create || site.Op != Update {
		t.Fatalf("site %+v", site)
	}

	// Nothing moves, nothing is deployed again.
	if r := computeFed(t, federatedYAML, nil, live...); r.Summary != (Summary{Keep: 3}) {
		t.Fatalf("summary %+v", r.Summary)
	}
}

func TestCompute_aGatewayRoutingToAReplacedServiceIsDeployedAgain(t *testing.T) {
	live := deployed(t, computeFed(t, edgeYAML, nil))
	moved := strings.Replace(edgeYAML, "    source: services/api\n", "    source: services/api\n    region: europe-west1\n", 1)

	r := computeFed(t, moved, nil, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	api, edge := action(t, r, "service:api"), action(t, r, "service:edge")
	if api.Op != Replace {
		t.Fatalf("api %+v", api)
	}
	if edge.Op != Update || edge.Reason != "api moves, so edge gets its new address." || edge.Step <= api.Step {
		t.Fatalf("edge %+v, api at step %d", edge, api.Step)
	}
}

func TestCompute_aHeldServiceUsingAReplacedGatewayKeepsTheOldAddress(t *testing.T) {
	// api, in mono, needs site, in site.
	first := `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [site]
  site:
    repo: site
    source: .
`
	live := deployed(t, computeFed(t, first, nil))
	// A gateway called site on ECS takes the name of the site on Cloud Run
	// that api, held, was deployed to use, so it replaces it.
	yaml := "version: 1\ndefaults:\n  provider: cloud-run\nservices:\n  api:\n    repo: mono\n    source: services/api\n" +
		"gateways:\n  site:\n    provider: ecs-fargate\n    routes:\n      - {path: /, to: api}\n"

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if site := action(t, r, "service:site"); site.Op != Replace || len(site.Service.Gateway) != 1 {
		t.Fatalf("site %+v", site)
	}
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-address:api"}) {
		t.Fatalf("problems %v", got)
	}
	want := "api in mono, which this plan holds back, uses site's address, and this plan replaces site, so api keeps the old one's address, which the deployment removes once the new one is up. Add mono to the scope."
	if r.Problems[0].Message != want {
		t.Fatalf("problem %+v", r.Problems[0])
	}
	// The problem says what api keeps, so no held warning says it again.
	if w := warning(r, "held", "site"); w != nil {
		t.Fatalf("warning %+v", w)
	}
}

func TestCompute_aServiceRetainedForAHeldServiceUsingAReplacedResourceKeepsTheOldAddress(t *testing.T) {
	// api, in mono, needs site, in site, which needs db.
	first := `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [site]
  site:
    repo: site
    source: .
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`
	live := deployed(t, computeFed(t, first, nil))
	// The manifest drops site, which is kept for api, held, and moves db
	// to another region, so db is replaced under site.
	yaml := "version: 1\ndefaults:\n  provider: cloud-run\nservices:\n  api:\n    repo: mono\n    source: services/api\n" +
		"resources:\n  db:\n    kind: postgres\n    provider: cloud-sql\n    region: europe-west1\n"

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if db := action(t, r, "resource:db"); db.Op != Replace {
		t.Fatalf("db %+v", db)
	}
	if site := action(t, r, "service:site"); site.Op != Keep || !strings.HasPrefix(site.Reason, "Kept") {
		t.Fatalf("site %+v", site)
	}
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-address:site"}) {
		t.Fatalf("problems %v", got)
	}
	want := "site, which this plan keeps for mono, which it holds back, uses db's address, and this plan replaces db, so site keeps the old one's address, which the deployment removes once the new one is up. Add mono to the scope."
	if r.Problems[0].Message != want {
		t.Fatalf("problem %+v", r.Problems[0])
	}

	// site deleted outside Loomplane reaches nothing anyway.
	r = computeFed(t, yaml, []string{"mono"}, drifted(live, "site", DriftMissing, "")...)
	if slices.Contains(codes(r.Problems), "held-address:site") {
		t.Fatalf("problems %v", codes(r.Problems))
	}
}

func TestCompute_aDroppedHeldServiceUsingAReplacedResourceKeepsTheOldAddress(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	// The manifest drops api, in mono, held, which runs on with db's
	// address, and moves db to another region, so db is replaced.
	yaml := strings.Replace(federatedYAML, "    needs: [api]\n", "", 1)
	yaml = strings.Replace(yaml, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)
	yaml = strings.Replace(yaml, "    provider: cloud-sql\n", "    provider: cloud-sql\n    region: europe-west1\n", 1)

	r := computeFed(t, yaml, []string{"mono"}, live...)
	if db := action(t, r, "resource:db"); db.Op != Replace {
		t.Fatalf("db %+v", db)
	}
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-address:api"}) {
		t.Fatalf("problems %v", got)
	}
	want := "api in mono, which this plan holds back, uses db's address, and this plan replaces db, so api keeps the old one's address, which the deployment removes once the new one is up. Add mono to the scope."
	if p := r.Problems[0]; p.Message != want || p.Path != "services.api" {
		t.Fatalf("problem %+v", p)
	}

	// api deleted outside Loomplane reaches nothing anyway.
	r = computeFed(t, yaml, []string{"mono"}, drifted(live, "api", DriftMissing, "")...)
	if slices.Contains(codes(r.Problems), "held-address:api") {
		t.Fatalf("problems %v", codes(r.Problems))
	}
}

func TestCompute_aHeldServiceUsingAServiceCreatedAgainKeepsTheOldAddress(t *testing.T) {
	// api was deleted outside Loomplane, so it's created again at a new
	// address, and site, held, was deployed with the old one.
	live := drifted(deployed(t, computeFed(t, federatedYAML, nil)), "api", DriftMissing, "")

	r := computeFed(t, federatedYAML, []string{"site"}, live...)
	if api := action(t, r, "service:api"); api.Op != Create {
		t.Fatalf("api %+v", api)
	}
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-address:site"}) {
		t.Fatalf("problems %v", got)
	}
	want := "site in site, which this plan holds back, uses api's address, and this plan creates api again, so site keeps the old one's address, which this plan stops tracking. Add site to the scope."
	if r.Problems[0].Message != want {
		t.Fatalf("problem %+v", r.Problems[0])
	}
}

// onConnection is live as the runner records it: each binding names the
// connection that deployed it.
func onConnection(live []*Binding, id string) []*Binding {
	out := make([]*Binding, 0, len(live))
	for _, b := range live {
		c := *b
		c.ConnectionID = id
		out = append(out, &c)
	}
	return out
}

func TestCompute_aHeldServiceUsingAServiceCreatedAgainAtTheSameAddress(t *testing.T) {
	// api, on Cloud Run, was deleted outside Loomplane, so it's created
	// again with the name, region and connection it had, and site, held,
	// was deployed with its address.
	first := onConnection(deployed(t, computeFed(t, federatedYAML, nil)), "conn_gcp")
	live := drifted(first, "api", DriftMissing, "")
	warn := federatedYAML + "held: {same_address: warn}\n"
	block := federatedYAML + "held: {same_address: block}\n"
	problem := "site in site, which this plan holds back, uses api's address, and this plan creates api again, so site keeps the old one's address, which this plan stops tracking. Add site to the scope."

	// Unset, or block, it's a problem as ever.
	for _, yaml := range []string{federatedYAML, block} {
		r := computeFed(t, yaml, []string{"site"}, live...)
		if got := codes(r.Problems); !slices.Equal(got, []string{"held-address:site"}) || r.Problems[0].Message != problem {
			t.Fatalf("problems %+v", r.Problems)
		}
		if w := warning(r, "held-address", "site"); w != nil {
			t.Fatalf("warning %+v", w)
		}
	}

	// warn makes it a warning, since Cloud Run names the service the same.
	r := computeFed(t, warn, []string{"site"}, live...)
	if api := action(t, r, "service:api"); api.Op != Create || api.Provider != "cloud-run" {
		t.Fatalf("api %+v", api)
	}
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	w := warning(r, "held-address", "site")
	want := "site in site, which this plan holds back, uses api's address, and this plan creates api again on Cloud Run with the name, region and connection it had, so it should get the same address, and site should still reach it. Add site to the scope if it doesn't."
	if w == nil || w.Message != want || w.Path != "services.site" || w.Subject != ServiceSubject {
		t.Fatalf("warnings %+v", r.Warnings)
	}

	// Anything the planner can't be sure gives the same address is still
	// a problem with warn.
	elsewhere := func(yaml, field, value string) string {
		return strings.Replace(yaml, "    source: services/api\n", "    source: services/api\n    "+field+": "+value+"\n", 1)
	}
	cases := []struct {
		name string
		yaml string
		live []*Binding
		op   Op
	}{
		{"another provider", elsewhere(warn, "provider", "ecs-fargate"), live, Create},
		{"another region", elsewhere(warn, "region", "europe-west1"), live, Create},
		{"a replacement", elsewhere(warn, "region", "europe-west1"), first, Replace},
		{"another connection", warn, onConnection(live, "conn_old"), Create},
		{"no connection recorded", warn, onConnection(live, ""), Create},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := computeFed(t, tt.yaml, []string{"site"}, tt.live...)
			if api := action(t, r, "service:api"); api.Op != tt.op {
				t.Fatalf("api %+v", api)
			}
			if !slices.Contains(codes(r.Problems), "held-address:site") {
				t.Fatalf("problems %v", codes(r.Problems))
			}
			if w := warning(r, "held-address", "site"); w != nil {
				t.Fatalf("warning %+v", w)
			}
		})
	}

	// A Cloud Run service the binding names no instance for gets a new
	// name, so a new address.
	noInstance := make([]*Binding, 0, len(live))
	for _, b := range live {
		c := *b
		if c.Name == "api" {
			c.ExternalID = ""
		}
		noInstance = append(noInstance, &c)
	}
	if r := computeFed(t, warn, []string{"site"}, noInstance...); !slices.Contains(codes(r.Problems), "held-address:site") {
		t.Fatalf("problems %v", codes(r.Problems))
	}
}

// federatedOnECS is federatedYAML with both services on ECS, where site reaches
// api by a fixed address inside the environment's network.
const federatedOnECS = `version: 1
defaults:
  provider: ecs-fargate
services:
  api:
    repo: mono
    source: services/api
  site:
    repo: site
    source: .
    needs: [api]
ignore: {resources: [postgres, redis]}
`

func TestCompute_aFixedAddressThatAMoveLeavesAloneIsntRedeployed(t *testing.T) {
	first := computeFed(t, federatedOnECS, nil)
	if len(first.Problems) != 0 {
		t.Fatalf("problems %v", codes(first.Problems))
	}
	if env := action(t, first, "service:site").Service.Env; len(env) != 1 || env[0].Address != "http://api:8080" {
		t.Fatalf("the test needs site to reach api at a fixed address: %+v", env)
	}
	live := deployed(t, first)
	region := strings.Replace(federatedOnECS, "    source: services/api\n", "    source: services/api\n    region: us-west-2\n", 1)
	cloudRun := strings.Replace(federatedOnECS, "    source: services/api\n", "    source: services/api\n    provider: cloud-run\n", 1)

	// api moves region, and site still reaches it at http://api:8080.
	r := computeFed(t, region, nil, live...)
	if api := action(t, r, "service:api"); api.Op != Replace {
		t.Fatalf("api %+v", api)
	}
	if site := action(t, r, "service:site"); site.Op != Keep {
		t.Fatalf("site %+v", site)
	}
	// Nor is site, held, left with an old address.
	if held := computeFed(t, region, []string{"site"}, live...); len(held.Problems) != 0 {
		t.Fatalf("problems %v", codes(held.Problems))
	}
	if held := computeFed(t, federatedOnECS, []string{"site"}, drifted(live, "api", DriftMissing, "")...); len(held.Problems) != 0 {
		t.Fatalf("problems %v", codes(held.Problems))
	}

	// On Cloud Run, api has an address of its own, so the fixed one is
	// dead.
	r = computeFed(t, cloudRun, nil, live...)
	if site := action(t, r, "service:site"); site.Op != Update {
		t.Fatalf("site %+v", site)
	}
	r = computeFed(t, cloudRun, []string{"site"}, live...)
	if got := codes(r.Problems); !slices.Equal(got, []string{"held-address:site"}) {
		t.Fatalf("problems %v", got)
	}
}

func TestCompute_buildContext(t *testing.T) {
	// The api's Dockerfile builds from its own directory, which the plan
	// leaves unsaid, so bindings from before build contexts don't change.
	r := compute(t, acme, "production")
	if api := action(t, r, "service:api"); api.Service.Image != "services/api/Dockerfile" || api.Service.Context != "" || api.Service.ContextDir() != "services/api" {
		t.Fatalf("api %+v", api.Service)
	}

	m := strings.Replace(acme, "    source: services/api\n", "    source: services/api\n    build: {context: .}\n", 1)
	m = strings.Replace(m, "    source: apps/gateway\n", "    source: apps/gateway\n    build: {context: apps}\n", 1)
	r = compute(t, m, "production")
	api := action(t, r, "service:api")
	if api.Service.Context != "." || api.Service.DockerfileIn(api.Service.ContextDir()) != "services/api/Dockerfile" {
		t.Fatalf("api %+v", api.Service)
	}
	// The gateway has no Dockerfile, so there's nothing to build from it.
	if gw := action(t, r, "service:gateway"); gw.Service.Context != "" || !slices.Contains(codes(r.Warnings), "build-context-ignored:gateway") {
		t.Fatalf("gateway %+v, findings %v", gw.Service, codes(r.Warnings))
	}
}

func TestServiceConfig_DockerfileIn(t *testing.T) {
	for _, tc := range []struct{ image, dir, want string }{
		{"services/api/Dockerfile", "services/api", "Dockerfile"},
		{"services/api/Dockerfile", ".", "services/api/Dockerfile"},
		{"services/api/Dockerfile", "services", "api/Dockerfile"},
		{"docker/api.Dockerfile", "services/api", "../../docker/api.Dockerfile"},
		{"Dockerfile", ".", "Dockerfile"},
	} {
		c := &ServiceConfig{Image: tc.image}
		if got := c.DockerfileIn(tc.dir); got != tc.want {
			t.Errorf("DockerfileIn(%s, %s) = %s, want %s", tc.image, tc.dir, got, tc.want)
		}
	}
}

// computeOn is computeFed with these connections, and gcp chosen as
// conn_gcp.
func computeOn(t *testing.T, conns []ConnectionOption, held []string, bindings ...*Binding) *Result {
	t.Helper()
	h := map[string]bool{}
	for _, a := range held {
		h[a] = true
	}
	return Compute(Input{
		Environment: "production", Graph: federated(t), Manifest: parse(t, federatedYAML),
		Providers: providers.Registry(), Bindings: bindings, Held: h, Connections: conns,
		Chosen: map[string]string{"gcp": "conn_gcp"},
	})
}

// deployedThrough is what federatedYAML deploys, each through the
// connection it names by binding name, or the plan's.
func deployedThrough(t *testing.T, conns map[string]string) []*Binding {
	t.Helper()
	live := deployed(t, computeFed(t, federatedYAML, nil))
	for _, b := range live {
		b.ConnectionID = map[string]string{"cloud-run": "conn_gcp", "cloud-sql": "conn_gcp", "vercel": "conn_vercel"}[b.Provider]
		if id, ok := conns[b.Name]; ok {
			b.ConnectionID = id
		}
	}
	return live
}

func TestCompute_aServiceOnAnotherConnectionIsReplaced(t *testing.T) {
	old := append(slices.Clone(connected), ConnectionOption{ID: "conn_old", Name: "Old", Cloud: "gcp", Ready: true})
	live := deployedThrough(t, map[string]string{"api": "conn_old"})

	r := computeOn(t, old, nil, live...)
	if len(r.Problems) != 0 || !r.Executable {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	api := action(t, r, "service:api")
	want := "api moves from Old to Prod, which production now deploys to Google Cloud with. The new one starts first, and the old one is removed through Old once it's up."
	if api.Op != Replace || api.Reason != want {
		t.Fatalf("api %s: %q, want %q", api.Op, api.Reason, want)
	}
	// So what uses api gets its new address.
	if site := action(t, r, "service:site"); site.Op != Update || site.Reason != "api moves, so site gets its new address." {
		t.Fatalf("site %s: %s", site.Op, site.Reason)
	}
	if db := action(t, r, "resource:db"); db.Op != Keep {
		t.Fatalf("db %s", db.Op)
	}

	// A held service that uses it keeps the old address, which the
	// deployment removes, so that's a problem.
	held := computeOn(t, old, []string{"site"}, live...)
	if !slices.Contains(codes(held.Problems), "held-address:site") {
		t.Fatalf("problems %v", codes(held.Problems))
	}

	// The old connection was removed: the reason says the deployment
	// can't remove what it ran on.
	gone := computeOn(t, connected, nil, live...)
	if api := action(t, gone, "service:api"); api.Op != Replace || !strings.Contains(api.Reason, "from a connection that's been removed") ||
		!strings.Contains(api.Reason, "says how to remove it yourself") {
		t.Fatalf("api %s: %s", api.Op, api.Reason)
	}

	// A binding from before bindings recorded their connection could be
	// on either, so it isn't moved here: the runner finds out.
	legacy := deployedThrough(t, map[string]string{"api": ""})
	if api := action(t, computeOn(t, old, nil, legacy...), "service:api"); api.Op != Keep {
		t.Fatalf("api from before connections were recorded %s: %s", api.Op, api.Reason)
	}
}

func TestCompute_aResourceOnAnotherConnectionIsAProblem(t *testing.T) {
	old := append(slices.Clone(connected), ConnectionOption{ID: "conn_old", Name: "Old", Cloud: "gcp", Ready: true})
	live := deployedThrough(t, map[string]string{"db": "conn_old"})

	r := computeOn(t, old, nil, live...)
	if got := codes(r.Problems); !slices.Equal(got, []string{"connection-moved:db"}) || r.Executable {
		t.Fatalf("problems %v, executable %v", got, r.Executable)
	}
	want := "db was provisioned through Old, and production now deploys to Google Cloud with Prod. Loomplane doesn't move data between connections, so provisioning db through Prod would start an empty one and leave the one holding its data behind. Choose Old for Google Cloud again to keep db where it is."
	if p := r.Problems[0]; p.Message != want || p.Path != "resources.db" || p.Subject != ResourceSubject {
		t.Fatalf("problem %+v", p)
	}

	gone := computeOn(t, connected, nil, live...)
	if got := codes(gone.Problems); !slices.Equal(got, []string{"connection-moved:db"}) ||
		!strings.Contains(gone.Problems[0].Message, "through a connection that's been removed") {
		t.Fatalf("problems %+v", gone.Problems)
	}

	// A binding from before bindings recorded their connection could be
	// on either, so it isn't a problem.
	legacy := deployedThrough(t, map[string]string{"db": ""})
	if r := computeOn(t, old, nil, legacy...); len(r.Problems) != 0 {
		t.Fatalf("a binding from before connections were recorded: %v", codes(r.Problems))
	}
}

func TestCompute_whatAMoveLeftRunningIsAWarning(t *testing.T) {
	live := deployedThrough(t, nil)
	for _, b := range live {
		if b.Name == "api" {
			b.Replaced = []Binding{{Subject: ServiceSubject, Name: "api", Provider: "ecs-fargate", Region: "us-east-1", ExternalID: "inst_old"}}
		}
	}
	r := computeOn(t, connected, nil, live...)
	w := warning(r, "still-replaced", "api")
	want := "api moved, and the old one on ecs-fargate in us-east-1 (inst_old) hasn't been removed yet. This plan keeps api as it is, so it won't. Retry the deployment that moved api, or deploy a change to it, and that removes it."
	if w == nil || w.Message != want || w.Path != "services.api" {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	if !r.Executable {
		t.Fatalf("problems %v", codes(r.Problems))
	}

	// A plan that deploys api removes it.
	for _, b := range live {
		if b.Name == "api" {
			b.CommitSHA = strings.Repeat("0", 40)
		}
	}
	r = computeOn(t, connected, nil, live...)
	if w := warning(r, "still-replaced", "api"); w == nil || !strings.HasSuffix(w.Message, "This plan's deployment removes it, and warns if it can't.") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	// Nothing else in the plan is on AWS, but the deployment may need the
	// connection to reach the old one.
	if r.Connections["aws"] != "conn_aws" || len(r.Problems) != 0 {
		t.Fatalf("connections %v, problems %v", r.Connections, codes(r.Problems))
	}
	// Deleting api removes it first, and warns if it can't go.
	gone := strings.Replace(federatedYAML, "  api:\n    repo: mono\n    source: services/api\n    needs: [db]\n", "", 1)
	gone = strings.Replace(gone, "    needs: [api]\n", "", 1)
	del := Compute(Input{
		Environment: "production", Graph: federated(t), Manifest: parse(t, gone),
		Providers: providers.Registry(), Bindings: live, Connections: connected, Chosen: map[string]string{"gcp": "conn_gcp"},
	})
	if a := action(t, del, "service:api"); a.Op != Delete {
		t.Fatalf("api %s", a.Op)
	}
	if w := warning(del, "still-replaced", "api"); w == nil || !strings.HasSuffix(w.Message, "This plan's deployment removes it before it deletes api. If it can't, it warns, and Loomplane stops tracking it, so you delete it yourself.") {
		t.Fatalf("warnings %+v", del.Warnings)
	}
	// With no AWS connection, the plan still goes ahead.
	var noAWS []ConnectionOption
	for _, c := range connected {
		if c.Cloud != "aws" {
			noAWS = append(noAWS, c)
		}
	}
	if r := computeOn(t, noAWS, nil, live...); !r.Executable || r.Connections["aws"] != "" {
		t.Fatalf("without aws: problems %v, connections %v", codes(r.Problems), r.Connections)
	}
}

// A delete goes through the connection that deployed what it deletes, so
// the plan needs no connection of its own to that cloud. Only one from
// before bindings recorded their connection goes through the plan's.
func TestCompute_aDeleteGoesThroughItsBindingsConnection(t *testing.T) {
	// Two Render connections and none chosen: nothing settles which one
	// production deploys to Render with.
	two := append(slices.Clone(connected), ConnectionOption{ID: "conn_render_2", Name: "Render 2", Cloud: "render", Ready: true})
	gone := func(conn string) []*Binding {
		return append(deployedThrough(t, nil), &Binding{
			ID: "bind_old", Subject: ServiceSubject, Name: "old", Provider: "render", ExternalID: "srv_old", ConnectionID: conn,
		})
	}

	r := computeOn(t, two, nil, gone("conn_render")...)
	if a := action(t, r, "service:old"); a.Op != Delete || a.ConnectionID != "conn_render" {
		t.Fatalf("delete destination: %+v", a)
	}
	if !r.Executable || len(r.Problems) != 0 || r.Connections["render"] != "" {
		t.Fatalf("problems %v, connections %v", codes(r.Problems), r.Connections)
	}
	if w := warning(r, "delete-unreachable", "old"); w != nil {
		t.Fatalf("warnings %+v", r.Warnings)
	}

	// Its connection was removed: the plan goes ahead, and says what that
	// leaves to you.
	r = computeOn(t, two, nil, gone("conn_render_gone")...)
	if a := action(t, r, "service:old"); a.ConnectionID != "" {
		t.Fatalf("missing delete destination entered active connection uses: %+v", a)
	}
	w := warning(r, "delete-unreachable", "old")
	if !r.Executable || w == nil || w.Path != "services.old" ||
		w.Message != "old was deployed through a connection that's been removed, so the deployment can't reach it to delete it. Loomplane stops tracking it, and if it still runs, you delete it yourself." {
		t.Fatalf("problems %v, warnings %+v", codes(r.Problems), r.Warnings)
	}

	// A binding that names no connection goes through the plan's: the
	// plan pins it when there's one, and goes ahead when there isn't.
	if r := computeOn(t, connected, nil, gone("")...); !r.Executable || r.Connections["render"] != "conn_render" {
		t.Fatalf("problems %v, connections %v", codes(r.Problems), r.Connections)
	}
	if r := computeOn(t, two, nil, gone("")...); !r.Executable || r.Connections["render"] != "" {
		t.Fatalf("problems %v, connections %v", codes(r.Problems), r.Connections)
	}
}

func TestCompute_pythonStartAssumed(t *testing.T) {
	g := *polyglot(t)
	g.Services = append(slices.Clone(g.Services), graph.Service{
		ID: "svc:python:services/ml", Name: "ml", Kind: graph.HTTPService, Deployable: true,
		Ecosystem: pd.Python, Path: "services/ml", Entrypoint: "services/ml", Confidence: pd.Likely,
	})
	in := func(start string) *Result {
		g.Services[len(g.Services)-1].Commands.Start = start
		return Compute(Input{
			Environment: "production", Graph: &g, Providers: providers.Registry(), Connections: connected,
			Manifest: parse(t, "version: 1\ndefaults:\n  provider: cloud-run\nservices:\n  ml:\n    source: services/ml\n    port: 8080\n"),
		})
	}
	if r := in(""); !slices.Contains(codes(r.Warnings), "start-assumed:ml") {
		t.Fatalf("no start command and no warning: %v", codes(r.Warnings))
	}
	if r := in("gunicorn shop.wsgi"); slices.Contains(codes(r.Warnings), "start-assumed:ml") {
		t.Fatalf("a Procfile's start, and still a warning: %v", codes(r.Warnings))
	}
}

// federatedOnAWS is federatedYAML with everything on AWS.
const federatedOnAWS = `version: 1
defaults:
  provider: ecs-fargate
services:
  api:
    repo: mono
    source: services/api
    needs: [db]
  site:
    repo: site
    source: .
    needs: [api]
resources:
  db:
    kind: postgres
    provider: rds
`

func TestCompute_onAWSTheConnectionDecidesTheRegion(t *testing.T) {
	ireland := ConnectionOption{ID: "conn_ie", Name: "Dublin", Cloud: "aws", Ready: true, Region: "eu-west-1"}
	oregon := ConnectionOption{ID: "conn_or", Name: "Oregon", Cloud: "aws", Ready: true, Region: "us-west-2"}
	plan := func(yaml string, chosen string, bindings ...*Binding) *Result {
		return Compute(Input{
			Environment: "production", Graph: federated(t), Manifest: parse(t, yaml),
			Providers: providers.Registry(), Bindings: bindings,
			Connections: []ConnectionOption{ireland, oregon}, Chosen: map[string]string{"aws": chosen},
		})
	}
	inOregon := func(yaml, name string) string {
		return strings.Replace(yaml, "  "+name+":\n", "  "+name+":\n    region: us-west-2\n", 1)
	}

	// With no region named, everything goes where the connection is.
	first := plan(federatedOnAWS, "conn_ie")
	if len(first.Problems) != 0 || !first.Executable {
		t.Fatalf("problems %v", codes(first.Problems))
	}
	for _, a := range first.Actions {
		if a.Region != "eu-west-1" {
			t.Fatalf("%s is in %q, not the connection's region", a.ID, a.Region)
		}
	}

	// Naming another region is a problem, since the deployment would
	// put it in the connection's anyway.
	r := plan(inOregon(federatedOnAWS, "db"), "conn_ie")
	if got := codes(r.Problems); !slices.Equal(got, []string{"connection-region:db"}) || r.Executable {
		t.Fatalf("problems %v", got)
	}
	want := "db can't be in us-west-2: on AWS, Loomplane deploys to the connection's region, and Dublin is in eu-west-1. Set resources.db.region to eu-west-1, or choose an AWS connection in us-west-2 for production."
	if p := r.Problems[0]; p.Message != want || p.Path != "resources.db.region" || p.Subject != ResourceSubject {
		t.Fatalf("problem %+v", p)
	}

	// So is moving what's deployed to another region with the same
	// connection: the deployment would keep the one it was to replace.
	live := deployed(t, first)
	for _, b := range live {
		b.ConnectionID = "conn_ie"
	}
	r = plan(inOregon(federatedOnAWS, "api"), "conn_ie", live...)
	if api := action(t, r, "service:api"); api.Op != Replace {
		t.Fatalf("api %+v", api)
	}
	if got := codes(r.Problems); !slices.Equal(got, []string{"connection-region:api"}) || r.Problems[0].Path != "services.api.region" {
		t.Fatalf("problems %+v", r.Problems)
	}

	// With a connection in the new region, the move is real.
	moved := plan(inOregon(inOregon(inOregon(federatedOnAWS, "api"), "site"), "db"), "conn_or", live...)
	if len(moved.Problems) != 0 {
		t.Fatalf("problems %v", codes(moved.Problems))
	}
	if moved.Summary.Replace != 3 {
		t.Fatalf("summary %+v", moved.Summary)
	}
	// And so is the region no one named, since it's the connection's.
	if unnamed := plan(federatedOnAWS, "conn_or", live...); len(unnamed.Problems) != 0 || unnamed.Summary.Replace != 3 {
		t.Fatalf("summary %+v, problems %v", unnamed.Summary, codes(unnamed.Problems))
	}

	// What's kept isn't deployed, so it stays where it is.
	before := deployed(t, plan(inOregon(federatedOnAWS, "db"), "conn_or"))
	r = plan(inOregon(federatedOnAWS, "db"), "conn_ie", before...)
	if db := action(t, r, "resource:db"); db.Op != Keep || slices.Contains(codes(r.Problems), "connection-region:db") {
		t.Fatalf("db %+v, problems %v", db, codes(r.Problems))
	}
}

func TestCompute_renderServicesReachEachOtherPrivately(t *testing.T) {
	r := compute(t, "version: 1\ndefaults: {provider: render}\nservices:\n  gw: {source: apps/gateway, needs: [api]}\n  api: {source: services/api, public: false}\nignore: {resources: [postgres, redis]}\n", "production")
	if len(r.Problems) != 0 || !r.Executable {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	gw := action(t, r, "service:gw")
	if len(gw.Service.Env) != 1 || gw.Service.Env[0].Key != "API_URL" || gw.Service.Env[0].Address != "" || gw.Engine != provider.Loomplane {
		t.Fatalf("gw %+v", gw)
	}
}

func TestCompute_aMissingResourceOnAnotherConnectionIsCreatedAgain(t *testing.T) {
	old := append(slices.Clone(connected), ConnectionOption{ID: "conn_old", Name: "Old", Cloud: "gcp", Ready: true})
	live := drifted(deployedThrough(t, map[string]string{"db": "conn_old"}), "db", DriftMissing, "")
	r := computeOn(t, old, nil, live...)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	if db := action(t, r, "resource:db"); db.Op != Create {
		t.Fatalf("db %s: %s", db.Op, db.Reason)
	}
}

func TestCompute_aLinkLetsCloudRunReachPrivateThingsOnAWS(t *testing.T) {
	const crossed = `version: 1
services:
  gateway:
    source: apps/gateway
    provider: cloud-run
    needs: [api, db]
  api:
    source: services/api
    provider: ecs-fargate
    public: false
resources:
  db:
    kind: postgres
    provider: rds
ignore: {resources: [redis]}
`
	// Without a link, neither is reachable from Cloud Run.
	r := compute(t, crossed, "production")
	got := codes(r.Problems)
	if !slices.Contains(got, "private-across-clouds:gateway") || !slices.Contains(got, "unreachable-resource:gateway") {
		t.Fatalf("problems without a link %v", got)
	}

	r = compute(t, crossed+"links:\n  vpn:\n    between: [gcp, aws]\n", "production")
	if len(r.Problems) != 0 || !r.Executable {
		t.Fatalf("problems with a link %v", codes(r.Problems))
	}
	link := action(t, r, "resource:vpn")
	if link.Provider != "cross-cloud-vpn" || link.Resource.Kind != "private-link" || link.Step != 1 || link.Region == "" {
		t.Fatalf("link %+v", link)
	}
	gw, api := action(t, r, "service:gateway"), action(t, r, "service:api")
	if !gw.Service.PrivateEgress || !slices.Contains(gw.DependsOn, "resource:vpn") || len(gw.Service.Env) != 2 {
		t.Fatalf("gateway %+v", gw.Service)
	}
	if !api.Service.Internal || api.Service.Public {
		t.Fatalf("api %+v", api.Service)
	}
	// The link is wanted, so the next plan keeps it.
	again := compute(t, crossed+"links:\n  vpn:\n    between: [gcp, aws]\n", "production", deployed(t, r)...)
	if a := action(t, again, "resource:vpn"); a.Op != Keep {
		t.Fatalf("second plan's link %+v", a)
	}
}

func TestCompute_aLinkDoesntLetAWSReachPrivateCloudRun(t *testing.T) {
	r := compute(t, `version: 1
services:
  worker:
    source: services/worker/cmd/worker
    provider: ecs-fargate
    needs: [api]
  api:
    source: services/api
    provider: cloud-run
    public: false
links:
  vpn: {between: [aws, gcp]}
`, "production")
	if !slices.Contains(codes(r.Problems), "private-across-clouds:worker") {
		t.Fatalf("problems %v", codes(r.Problems))
	}
}

// envKeyPlan is a service on a server that needs a MariaDB database and a
// Valkey cache on the same server.
func envKeyPlan(t *testing.T, bindings ...*Binding) *Result {
	t.Helper()
	doc := "version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: " + targetA + ", public: false, needs: [db, cache]}\n" +
		"resources:\n  db: {kind: mariadb, provider: server, target: " + targetA + "}\n  cache: {kind: valkey, provider: server, target: " + targetA + "}\n"
	connections := infrastructureConnections(t)
	connections[0].Details["architecture"] = "arm64"
	return Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc),
		Providers: providers.Registry(), Connections: connections, Bindings: bindings,
	})
}

func TestMariaDBAndValkeyConsumersGetConventionalKeys(t *testing.T) {
	r := envKeyPlan(t)
	if len(r.Problems) != 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	got := action(t, r, "service:api").Service.Env
	for _, want := range []EnvLink{
		{Key: "DATABASE_URL", From: "db", Subject: ResourceSubject},
		{Key: "REDIS_URL", From: "cache", Subject: ResourceSubject},
	} {
		if !slices.ContainsFunc(got, func(e EnvLink) bool { return reflect.DeepEqual(e, want) }) {
			t.Errorf("api env lacks %+v: %+v", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("api env = %+v, want only the two conventional variables", got)
	}
	if slices.Contains(codes(r.Warnings), "env-key-renamed:api") {
		t.Errorf("a new service was warned about a rename: %v", codes(r.Warnings))
	}
}

func TestDeployedConsumerKeepsLegacyKeyWithWarning(t *testing.T) {
	cfg, err := json.Marshal(ServiceConfig{Env: []EnvLink{
		{Key: "DB_URL", From: "db", Subject: ResourceSubject},
		{Key: "CACHE_URL", From: "cache", Subject: ResourceSubject},
	}})
	if err != nil {
		t.Fatal(err)
	}
	live := &Binding{ID: "binding", Name: "api", Subject: ServiceSubject, Provider: "server", ConnectionID: targetA, ExternalID: "ext-api", Config: cfg}
	r := envKeyPlan(t, live)
	env := action(t, r, "service:api").Service.Env
	for _, key := range []string{"DB_URL", "DATABASE_URL", "CACHE_URL", "REDIS_URL"} {
		if !slices.ContainsFunc(env, func(e EnvLink) bool { return e.Key == key }) {
			t.Errorf("api env lacks %s: %+v", key, env)
		}
	}
	w := warning(r, "env-key-renamed", "api")
	if w == nil {
		t.Fatalf("no env-key-renamed warning: %v", codes(r.Warnings))
	}
	if !strings.Contains(w.Message, "DB_URL") || !strings.Contains(w.Message, "DATABASE_URL") {
		t.Errorf("message = %q", w.Message)
	}
}
