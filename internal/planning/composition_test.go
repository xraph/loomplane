package planning

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/pkg/provider"
)

// composedConnections are the server targets, both checked as arm64.
func composedConnections(t *testing.T) []ConnectionOption {
	t.Helper()
	connections := infrastructureConnections(t)
	connections[0].Details["architecture"] = "arm64"
	connections[1].Details["architecture"] = "arm64"
	return connections
}

func composedPlan(t *testing.T, kinds *recipes.Registry, doc string, bindings ...*Binding) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: kinds, Bindings: bindings,
	})
}

// boundFrom is what a deployment of r's resource actions would have bound.
func boundFrom(t *testing.T, r *Result) []*Binding {
	t.Helper()
	var out []*Binding
	for _, a := range r.Actions {
		if a.Resource == nil {
			continue
		}
		raw, err := json.Marshal(a.Resource)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &Binding{ID: "binding-" + a.Name, Subject: a.Subject, Name: a.Name, Provider: a.Provider, ConnectionID: a.ConnectionID, ExternalID: "instance-" + a.Name, Engine: a.Engine, Config: raw})
	}
	return out
}

func findAction(r *Result, id string) *Action {
	for i := range r.Actions {
		if r.Actions[i].ID == id {
			return &r.Actions[i]
		}
	}
	return nil
}

func hasCode(fs []Finding, code string) bool {
	return slices.ContainsFunc(fs, func(f Finding) bool { return f.Code == code })
}

const sampleApp = "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + "}\n"

func TestRoutablePortsOnlyOnRoutableComponentPorts(t *testing.T) {
	r := composedPlan(t, recipestest.Registry(t), sampleApp)
	if !r.Executable {
		t.Fatalf("composed plan blocked: %+v", r.Problems)
	}
	for _, a := range r.Actions {
		data, err := json.Marshal(a.Resource)
		if err != nil {
			t.Fatal(err)
		}
		if a.ID == "resource:app--api" {
			if !bytes.Contains(data, []byte(`"routable_ports":[8080]`)) {
				t.Fatalf("api routable ports: %s", data)
			}
			continue
		}
		if bytes.Contains(data, []byte("routable_ports")) {
			t.Fatalf("%s has routable ports: %s", a.ID, data)
		}
	}
	for name, plan := range engineFixtures(t) {
		data, err := json.Marshal(plan.Actions)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("routable_ports")) {
			t.Fatalf("engine plan %s has routable ports: %s", name, data)
		}
	}
}

func TestCompositionExpandsIntoOrderedActions(t *testing.T) {
	r := composedPlan(t, recipestest.Registry(t), sampleApp)
	if !r.Executable {
		t.Fatalf("composed plan blocked: %+v", r.Problems)
	}
	want := []struct {
		id        string
		step      int
		dependsOn []string
	}{
		{"resource:app--postgres", 1, nil},
		{"resource:app--migrate", 2, []string{"resource:app--postgres"}},
		{"resource:app--api", 3, []string{"resource:app--migrate"}},
		{"resource:app--worker", 4, []string{"resource:app--api"}},
		{"resource:app", 5, []string{"resource:app--api", "resource:app--migrate", "resource:app--worker"}},
	}
	if len(r.Actions) != len(want) {
		t.Fatalf("actions: %+v", r.Actions)
	}
	for i, w := range want {
		a := r.Actions[i]
		if a.ID != w.id || a.Step != w.step || !slices.Equal(a.DependsOn, w.dependsOn) || a.Op != Create || a.ConnectionID != targetA {
			t.Fatalf("action %d: got %s step %d depends %v op %s, want %+v", i, a.ID, a.Step, a.DependsOn, a.Op, w)
		}
	}

	postgres := findAction(r, "resource:app--postgres").Resource
	if postgres.Kind != "postgres" || postgres.Catalog == nil || postgres.Catalog.ID != "postgresql-17" || postgres.Component != nil || postgres.Composition != nil {
		t.Fatalf("new dependency is not an ordinary database: %+v", postgres)
	}
	migrate := findAction(r, "resource:app--migrate")
	if c := migrate.Resource.Component; c == nil || c.Component.Role != recipes.RoleJob || c.Resource != "app" || c.Kind != "sample" || c.Revision != "1" || c.Protection != recipes.ProtectionNone || !strings.Contains(c.Component.Image, "sample-migrate@sha256:6b1f63a5") {
		t.Fatalf("migrate component: %+v", migrate.Resource.Component)
	}
	if migrate.Creates != "Sample migrate job" || findAction(r, "resource:app--api").Creates != "Sample api container" {
		t.Fatalf("creates: %q, %q", migrate.Creates, findAction(r, "resource:app--api").Creates)
	}

	choices := map[string]DependencyChoice{
		"postgres": {Mode: DependencyNew, Resource: "app--postgres", Kind: "postgres"},
		"cache":    {Mode: DependencyOff},
	}
	parent := findAction(r, "resource:app").Resource
	if parent.Composition == nil || parent.Component != nil || parent.Catalog != nil {
		t.Fatalf("parent: %+v", parent)
	}
	got := *parent.Composition
	if got.Kind != "sample" || got.Version != "1" || got.Revision != "1" || !reflect.DeepEqual(got.Dependencies, choices) || len(got.Outputs) != 2 || len(got.Secrets) != 1 {
		t.Fatalf("composition: %+v", got)
	}
	if !reflect.DeepEqual(migrate.Resource.Component.Dependencies, choices) {
		t.Fatalf("component choices: %+v", migrate.Resource.Component.Dependencies)
	}
}

func TestServiceNeedingCompositionRunsAfterIt(t *testing.T) {
	r := composedPlan(t, recipestest.Registry(t), sampleApp+"services:\n  web: {source: cmd/api, provider: server, target: "+targetA+", public: false, needs: [app]}\n")
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	web := findAction(r, "service:web")
	if web == nil || web.Step != 6 || !slices.Contains(web.DependsOn, "resource:app") {
		t.Fatalf("web: %+v", web)
	}
	want := []EnvLink{
		{Key: "SAMPLE_ADMIN_URL", From: "app", Subject: ResourceSubject, Field: "output:admin"},
		{Key: "SAMPLE_URL", From: "app", Subject: ResourceSubject, Field: "url"},
	}
	if !reflect.DeepEqual(web.Service.Env, want) {
		t.Fatalf("env: %+v %+v", web.Service.Env, r.Problems)
	}
}

func TestCompositionConnectsManifestResource(t *testing.T) {
	doc := "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {resource: db}}}\n  db: {kind: postgres, provider: server, target: " + targetA + "}\n"
	r := composedPlan(t, recipestest.Registry(t), doc)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	if findAction(r, "resource:app--postgres") != nil {
		t.Fatal("a connected dependency was provisioned too")
	}
	want := DependencyChoice{Mode: DependencyResource, Resource: "db", Kind: "postgres"}
	if got := findAction(r, "resource:app").Resource.Composition.Dependencies["postgres"]; got != want {
		t.Fatalf("choice: %+v", got)
	}
	if migrate := findAction(r, "resource:app--migrate"); !slices.Equal(migrate.DependsOn, []string{"resource:db"}) || migrate.Step != 2 {
		t.Fatalf("migrate: %+v", migrate)
	}
	if db := findAction(r, "resource:db"); db.Step != 1 {
		t.Fatalf("db: %+v", db)
	}
}

