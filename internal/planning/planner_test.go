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
	render := compute(t, "version: 1\ndefaults: {provider: render}\nservices:\n  api: {source: services/api}\nignore: {resources: [postgres, redis]}\n", "production")
	if render.Executable || !slices.Contains(codes(render.Warnings), "provider-unavailable:render") {
		t.Errorf("render: executable %v, warnings %v", render.Executable, codes(render.Warnings))
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
	want := "site in site, which this plan holds back, uses api's address, and this plan replaces api, so site keeps the old address until a plan moves site. Replacing api removes the old one, so site would reach nothing. Add site to the scope."
	if p := r.Problems[0]; p.Message != want || p.Path != "services.site" || p.Subject != ServiceSubject {
		t.Fatalf("problem %+v", p)
	}

	// Moving site too gives it the new address.
	if r := computeFed(t, moved, nil, live...); len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
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
	want := "api in mono, which this plan holds back, uses site's address, and this plan replaces site, so api keeps the old address until a plan moves mono. Replacing site removes the old one, so api would reach nothing. Add mono to the scope."
	if r.Problems[0].Message != want {
		t.Fatalf("problem %+v", r.Problems[0])
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
