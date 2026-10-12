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
    needs: [cache]
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

func compute(t *testing.T, yaml, env string, bindings ...*Binding) *Result {
	t.Helper()
	return Compute(Input{
		Environment: env, Graph: polyglot(t), Manifest: parse(t, yaml),
		Providers: providers.Registry(), Bindings: bindings,
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
		"provider-unavailable:cloud-run", "provider-unavailable:vercel",
	} {
		if !slices.Contains(got, w) {
			t.Errorf("no warning %s in %v", w, got)
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
	r := Compute(Input{Environment: "production", Graph: &g, Manifest: parse(t, yaml), Providers: providers.Registry()})
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
		Providers: providers.Registry(), Bindings: bindings, Held: h,
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
		Providers: providers.Registry(), Held: map[string]bool{"lib": true},
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
    provider: vercel
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