func TestCompositionRejectsIncompatibleConnection(t *testing.T) {
	for _, tc := range []struct{ name, doc, code string }{
		{"kind", "  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {resource: cache}}}\n  cache: {kind: redis, provider: server, target: " + targetA + "}\n", "catalog-connect-kind-mismatch"},
		{"composed", "  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {resource: other}}}\n  other: {kind: sample, provider: server, target: " + targetA + "}\n", "catalog-connect-kind-mismatch"},
		{"target", "  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {resource: db}}}\n  db: {kind: postgres, provider: server, target: " + targetB + "}\n", "catalog-connect-across-targets"},
		{"unknown", "  app: {kind: sample, provider: server, target: " + targetA + ", uses: {unknown: {resource: db}}}\n  db: {kind: postgres, provider: server, target: " + targetA + "}\n", "catalog-dependency-unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := composedPlan(t, recipestest.Registry(t), "version: 1\nresources:\n"+tc.doc)
			if r.Executable || !hasCode(r.Problems, tc.code) {
				t.Fatalf("want %s: %+v", tc.code, r.Problems)
			}
		})
	}
}

func TestCompositionOptionalDependency(t *testing.T) {
	r := composedPlan(t, recipestest.Registry(t), "version: 1\nresources:\n  app: {kind: sample, provider: server, target: "+targetA+", uses: {cache: {enabled: true}}}\n")
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	cache := findAction(r, "resource:app--cache")
	if cache == nil || cache.Step != 1 || cache.Resource.Kind != "redis" || cache.Resource.Catalog == nil {
		t.Fatalf("cache: %+v", cache)
	}
	if got := findAction(r, "resource:app").Resource.Composition.Dependencies["cache"]; got != (DependencyChoice{Mode: DependencyNew, Resource: "app--cache", Kind: "redis"}) {
		t.Fatalf("cache choice: %+v", got)
	}
	if migrate := findAction(r, "resource:app--migrate"); !slices.Equal(migrate.DependsOn, []string{"resource:app--cache", "resource:app--postgres"}) {
		t.Fatalf("migrate depends on %v", migrate.DependsOn)
	}

	bindings := boundFrom(t, r)
	r = composedPlan(t, recipestest.Registry(t), sampleApp)
	if findAction(r, "resource:app--cache") != nil {
		t.Fatal("an optional dependency that's off by default was provisioned")
	}

	// Turning it off again removes the one it provisioned.
	r = composedPlan(t, recipestest.Registry(t), sampleApp, bindings...)
	if cache := findAction(r, "resource:app--cache"); cache == nil || cache.Op != Delete || cache.Reason != "app no longer has app--cache." {
		t.Fatalf("cache: %+v", cache)
	}
	if hasCode(r.Problems, "catalog-dependency-migration-required") {
		t.Fatalf("turning an optional dependency off is a switch: %+v", r.Problems)
	}
}

func TestBlockedCompositionKeepsItsComponents(t *testing.T) {
	bindings := boundFrom(t, composedPlan(t, recipestest.Registry(t), sampleApp))
	r := composedPlan(t, recipestest.Registry(t), "version: 1\nresources:\n  app: {kind: sample, provider: server, target: conn_gone}\n", bindings...)
	if r.Executable {
		t.Fatal("a composition on a missing target was planned")
	}
	for _, a := range r.Actions {
		if a.Op == Delete {
			t.Fatalf("a composition that couldn't be planned deletes %s", a.Name)
		}
	}
}

func TestCompositionRefusesDependencySwitch(t *testing.T) {
	connected := "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {resource: db}}}\n  db: {kind: postgres, provider: server, target: " + targetA + "}\n"
	bindings := boundFrom(t, composedPlan(t, recipestest.Registry(t), connected))
	if i := slices.IndexFunc(bindings, func(b *Binding) bool { return b.Name == "app" }); i < 0 || !bytes.Contains(bindings[i].Config, []byte(`"postgres":{"mode":"resource","resource":"db"`)) {
		t.Fatalf("bound parent: %+v", bindings)
	}
	provisioned := "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + "}\n  db: {kind: postgres, provider: server, target: " + targetA + "}\n"
	r := composedPlan(t, recipestest.Registry(t), provisioned, bindings...)
	if r.Executable || !hasCode(r.Problems, "catalog-dependency-migration-required") {
		t.Fatalf("switch accepted: %+v", r.Problems)
	}
	if findAction(r, "resource:app--postgres") != nil {
		t.Fatal("a refused switch still provisions the new dependency")
	}
	if db := findAction(r, "resource:db"); db == nil || db.Op == Delete {
		t.Fatalf("the connected database isn't left in place: %+v", db)
	}

	// The reverse keeps the database it provisioned.
	bindings = boundFrom(t, composedPlan(t, recipestest.Registry(t), provisioned))
	r = composedPlan(t, recipestest.Registry(t), connected, bindings...)
	if r.Executable || !hasCode(r.Problems, "catalog-dependency-migration-required") {
		t.Fatalf("switch accepted: %+v", r.Problems)
	}
	if a := findAction(r, "resource:app--postgres"); a == nil || a.Op == Delete {
		t.Fatalf("the provisioned dependency isn't left in place: %+v", a)
	}
}

func TestRemovingCompositionDeletesComponentsAndKeepsConnected(t *testing.T) {
	both := "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + "}\n  db: {kind: postgres, provider: server, target: " + targetA + "}\n"
	bindings := boundFrom(t, composedPlan(t, recipestest.Registry(t), both))
	if len(bindings) != 6 {
		t.Fatalf("bindings: %d", len(bindings))
	}
	r := composedPlan(t, recipestest.Registry(t), "version: 1\nresources:\n  db: {kind: postgres, provider: server, target: "+targetA+"}\n", bindings...)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	for _, name := range []string{"app", "app--api", "app--migrate", "app--worker", "app--postgres"} {
		if a := findAction(r, "resource:"+name); a == nil || a.Op != Delete {
			t.Fatalf("%s not deleted: %+v", name, a)
		}
	}
	if db := findAction(r, "resource:db"); db == nil || db.Op != Keep {
		t.Fatalf("db: %+v", db)
	}
	// The composition's secrets go with it, after the last component.
	parent, api := findAction(r, "resource:app"), findAction(r, "resource:app--api")
	if parent.Step <= api.Step || findAction(r, "resource:app--postgres").Step <= api.Step {
		t.Fatalf("parent step %d, api step %d", parent.Step, api.Step)
	}
	if parent.Creates != "Sample catalog service" || api.Creates != "Sample api container" {
		t.Fatalf("creates: %q, %q", parent.Creates, api.Creates)
	}
}

func TestCompositionRejectsTakenComponentName(t *testing.T) {
	r := composedPlan(t, recipestest.Registry(t), sampleApp+"  app--api: {kind: postgres, provider: server, target: "+targetA+"}\n")
	if r.Executable || !slices.ContainsFunc(r.Problems, func(f Finding) bool {
		return f.Code == "catalog-component-name-taken" && f.Path == "resources.app--api"
	}) {
		t.Fatalf("taken name accepted: %+v", r.Problems)
	}
	if a := findAction(r, "resource:app--api"); a == nil || a.Resource.Component != nil {
		t.Fatalf("the component replaced the manifest's resource: %+v", a)
	}
}

func TestCompositionRecipeDriftNeedsReplan(t *testing.T) {
	bindings := boundFrom(t, composedPlan(t, recipestest.Registry(t), sampleApp))
	kind := recipestest.Sample()
	kind.Releases[0].Revision = "2"
	kinds := recipes.NewRegistry()
	if err := kinds.Register(kind); err != nil {
		t.Fatal(err)
	}
	r := composedPlan(t, kinds, sampleApp, bindings...)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	api := findAction(r, "resource:app--api")
	if api.Op != Update || !slices.Contains(api.Changes, Change{Field: "recipe revision", From: "1", To: "2"}) {
		t.Fatalf("api: %s %+v", api.Op, api.Changes)
	}
	if slices.ContainsFunc(api.Changes, func(c Change) bool { return c.Field == "component" }) {
		t.Fatalf("revision also reported as a raw component change: %+v", api.Changes)
	}
	if postgres := findAction(r, "resource:app--postgres"); postgres.Op != Keep {
		t.Fatalf("dependency: %s %+v", postgres.Op, postgres.Changes)
	}
}

const clusterApp = "version: 1\nresources:\n  app: {kind: sample, provider: kubernetes, target: " + targetA + "}\n"

// clusterWorkloadConnection is a checked cluster namespace with the
// service proxy, no storage class and no database recovery permissions.
func clusterWorkloadConnection(t *testing.T) ConnectionOption {
	t.Helper()
	return infrastructureConnection(t, targetA, "kubernetes", map[string]string{"endpoint": "https://cluster.example", "cluster_ca": "checked-ca", "namespace": "apps", "namespace_uid": "namespace-identity", "architecture": "arm64", "service_proxy": "ready"})
}

func clusterComposedPlan(t *testing.T, kinds *recipes.Registry, c ConnectionOption) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: &graph.Graph{}, Providers: providers.Registry(), Kinds: kinds,
		Manifest: parse(t, clusterApp), Connections: []ConnectionOption{c},
	})
}

// statelessSample is Sample without its job and dependencies: an API and
// a worker, neither with a volume.
func statelessSample(t *testing.T) recipes.Kind {
	t.Helper()
	kind := recipestest.Sample()
	kind.Dependencies = nil
	delete(kind.Releases[0].Images, "migrate")
	api, worker := kind.Components[1], kind.Components[2]
	api.After = nil
	api.Env = []recipes.EnvVar{{Name: "ADMIN_PASSWORD", Secret: "admin-password"}}
	kind.Components = []recipes.Component{api, worker}
	return kind
}

func registryOf(t *testing.T, kind recipes.Kind) *recipes.Registry {
	t.Helper()
	kinds := recipes.NewRegistry()
	if err := kinds.Register(kind); err != nil {
		t.Fatal(err)
	}
	return kinds
}

func TestCompositionOnKubernetesNeedsNamespaceNotStorage(t *testing.T) {
	r := clusterComposedPlan(t, registryOf(t, statelessSample(t)), clusterWorkloadConnection(t))
	if !r.Executable {
		t.Fatalf("stateless composition on a cluster blocked: %+v", r.Problems)
	}
	api := findAction(r, "resource:app--api")
	if api == nil || api.ConnectionID != targetA || api.Resource.TargetSettings["namespace_uid"] != "namespace-identity" || api.Resource.Component == nil || !strings.Contains(api.Resource.Component.Component.Image, "sample-api@sha256:01873eef") {
		t.Fatalf("api on the cluster: %+v", api)
	}
	for _, key := range []string{"architecture", "namespace", "namespace_uid"} {
		t.Run(key, func(t *testing.T) {
			c := clusterWorkloadConnection(t)
			delete(c.Details, key)
			if r := clusterComposedPlan(t, registryOf(t, statelessSample(t)), c); r.Executable {
				t.Fatalf("composition accepted an unchecked %s", key)
			}
		})
	}
	c := clusterWorkloadConnection(t)
	c.Details["namespace"] = "Not_A_Label"
	if r := clusterComposedPlan(t, registryOf(t, statelessSample(t)), c); r.Executable {
		t.Fatal("composition accepted an invalid namespace")
	}
}

func TestCompositionOnKubernetesWithVolumeNeedsRetainedStorage(t *testing.T) {
	kind := statelessSample(t)
	kind.Components[1].Volume = &recipes.Volume{MountPath: "/var/lib/worker", KubernetesSizeMB: 1024}
	r := clusterComposedPlan(t, registryOf(t, kind), clusterWorkloadConnection(t))
	if r.Executable || !hasCode(r.Problems, "catalog-storage-unavailable") {
		t.Fatalf("volume accepted without retained storage: %+v", r.Problems)
	}
	c := clusterCatalogConnection(t)
	delete(c.Details, "postgres_recovery_permissions")
	c.Details["service_proxy"] = "ready"
	if r := clusterComposedPlan(t, registryOf(t, kind), c); !r.Executable {
		t.Fatalf("volume on retained storage blocked: %+v", r.Problems)
	}
}

func TestCompositionOnKubernetesWithJobNeedsJobPermission(t *testing.T) {
	c := clusterCatalogConnection(t)
	c.Details["service_proxy"] = "ready"
	r := clusterComposedPlan(t, recipestest.Registry(t), c)
	if r.Executable || !hasCode(r.Problems, "catalog-jobs-unavailable") {
		t.Fatalf("job accepted without job permission: %+v", r.Problems)
	}
	c.Details["jobs"] = "ready"
	if r := clusterComposedPlan(t, recipestest.Registry(t), c); !r.Executable {
		t.Fatalf("job with job permission blocked: %+v", r.Problems)
	}
	if r := clusterComposedPlan(t, registryOf(t, statelessSample(t)), clusterWorkloadConnection(t)); hasCode(r.Problems, "catalog-jobs-unavailable") {
		t.Fatalf("job permission required without a job: %+v", r.Problems)
	}
}

func TestCompositionOnKubernetesNeedsAKindThatTargetsIt(t *testing.T) {
	kind := statelessSample(t)
	kind.Targets = []string{"server"}
	r := clusterComposedPlan(t, registryOf(t, kind), clusterWorkloadConnection(t))
	if r.Executable || !hasCode(r.Problems, "catalog-target-unsupported") {
		t.Fatalf("server-only kind accepted on a cluster: %+v", r.Problems)
	}
}

func TestCompositionDependencyKindMustBeKnown(t *testing.T) {
	kind := recipestest.Sample()
	kind.Dependencies[0].Kinds = []string{"cassandra"}
	kinds := recipes.NewRegistry()
	if err := kinds.Register(kind); err != nil {
		t.Fatal(err)
	}
	r := composedPlan(t, kinds, sampleApp)
	if r.Executable || !hasCode(r.Problems, "catalog-dependency-unavailable") {
		t.Fatalf("unknown dependency kind accepted: %+v", r.Problems)
	}
}

// engineFixtures are the catalog_test plans of the five engine kinds, on a
// server and, for postgres, on a cluster, each alone and with a service
// that needs it.
func engineFixtures(t *testing.T) map[string]*Result {
	t.Helper()
	out := map[string]*Result{}
	for _, kind := range []string{"postgres", "mysql", "mariadb", "redis", "valkey"} {
		connections := infrastructureConnections(t)
		connections[0].Details["architecture"] = "arm64"
		connections[0].Details["docker_id"] = "reviewed-daemon"
		alone := parse(t, "version: 1\nresources:\n  database: {kind: "+kind+", provider: server, target: "+targetA+"}\n")
		out[kind] = Compute(Input{Environment: "production", Graph: &graph.Graph{}, Manifest: alone, Providers: providers.Registry(), Connections: connections})
		needed := parse(t, "version: 1\nresources:\n  database: {kind: "+kind+", provider: server, target: "+targetA+"}\nservices:\n  web: {source: cmd/api, provider: server, target: "+targetA+", public: false, needs: [database]}\n")
		out[kind+"-needed"] = Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: needed, Providers: providers.Registry(), Connections: connections})
	}
	out["postgres-kubernetes"] = clusterCatalogPlan(t, clusterCatalogConnection(t))
	return out
}

// TestEnginePlansUnchanged holds the five engine kinds' plans to the ones
// captured before composed kinds were planned.
func TestEnginePlansUnchanged(t *testing.T) {
	plans := map[string][]Action{}
	for name, r := range engineFixtures(t) {
		plans[name] = r.Actions
	}
	got, err := json.MarshalIndent(plans, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/engine-plans.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Fatalf("engine plans changed:\n%s", got)
	}
}

// prodPG and otherPG are external endpoint IDs.
const (
	prodPG  = "endpoint_01m4vewksnea2bwdxwdy5aekhn"
	otherPG = "endpoint_01m4va49zwe9pvk24sm9h8v0ty"
)

const externalApp = "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n"

func externalPlan(t *testing.T, doc string, checks map[string]EndpointCheck) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: recipestest.Registry(t), Endpoints: checks,
	})
}

func findCode(fs []Finding, code string) *Finding {
	i := slices.IndexFunc(fs, func(f Finding) bool { return f.Code == code })
	if i < 0 {
		return nil
	}
	return &fs[i]
}

func TestExternalDependencyNeedsAPassingCheck(t *testing.T) {
	r := externalPlan(t, externalApp, map[string]EndpointCheck{prodPG: {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Version: "16.4"}}})
	if !r.Executable {
		t.Fatalf("passing endpoint blocked: %+v", r.Problems)
	}
	w := findCode(r.Warnings, "catalog-endpoint-unmanaged")
	if w == nil || w.Message != "Not managed by Loomplane: no backups or restores" || w.Name != "app" || w.Path != "resources.app.uses.postgres.external" {
		t.Fatalf("unmanaged warning: %+v", r.Warnings)
	}
	if findAction(r, "resource:app--postgres") != nil {
		t.Fatal("an external dependency was provisioned too")
	}
	parent := findAction(r, "resource:app")
	if parent == nil || parent.Resource.Composition.Dependencies["postgres"] != (DependencyChoice{Mode: DependencyExternal, External: prodPG}) {
		t.Fatalf("parent: %+v", parent)
	}
	if migrate := findAction(r, "resource:app--migrate"); migrate == nil || len(migrate.DependsOn) != 0 || migrate.Step != 2 {
		t.Fatalf("migrate waits for an external endpoint: %+v", migrate)
	}

	// A plan made without checking the endpoint can't use it.
	r = externalPlan(t, externalApp, nil)
	if r.Executable || findCode(r.Problems, "catalog-endpoint-unavailable") == nil {
		t.Fatalf("unchecked endpoint accepted: %+v", r.Problems)
	}
}

func TestExternalDependencyMissingExtension(t *testing.T) {
	r := externalPlan(t, externalApp, map[string]EndpointCheck{prodPG: {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Version: "16.4", Missing: []string{"timescaledb"}}}})
	p := findCode(r.Problems, "catalog-endpoint-incompatible")
	if r.Executable || p == nil || p.Message != "endpoint prod-pg lacks the timescaledb extension" || p.Name != "app" || p.Path != "resources.app.uses.postgres.external" {
		t.Fatalf("missing extension accepted: %+v", r.Problems)
	}

	// An endpoint that answered and is too old, or of another engine, is
	// incompatible too.
	r = externalPlan(t, externalApp, map[string]EndpointCheck{prodPG: {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Version: "15.2", Problems: []string{"PostgreSQL 15.2 is older than 16"}}}})
	if p = findCode(r.Problems, "catalog-endpoint-incompatible"); r.Executable || p == nil || p.Message != "endpoint prod-pg can't be used: PostgreSQL 15.2 is older than 16" {
		t.Fatalf("old endpoint accepted: %+v", r.Problems)
	}
	r = externalPlan(t, externalApp, map[string]EndpointCheck{prodPG: {ID: prodPG, Name: "prod-pg", Found: true, Engine: "redis", Report: &catalogconnect.Report{Version: "8.2.1"}}})
	if p = findCode(r.Problems, "catalog-endpoint-incompatible"); r.Executable || p == nil || !strings.HasPrefix(p.Message, "endpoint prod-pg runs redis") {
		t.Fatalf("redis endpoint accepted for postgres: %+v", r.Problems)
	}
}

func TestExternalDependencyUnreachableOrUnknown(t *testing.T) {
	for name, check := range map[string]EndpointCheck{
		"unknown":     {ID: prodPG},
		"error":       {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Err: "the SSH server or Docker identity changed"},
		"unreachable": {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Problems: []string{"db.example:5432 isn't reachable from the target: connection refused"}}},
	} {
		t.Run(name, func(t *testing.T) {
			r := externalPlan(t, externalApp, map[string]EndpointCheck{prodPG: check})
			p := findCode(r.Problems, "catalog-endpoint-unavailable")
			label := "endpoint prod-pg"
			if !check.Found {
				label = "endpoint " + prodPG
			}
			if r.Executable || p == nil || p.Name != "app" || p.Path != "resources.app.uses.postgres.external" || !strings.Contains(p.Message, label) {
				t.Fatalf("endpoint accepted: %+v", r.Problems)
			}
			for _, want := range []string{check.Err, firstOf(check.Report)} {
				if !strings.Contains(p.Message, want) {
					t.Fatalf("message %q doesn't say %q", p.Message, want)
				}
			}
		})
	}
}

func firstOf(r *catalogconnect.Report) string {
	if r == nil || len(r.Problems) == 0 {
		return ""
	}
	return r.Problems[0]
}

func TestExternalCheckThatFoundNothingIsUnavailable(t *testing.T) {
	r := externalPlan(t, externalApp, map[string]EndpointCheck{prodPG: {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{}}})
	p := findCode(r.Problems, "catalog-endpoint-unavailable")
	if r.Executable || p == nil || p.Message != "endpoint prod-pg didn't say which version it runs, so app can't use it. Plan again." {
		t.Fatalf("an empty report passed: %+v", r.Problems)
	}

	// The stricter rules arrive as problems and block the plan.
	r = externalPlan(t, externalApp, map[string]EndpointCheck{prodPG: {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Version: "16.4", Problems: []string{"timescaledb isn't in shared_preload_libraries"}}}})
	if p = findCode(r.Problems, "catalog-endpoint-incompatible"); r.Executable || p == nil || p.Message != "endpoint prod-pg can't be used: timescaledb isn't in shared_preload_libraries" {
		t.Fatalf("missing preload accepted: %+v", r.Problems)
	}
}

func TestEndpointKeySeparatesWhatTheCheckDependsOn(t *testing.T) {
	sample, other := catalogconnect.Consumer{Title: "Sample"}, catalogconnect.Consumer{Title: "Other", Role: "other_app"}
	base := recipes.Requirement{Engine: "postgres", MinVersion: "16", Extensions: []string{"vector", "postgis"}}
	// Keys made before the stricter rules don't change, and the consumer
	// only counts where a problem names it.
	if got := endpointKey(prodPG, targetA, base, sample); got != prodPG+"@"+targetA+"/postgres/16/postgis,vector" || got != endpointKey(prodPG, targetA, base, other) {
		t.Fatalf("base key %q", got)
	}
	keys := map[string]bool{endpointKey(prodPG, targetA, base, sample): true}
	for _, req := range []recipes.Requirement{
		{Engine: "postgres", MinVersion: "16", Extensions: []string{"vector", "postgis"}, Preload: []string{"timescaledb"}},
		{Engine: "postgres", MinVersion: "16", Extensions: []string{"vector", "postgis"}, CreateRole: true},
		{Engine: "postgres", MinVersion: "16", Extensions: []string{"vector", "postgis"}, TLS: []string{"verify-full"}},
	} {
		for _, c := range []catalogconnect.Consumer{sample, other} {
			keys[endpointKey(prodPG, targetA, req, c)] = true
		}
	}
	// The base, one for the preload rule, and one per consumer for each
	// rule whose problem names the consumer.
	if len(keys) != 6 {
		t.Fatalf("keys: %v", keys)
	}
}

func TestExternalRefusedWhenDependencyTakesNone(t *testing.T) {
	doc := "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + ", uses: {cache: {external: " + otherPG + "}}}\n"
	r := externalPlan(t, doc, map[string]EndpointCheck{otherPG: {ID: otherPG, Found: true, Engine: "redis", Report: &catalogconnect.Report{Version: "8.2.1"}}})
	p := findCode(r.Problems, "catalog-connect-external-unaccepted")
	if r.Executable || p == nil || p.Name != "app" || p.Path != "resources.app.uses.cache.external" {
		t.Fatalf("external cache accepted: %+v", r.Problems)
	}
	if findCode(r.Warnings, "catalog-endpoint-unmanaged") != nil {
		t.Fatalf("a refused endpoint is labelled as connected: %+v", r.Warnings)
	}
}

func TestExternalChecksLandOnTheirTarget(t *testing.T) {
	doc := "version: 1\nresources:\n" +
		"  near: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n" +
		"  far: {kind: sample, provider: server, target: " + targetB + ", uses: {postgres: {external: " + prodPG + "}}}\n"
	req, consumer := *recipestest.Sample().Dependencies[0].External, catalogconnect.ConsumerOf(recipestest.Sample(), "postgres")
	r := externalPlan(t, doc, map[string]EndpointCheck{
		endpointKey(prodPG, targetA, req, consumer): {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Version: "16.4"}},
		endpointKey(prodPG, targetB, req, consumer): {ID: prodPG, Name: "prod-pg", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Problems: []string{"db.example:5432 isn't reachable from the target: i/o timeout"}}},
	})
	if len(r.Problems) != 1 || r.Problems[0].Code != "catalog-endpoint-unavailable" || r.Problems[0].Name != "far" {
		t.Fatalf("problems: %+v", r.Problems)
	}
}

func TestCompositionWithControlPortNeedsServiceProxy(t *testing.T) {
	c := clusterWorkloadConnection(t)
	delete(c.Details, "service_proxy")
	r := clusterComposedPlan(t, registryOf(t, statelessSample(t)), c)
	if r.Executable || !hasCode(r.Problems, "catalog-channel-unavailable") {
		t.Fatalf("control port accepted without the service proxy: %+v", r.Problems)
	}
	i := slices.IndexFunc(r.Problems, func(f Finding) bool { return f.Code == "catalog-channel-unavailable" })
	if f := r.Problems[i]; f.Subject != ResourceSubject || f.Name != "app" || f.Path != "resources.app.target" || !strings.Contains(f.Message, "admin") {
		t.Fatalf("finding: %+v", f)
	}
	c.Details["service_proxy"] = "ready"
	if r := clusterComposedPlan(t, registryOf(t, statelessSample(t)), c); !r.Executable {
		t.Fatalf("control port with the service proxy blocked: %+v", r.Problems)
	}
	// Without a control port, nothing needs the proxy.
	kind := statelessSample(t)
	kind.Components[0].Ports = kind.Components[0].Ports[:1]
	kind.Outputs = kind.Outputs[:1]
	delete(c.Details, "service_proxy")
	if r := clusterComposedPlan(t, registryOf(t, kind), c); hasCode(r.Problems, "catalog-channel-unavailable") {
		t.Fatalf("service proxy required without a control port: %+v", r.Problems)
	}
}

const edgeApp = "version: 1\nresources:\n  edge: {kind: sample, provider: server, target: " + targetA + "}\n"

func edgePlacement() CloudPlacement {
	return CloudPlacement{CloudID: "cloud_1", CloudRevision: 1, AssignmentRevision: 1, TargetID: "t1", TargetRevision: 1, TargetName: "edge-host", Provider: "server", ConnectionID: targetA, RegionSource: "default"}
}

func TestComposedComponentsCarryTheCloudPlacement(t *testing.T) {
	r := Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, edgeApp), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: recipestest.Registry(t), CloudPlacements: map[string]CloudPlacement{"edge": edgePlacement()},
	})
	if !r.Executable {
		t.Fatalf("composed plan blocked: %+v", r.Problems)
	}
	if hasCode(r.Warnings, "catalog-cloud-placement-missing") {
		t.Fatalf("placed resource warned: %+v", r.Warnings)
	}
	for _, id := range []string{"resource:edge", "resource:edge--migrate", "resource:edge--api", "resource:edge--worker"} {
		a := findAction(r, id)
		if a == nil || a.Resource == nil || a.Resource.CloudPlacement == nil || a.Resource.CloudPlacement.CloudID != "cloud_1" || a.Resource.CloudPlacement.TargetID != "t1" || a.Resource.CloudPlacement.ConnectionID != targetA {
			t.Fatalf("%s placement: %+v", id, a)
		}
		data, err := json.Marshal(a.Resource)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"cloud_placement":{`)) || !bytes.Contains(data, []byte(`"cloud_id":"cloud_1"`)) {
			t.Fatalf("%s binding config: %s", id, data)
		}
	}
	// The dependency it provisions is an ordinary database.
	if a := findAction(r, "resource:edge--postgres"); a == nil || a.Resource.CloudPlacement != nil {
		t.Fatalf("provisioned dependency placed: %+v", a)
	}

	// Without a placement the key is absent, and the plan says why.
	r = Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, edgeApp), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: recipestest.Registry(t),
	})
	for _, a := range r.Actions {
		data, err := json.Marshal(a.Resource)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("cloud_placement")) {
			t.Fatalf("%s placed without a Cloud: %s", a.ID, data)
		}
	}
	w := findCode(r.Warnings, "catalog-cloud-placement-missing")
	if w == nil || w.Subject != ResourceSubject || w.Name != "edge" || w.Path != "resources.edge.target" || w.Message != "edge isn't on a target of this environment's Cloud, so it can't take domain routes." {
		t.Fatalf("missing placement warning: %+v", r.Warnings)
	}
}

func TestEnginesNeverGetACloudPlacement(t *testing.T) {
	connections := infrastructureConnections(t)
	connections[0].Details["architecture"] = "arm64"
	connections[0].Details["docker_id"] = "reviewed-daemon"
	doc := "version: 1\nresources:\n  database: {kind: postgres, provider: server, target: " + targetA + "}\n"
	r := Compute(Input{
		Environment: "production", Graph: &graph.Graph{}, Manifest: parse(t, doc), Providers: providers.Registry(), Connections: connections,
		CloudPlacements: map[string]CloudPlacement{"database": edgePlacement()},
	})
	for _, a := range r.Actions {
		if a.Resource != nil && a.Resource.CloudPlacement != nil {
			t.Fatalf("%s placed: %+v", a.ID, a.Resource.CloudPlacement)
		}
	}
	if hasCode(r.Warnings, "catalog-cloud-placement-missing") {
		t.Fatalf("engine warned about its placement: %+v", r.Warnings)
	}
	got, err := json.Marshal(r.Actions)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/engine-plans.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, golden["postgres"]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("engine plan changed:\n%s", got)
	}
}

// A composed resource deployed before its environment's Cloud was assigned
// adopts its placement without a redeploy: the resource and each component
// keep running and record it. Its provisioned database doesn't.
func TestDeployedCompositionAdoptsItsCloudPlacement(t *testing.T) {
	plan := func(bindings []*Binding) *Result {
		return Compute(Input{
			Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, edgeApp), Providers: providers.Registry(),
			Connections: composedConnections(t), Kinds: recipestest.Registry(t), Bindings: bindings,
			CloudPlacements: map[string]CloudPlacement{"edge": edgePlacement()},
		})
	}
	unplaced := composedPlan(t, recipestest.Registry(t), edgeApp)
	r := plan(boundFrom(t, unplaced))
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	composed := []string{"resource:edge", "resource:edge--migrate", "resource:edge--api", "resource:edge--worker"}
	for _, id := range composed {
		a := findAction(r, id)
		if a == nil || a.Op != Keep || !a.AdoptCloudPlacement || a.CloudAdoption == nil || a.CloudAdoption.BindingID != "binding-"+a.Name {
			t.Fatalf("%s doesn't adopt its placement: %+v", id, a)
		}
	}
	if a := findAction(r, "resource:edge--postgres"); a == nil || a.Op != Keep || a.AdoptCloudPlacement || a.CloudAdoption != nil {
		t.Fatalf("provisioned database adopts a placement: %+v", a)
	}

	// Once recorded, the same placement is a plain Keep.
	r = plan(boundFrom(t, r))
	for _, id := range append(composed, "resource:edge--postgres") {
		if a := findAction(r, id); a == nil || a.Op != Keep || a.AdoptCloudPlacement || a.CloudAdoption != nil {
			t.Fatalf("%s isn't a plain Keep: %+v", id, a)
		}
	}
}

// graphApp is a kind that may use a dependency-only graph database: an
// API, a job that rebuilds the graph and runs only with it, and a worker
// that starts after the job.
func graphApp() recipes.Kind {
	image := recipes.Image{Repository: "registry.example/graph-app", Digests: map[string]string{
		"amd64": "sha256:cb2ffce7d6a5fca5f8615339cc171c74dfe47017c634894dd43e33e10cb4cbba",
		"arm64": "sha256:01873eef9d70a2cf2ee33aab65b3496fcfc9674a5707b42b12fe19e7ae0c5ce7",
	}}
	graphURL := recipes.EnvVar{Name: "GRAPH_URL", From: &recipes.Ref{Dependency: "falkordb", Field: recipes.FieldURL}, Requires: "falkordb"}
	return recipes.Kind{
		Name:      "graphapp",
		Title:     "Graph app",
		Placement: recipes.PlacementAppProject,
		Targets:   []string{"server", "kubernetes"},
		Releases: []recipes.Release{{
			Version: "1", Product: "1.0.0", Revision: "1",
			Images: map[string]recipes.Image{"api": image, "rebuild-graph": image, "worker": image},
		}},
		Dependencies: []recipes.Dependency{{Name: "falkordb", Title: "FalkorDB", Kinds: []string{"graph"}, Optional: true}},
		Components: []recipes.Component{
			{
				Name:      "api",
				Role:      recipes.RoleProcess,
				Ports:     []recipes.Port{{Name: "http", Container: 8080, Protocol: "http", Routable: true}},
				Env:       []recipes.EnvVar{graphURL},
				Readiness: &recipes.HTTPProbe{Port: "http", Path: "/readyz"},
			},
			{
				Name:     "rebuild-graph",
				Role:     recipes.RoleJob,
				Env:      []recipes.EnvVar{{Name: "GRAPH_URL", From: &recipes.Ref{Dependency: "falkordb", Field: recipes.FieldURL}}},
				After:    []string{"api"},
				Requires: "falkordb",
			},
			{
				Name:      "worker",
				Role:      recipes.RoleProcess,
				Ports:     []recipes.Port{{Name: "health", Container: 8081, Protocol: "http"}},
				Readiness: &recipes.HTTPProbe{Port: "health", Path: "/healthz"},
				After:     []string{"rebuild-graph"},
			},
		},
		Outputs: []recipes.Output{{Name: "url", EnvKey: "GRAPH_APP_URL", From: &recipes.Ref{Component: "api", Field: "url:http"}}},
	}
}

// graphKinds is a registry with the graph database and graphApp.
func graphKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	kinds := recipes.NewRegistry()
	for _, kind := range []recipes.Kind{recipestest.Graph(), graphApp()} {
		if err := kinds.Register(kind); err != nil {
			t.Fatal(err)
		}
	}
	return kinds
}

const (
	graphOff = "version: 1\nresources:\n  app: {kind: graphapp, provider: server, target: " + targetA + "}\n"
	graphOn  = "version: 1\nresources:\n  app: {kind: graphapp, provider: server, target: " + targetA + ", uses: {falkordb: {enabled: true}}}\n"
)

func TestDependencyOnlyKindPlansAsOneComponentAction(t *testing.T) {
	r := composedPlan(t, graphKinds(t), graphOn)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	a := findAction(r, "resource:app--falkordb")
	if a == nil || a.Step != 1 || a.Op != Create || a.ConnectionID != targetA || a.Creates != "Graph server container" || a.Resource.Composition != nil || a.Resource.Catalog != nil || a.Resource.CloudPlacement != nil {
		t.Fatalf("falkordb: %+v", a)
	}
	c := a.Resource.Component
	if c == nil || c.Kind != "graph" || c.Resource != "app--falkordb" || c.Protection != recipes.ProtectionRebuildable || c.Component.Name != "server" || c.Revision != "1" || !strings.Contains(c.Component.Image, "graph@sha256:4a2d7b2e") {
		t.Fatalf("falkordb component: %+v", c)
	}
	for _, other := range r.Actions {
		if strings.HasPrefix(other.Name, "app--falkordb-") || (other.Resource != nil && other.Resource.Composition != nil && other.Resource.Composition.Kind == "graph") {
			t.Fatalf("a nested action for the dependency: %+v", other)
		}
	}
	if got := findAction(r, "resource:app").Resource.Composition.Dependencies["falkordb"]; got != (DependencyChoice{Mode: DependencyNew, Resource: "app--falkordb", Kind: "graph"}) {
		t.Fatalf("choice: %+v", got)
	}
	if api := findAction(r, "resource:app--api"); api == nil || !slices.Equal(api.DependsOn, []string{"resource:app--falkordb"}) || api.Step != 2 {
		t.Fatalf("api: %+v", api)
	}

	// On a cluster its volume needs retained storage, like any component's.
	cluster := func(c ConnectionOption) *Result {
		return Compute(Input{
			Environment: "production", Graph: &graph.Graph{}, Providers: providers.Registry(), Kinds: graphKinds(t), Connections: []ConnectionOption{c},
			Manifest: parse(t, "version: 1\nresources:\n  app: {kind: graphapp, provider: kubernetes, target: "+targetA+", uses: {falkordb: {enabled: true}}}\n"),
		})
	}
	conn := clusterCatalogConnection(t)
	conn.Details["jobs"] = "ready"
	if r := cluster(conn); !r.Executable || findAction(r, "resource:app--falkordb").Resource.TargetSettings["storage_class_uid"] != "class-identity" {
		t.Fatalf("falkordb on a cluster: %+v", r.Problems)
	}
	conn = clusterWorkloadConnection(t)
	conn.Details["jobs"] = "ready"
	if r := cluster(conn); r.Executable || !hasCode(r.Problems, "catalog-storage-unavailable") || findAction(r, "resource:app--falkordb") != nil {
		t.Fatalf("falkordb on a cluster without retained storage: %+v", r.Problems)
	}
}

func TestRequiredComponentFollowsItsDependency(t *testing.T) {
	steps := func(r *Result) map[string]int {
		out := map[string]int{}
		for _, a := range r.Actions {
			out[a.Name] = a.Step
		}
		return out
	}
	r := composedPlan(t, graphKinds(t), graphOn)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	if got, want := steps(r), map[string]int{"app--falkordb": 1, "app--api": 2, "app--rebuild-graph": 3, "app--worker": 4, "app": 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steps with the dependency: %v", got)
	}

	r = composedPlan(t, graphKinds(t), graphOff)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	if got, want := steps(r), map[string]int{"app--api": 2, "app--worker": 2, "app": 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steps without the dependency: %v", got)
	}
	if worker := findAction(r, "resource:app--worker"); len(worker.DependsOn) != 0 || len(worker.Resource.Component.Component.After) != 0 {
		t.Fatalf("worker still waits for the job: %+v", worker)
	}
	if parent := findAction(r, "resource:app"); !slices.Equal(parent.DependsOn, []string{"resource:app--api", "resource:app--worker"}) {
		t.Fatalf("parent: %+v", parent.DependsOn)
	}
}

func TestDependencyOnlyKindRefusedStandalone(t *testing.T) {
	r := composedPlan(t, graphKinds(t), "version: 1\nresources:\n  g: {kind: graph, provider: server, target: "+targetA+"}\n")
	p := findCode(r.Problems, "catalog-kind-dependency-only")
	if r.Executable || p == nil || p.Subject != ResourceSubject || p.Name != "g" || p.Path != "resources.g.kind" || p.Message != "graph is only added as part of another catalog service" {
		t.Fatalf("standalone dependency-only kind accepted: %+v", r.Problems)
	}
	if len(r.Actions) != 0 {
		t.Fatalf("actions: %+v", r.Actions)
	}
}

func TestOptionalRebuildableDependencyMayTurnOnAndOff(t *testing.T) {
	off := boundFrom(t, composedPlan(t, graphKinds(t), graphOff))
	r := composedPlan(t, graphKinds(t), graphOn, off...)
	if !r.Executable || hasCode(r.Problems, "catalog-dependency-migration-required") {
		t.Fatalf("turning it on refused: %+v", r.Problems)
	}
	if a := findAction(r, "resource:app--falkordb"); a == nil || a.Op != Create {
		t.Fatalf("falkordb: %+v", a)
	}

	on := boundFrom(t, r)
	r = composedPlan(t, graphKinds(t), graphOff, on...)
	if !r.Executable || hasCode(r.Problems, "catalog-dependency-migration-required") {
		t.Fatalf("turning it off refused: %+v", r.Problems)
	}
	for _, name := range []string{"app--falkordb", "app--rebuild-graph"} {
		if a := findAction(r, "resource:"+name); a == nil || a.Op != Delete || a.Reason != "app no longer has "+name+"." {
			t.Fatalf("%s: %+v", name, a)
		}
	}
	if findAction(r, "resource:app--falkordb").Step <= findAction(r, "resource:app--rebuild-graph").Step {
		t.Fatal("the dependency goes before the component that used it")
	}
}

// tenanted is statelessSample with a primary output issued to each
// consuming service for the tenant it chooses.
func tenanted(t *testing.T) *recipes.Registry {
	t.Helper()
	kind := statelessSample(t)
	kind.Outputs = []recipes.Output{{Name: "url", EnvKey: "SAMPLE_URL", PerConsumer: true}}
	kind.ConsumerOptions = []recipes.ConsumerOption{{Name: "tenant", Title: "Tenant", Pattern: "[a-z][a-z0-9-]*", Required: true}}
	return registryOf(t, kind)
}

func tenantDoc(options string) string {
	need := ""
	if options != "" {
		need = ", need_options: {app: {" + options + "}}"
	}
	return sampleApp + "services:\n  web: {source: cmd/api, provider: server, target: " + targetA + ", public: false, needs: [app]" + need + "}\n"
}

func TestConsumerOptionsLandOnTheLink(t *testing.T) {
	r := composedPlan(t, tenanted(t), tenantDoc("tenant: acme"))
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	want := []EnvLink{{Key: "SAMPLE_URL", From: "app", Subject: ResourceSubject, Field: "consumer:url", Options: map[string]string{"tenant": "acme"}}}
	if web := findAction(r, "service:web"); !reflect.DeepEqual(web.Service.Env, want) {
		t.Fatalf("env: %+v", web.Service.Env)
	}

	// Only a consumer link carries them.
	r = composedPlan(t, recipestest.Registry(t), sampleApp+"services:\n  web: {source: cmd/api, provider: server, target: "+targetA+", public: false, needs: [app]}\n")
	data, err := json.Marshal(findAction(r, "service:web").Service)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"options"`)) {
		t.Fatalf("options on a link that isn't a consumer's: %s", data)
	}
}

func TestConsumerOptionProblems(t *testing.T) {
	for _, tc := range []struct{ name, options, code, path, message string }{
		{"missing", "", "catalog-consumer-option-missing", "services.web.need_options.app.tenant", "web needs app without a tenant. Set need_options.app.tenant."},
		{"invalid", "tenant: Acme!", "catalog-consumer-option-invalid", "services.web.need_options.app.tenant", ""},
		{"unknown", "tenant: acme, region: eu", "catalog-consumer-option-unknown", "services.web.need_options.app.region", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := composedPlan(t, tenanted(t), tenantDoc(tc.options))
			p := findCode(r.Problems, tc.code)
			if r.Executable || p == nil || p.Subject != ServiceSubject || p.Name != "web" || p.Path != tc.path || (tc.message != "" && p.Message != tc.message) {
				t.Fatalf("want %s: %+v", tc.code, r.Problems)
			}
		})
	}
}

// boundAll is what a deployment of every action of r would have bound.
func boundAll(t *testing.T, r *Result) []*Binding {
	t.Helper()
	out := boundFrom(t, r)
	for _, a := range r.Actions {
		if a.Service == nil {
			continue
		}
		raw, err := json.Marshal(a.Service)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &Binding{ID: "binding-" + a.Name, Subject: a.Subject, Name: a.Name, Provider: a.Provider, ConnectionID: a.ConnectionID, ExternalID: "instance-" + a.Name, Engine: a.Engine, Config: raw})
	}
	return out
}

func TestChangingAConsumerOptionUpdatesTheService(t *testing.T) {
	bindings := boundAll(t, composedPlan(t, tenanted(t), tenantDoc("tenant: acme")))
	if r := composedPlan(t, tenanted(t), tenantDoc("tenant: acme"), bindings...); findAction(r, "service:web").Op != Keep {
		t.Fatalf("unchanged service: %+v", findAction(r, "service:web"))
	}
	r := composedPlan(t, tenanted(t), tenantDoc("tenant: beta"), bindings...)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	web := findAction(r, "service:web")
	if web.Op != Update || !reflect.DeepEqual(web.Changes, []Change{{Field: "app tenant", From: "acme", To: "beta"}}) {
		t.Fatalf("web: %s %+v", web.Op, web.Changes)
	}
}

func TestRedisDependencyRunsOnKubernetes(t *testing.T) {
	doc := "version: 1\nresources:\n  app: {kind: sample, provider: kubernetes, target: " + targetA + ", uses: {cache: {enabled: true}}}\n"
	plan := func(c ConnectionOption, doc string) *Result {
		return Compute(Input{
			Environment: "production", Graph: &graph.Graph{}, Providers: providers.Registry(), Kinds: recipestest.Registry(t),
			Manifest: parse(t, doc), Connections: []ConnectionOption{c},
		})
	}
	c := clusterCatalogConnection(t)
	c.Details["service_proxy"], c.Details["jobs"] = "ready", "ready"
	r := plan(c, doc)
	if !r.Executable {
		t.Fatalf("cache dependency on a cluster blocked: %+v", r.Problems)
	}
	cache := findAction(r, "resource:app--cache")
	want, err := recipes.KubernetesCache("redis", "8.2", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if cache == nil || cache.Step != 1 || cache.ConnectionID != targetA || cache.Resource.Kind != "redis" || !reflect.DeepEqual(cache.Resource.Catalog, want) || cache.Resource.TargetSettings["namespace_uid"] != "namespace-identity" {
		t.Fatalf("cache: %+v", cache)
	}

	delete(c.Details, "postgres_recovery_permissions")
	r = plan(c, doc)
	i := slices.IndexFunc(r.Problems, func(f Finding) bool { return f.Name == "app--cache" })
	if r.Executable || i < 0 || r.Problems[i].Code != "catalog-recovery-unavailable" || r.Problems[i].Message != "Check pod exec, network-policy and temporary-volume cleanup permissions before deploying this cache." {
		t.Fatalf("cache without recovery permissions: %+v", r.Problems)
	}

	// A standalone cache still isn't offered on a cluster (ruling R13).
	c = clusterCatalogConnection(t)
	r = plan(c, "version: 1\nresources:\n  cache: {kind: redis, provider: kubernetes, target: "+targetA+"}\n")
	if r.Executable || !hasCode(r.Problems, "provider-cant-provision") {
		t.Fatalf("standalone cache on a cluster: %+v", r.Problems)
	}
}

func TestCompositionWithKubernetesRulesNeedsRBAC(t *testing.T) {
	kind := statelessSample(t)
	kind.Components[0].KubernetesRules = []recipes.KubernetesRule{{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list", "watch"}}}
	c := clusterWorkloadConnection(t)
	r := clusterComposedPlan(t, registryOf(t, kind), c)
	p := findCode(r.Problems, "catalog-rbac-unavailable")
	const message = "Sample needs a Role in namespace apps to discover Services. Let Loomplane create serviceaccounts, roles and rolebindings there, and get, list and watch services, endpoints and endpointslices, then check the connection again."
	if r.Executable || p == nil || p.Subject != ResourceSubject || p.Name != "app" || p.Path != "resources.app" || p.Message != message {
		t.Fatalf("rules accepted without RBAC: %+v", r.Problems)
	}
	c.Details["rbac"] = "ready"
	if r := clusterComposedPlan(t, registryOf(t, kind), c); !r.Executable {
		t.Fatalf("rules with RBAC blocked: %+v", r.Problems)
	}
	if r := clusterComposedPlan(t, registryOf(t, statelessSample(t)), clusterWorkloadConnection(t)); hasCode(r.Problems, "catalog-rbac-unavailable") {
		t.Fatalf("RBAC required without rules: %+v", r.Problems)
	}
}

// front is a composed kind that connects a sample resource as its backend,
// the way one composed catalog service uses another.
func front() recipes.Kind {
	image := recipes.Image{Repository: "registry.example/front", Digests: map[string]string{
		"amd64": "sha256:5b3e8c3f2d1a0b9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c",
		"arm64": "sha256:6c4f9d4a3e2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b7c6d",
	}}
	return recipes.Kind{
		Name:      "front",
		Title:     "Front",
		Placement: recipes.PlacementAppProject,
		Targets:   []string{"server", "kubernetes"},
		Releases: []recipes.Release{{
			Version: "1", Product: "1.0.0", Revision: "1",
			Images: map[string]recipes.Image{"web": image},
		}},
		Dependencies: []recipes.Dependency{{Name: "backend", Title: "Sample", Kinds: []string{"sample"}}},
		Components: []recipes.Component{{
			Name:      "web",
			Role:      recipes.RoleProcess,
			Ports:     []recipes.Port{{Name: "http", Container: 8080, Protocol: "http", Routable: true}},
			Env:       []recipes.EnvVar{{Name: "BACKEND_URL", From: &recipes.Ref{Dependency: "backend", Field: recipes.FieldURL}}},
			Readiness: &recipes.HTTPProbe{Port: "http", Path: "/readyz"},
		}},
		Outputs: []recipes.Output{{Name: "url", EnvKey: "FRONT_URL", From: &recipes.Ref{Component: "web", Field: "url:http"}}},
	}
}

func TestComposedDependencyStartsAfterTheCompositionItConnects(t *testing.T) {
	kinds := recipestest.Registry(t)
	if err := kinds.Register(front()); err != nil {
		t.Fatal(err)
	}
	doc := sampleApp + "  site: {kind: front, provider: server, target: " + targetA + ", uses: {backend: {resource: app}}}\n" +
		"services:\n  web: {source: cmd/api, provider: server, target: " + targetA + ", public: false, needs: [site]}\n"
	r := composedPlan(t, kinds, doc)
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	steps := map[string]int{}
	for _, a := range r.Actions {
		steps[a.Name] = a.Step
	}
	want := map[string]int{"app--postgres": 1, "app--migrate": 2, "app--api": 3, "app--worker": 4, "app": 5, "site--web": 6, "site": 7, "web": 8}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps: %v, want %v", steps, want)
	}
	if web := findAction(r, "resource:site--web"); !slices.Equal(web.DependsOn, []string{"resource:app"}) {
		t.Fatalf("site--web depends on %v", web.DependsOn)
	}
}

func TestServiceVariableComesFromThePlansOwnEngines(t *testing.T) {
	kinds := recipes.NewRegistry()
	err := kinds.RegisterEngine(recipes.Engine{
		Kind: "timescaledb", Family: "postgres", Title: "PostgreSQL 16 with TimescaleDB", EnvKey: "DATABASE_URL",
		Targets: []string{"server"},
		Releases: []recipes.EngineRelease{{
			Version: "16", Product: "16.4", Revision: "1",
			Image: recipes.Image{Repository: "registry.example/timescaledb", Digests: map[string]string{
				"amd64": "sha256:11538f20d0d489fad2dc7d30eeaafd731bd8405e25b1696d86571282ab6cdf25",
				"arm64": "sha256:61581a49b789716d4da7a45ce7fd3c944b60150492ccece97b11b0b497f49aa8",
			}},
			MountPath: "/var/lib/postgresql/data", BackupFormat: "pg-dump-custom-v1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := "version: 1\nresources:\n  tsdb: {kind: timescaledb, provider: server, target: " + targetA + "}\n" +
		"services:\n  web: {source: cmd/api, provider: server, target: " + targetA + ", public: false, needs: [tsdb]}\n"
	// The server offers it the way the catalog offers a registered engine.
	all := providers.Catalog()
	for i := range all {
		if all[i].Name == "server" {
			all[i].Provisions = append(all[i].Provisions, provider.Provisions{Kind: "timescaledb", Creates: "PostgreSQL 16 with TimescaleDB container", Versions: []string{"16"}})
		}
	}
	offered, err := provider.NewRegistry(all...)
	if err != nil {
		t.Fatal(err)
	}
	r := Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: offered,
		Connections: composedConnections(t), Kinds: kinds,
	})
	web := findAction(r, "service:web")
	if web == nil || web.Service == nil {
		t.Fatalf("web: %+v %+v", web, r.Problems)
	}
	if want := []EnvLink{{Key: "DATABASE_URL", From: "tsdb", Subject: ResourceSubject}}; !reflect.DeepEqual(web.Service.Env, want) {
		t.Fatalf("env: %+v %+v", web.Service.Env, r.Problems)
	}
}
