package planning

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Each environment of a resource runs its own copy, on the target its own
// block names. These tests pin every planner read of a resource's target,
// GPU and settings to the environment's view of it, so production's copy
// never lands on staging's target.

// twoCopies runs llm in staging on targetA, the top-level target, and in
// production on targetB, its own.
const twoCopies = "version: 1\nresources:\n  llm:\n    kind: optin\n    provider: server\n    target: " + targetA + "\n" +
	"    environments:\n      staging: {enabled: true}\n      production: {enabled: true, target: " + targetB + "}\n"

var optInComponents = []string{"resource:llm--api", "resource:llm--migrate", "resource:llm--worker"}

// copyKinds is recipestest.Registry with the opt-in kind taking an
// optional model and a size that defaults to 1, and advising how to run
// it in another environment the way the model kinds do.
func copyKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	k := recipestest.OptIn()
	k.Settings = []recipes.Setting{
		{Name: "model", Title: "Model", Pattern: "[a-z0-9-]+/[a-z0-9.-]+"},
		{Name: "size", Title: "Size", Pattern: "[0-9]+", Default: "1"},
	}
	k.OptInAdvice = "Deploy {resource} to {environment} from its model page."
	r := recipestest.Registry(t)
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	return r
}

func copyPlan(t *testing.T, environment, doc string, bindings ...*Binding) *Result {
	t.Helper()
	return Compute(Input{
		Environment: environment, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: copyKinds(t), Bindings: bindings,
	})
}

func hasFinding(fs []Finding, code, path string) bool {
	return slices.ContainsFunc(fs, func(f Finding) bool { return f.Code == code && f.Path == path })
}

func TestEachEnvironmentRunsItsOwnCopy(t *testing.T) {
	for env, want := range map[string]string{"staging": targetA, "production": targetB} {
		r := optInPlan(t, env, twoCopies)
		if !r.Executable {
			t.Fatalf("%s: problems %+v", env, r.Problems)
		}
		for _, id := range append([]string{"resource:llm"}, optInComponents...) {
			a := findAction(r, id)
			if a == nil || a.ConnectionID != want || a.Resource == nil || a.Resource.Target != want {
				t.Fatalf("%s: %s = %+v, want it on %s", env, id, a, want)
			}
		}
	}
}

func TestEnvironmentTargetFindingPath(t *testing.T) {
	gone := strings.Replace(twoCopies, "target: "+targetB, "target: conn_missing", 1)
	r := optInPlan(t, "production", gone)
	if r.Executable || !hasFinding(r.Problems, "target-gone", "resources.llm.environments.production.target") {
		t.Fatalf("production on a missing target: problems %+v", r.Problems)
	}
	if s := optInPlan(t, "staging", gone); !s.Executable {
		t.Fatalf("staging was blocked by production's target: %+v", s.Problems)
	}

	// No target anywhere for production: the finding points at the top
	// level, where every environment without its own reads from.
	none := "version: 1\nresources:\n  llm:\n    kind: optin\n    provider: server\n" +
		"    environments:\n      staging: {enabled: true, target: " + targetA + "}\n      production: {enabled: true}\n"
	r = optInPlan(t, "production", none)
	if r.Executable || !hasFinding(r.Problems, "target-required", "resources.llm.target") {
		t.Fatalf("production without a target: problems %+v", r.Problems)
	}
	s := optInPlan(t, "staging", none)
	if a := findAction(s, "resource:llm"); !s.Executable || a == nil || a.ConnectionID != targetA {
		t.Fatalf("staging: %+v, problems %+v", a, s.Problems)
	}

	// A provisioned dependency runs on the resource's target, so its
	// target finding points at production's block too.
	db := "version: 1\nresources:\n  llm:\n    kind: optin\n    provider: server\n    target: " + targetA + "\n" +
		"    environments:\n      production: {enabled: true, target: conn_missing}\n"
	r = optInPlan(t, "production", db)
	if !hasFinding(r.Problems, "target-gone", "resources.llm.environments.production.target") || hasFinding(r.Problems, "target-gone", "resources.llm.target") {
		t.Fatalf("dependency target finding: %+v", r.Problems)
	}
}

func TestEnvironmentSettingsReachComponents(t *testing.T) {
	doc := "version: 1\nresources:\n  llm:\n    kind: optin\n    provider: server\n    target: " + targetA + "\n" +
		"    settings: {model: acme/x, size: \"2\"}\n" +
		"    environments:\n      staging: {enabled: true}\n      production: {enabled: true, settings: {size: \"4\"}}\n"
	for env, want := range map[string]map[string]string{
		"staging":    {"model": "acme/x", "size": "2"},
		"production": {"model": "acme/x", "size": "4"},
	} {
		r := copyPlan(t, env, doc)
		if !r.Executable {
			t.Fatalf("%s: problems %+v", env, r.Problems)
		}
		for _, id := range optInComponents {
			a := findAction(r, id)
			if a == nil || a.Resource.Component == nil || !maps.Equal(a.Resource.Component.Settings, want) {
				t.Fatalf("%s: %s = %+v, want settings %v", env, id, a, want)
			}
		}
	}

	// A bad value production sets is reported where production sets it,
	// and staging, which doesn't read it, plans.
	bad := strings.Replace(doc, `settings: {size: "4"}`, "settings: {size: big}", 1)
	if r := copyPlan(t, "production", bad); r.Executable || !hasFinding(r.Problems, "catalog-setting-invalid", "resources.llm.environments.production.settings.size") {
		t.Fatalf("production's bad size: %+v", r.Problems)
	}
	if r := copyPlan(t, "staging", bad); !r.Executable {
		t.Fatalf("staging: %+v", r.Problems)
	}
	// A bad top-level value is reported at the top.
	top := strings.Replace(doc, `size: "2"`, "size: big", 1)
	if r := copyPlan(t, "staging", top); r.Executable || !hasFinding(r.Problems, "catalog-setting-invalid", "resources.llm.settings.size") {
		t.Fatalf("staging's bad size: %+v", r.Problems)
	}
}

func TestComposedPlacementUsesTheEnvironmentsTarget(t *testing.T) {
	m := parse(t, twoCopies)
	options := composedConnections(t)
	options[1].Details["docker_id"] = "daemon-b"
	hostB := cloudprofiles.Target{ID: "t2", OrgID: "org", CloudID: "cloud_1", Name: "model-host", Provider: "server", ConnectionID: targetB, Runtime: "server", Revision: 5}
	a := edgeAssignment(hostB)

	if placed := composedPlacements(m, "production", []string{"llm"}, a, options); placed["llm"].ConnectionID != targetB {
		t.Fatalf("production placements = %+v, want llm on %s", placed, targetB)
	}
	if placed := composedPlacements(m, "staging", []string{"llm"}, a, options); len(placed) != 0 {
		t.Fatalf("staging placements = %+v, want none: staging runs on %s", placed, targetA)
	}

	// An unreviewed target counts as unverified only for the environment
	// that runs on it.
	hostB.Revision = 0
	a = edgeAssignment(hostB)
	if got := unverifiedPlacements(m, "production", []string{"llm"}, a, options); !got["llm"] {
		t.Fatalf("production unverified = %v", got)
	}
	if got := unverifiedPlacements(m, "staging", []string{"llm"}, a, options); len(got) != 0 {
		t.Fatalf("staging unverified = %v", got)
	}
}

// recordedInputs is a CatalogInputs that keeps the resource it was given.
type recordedInputs struct{ got []manifest.Resource }

func (d *recordedInputs) Digest(_ context.Context, _, _, _, _ string, r manifest.Resource) (map[string]string, error) {
	d.got = append(d.got, r)
	return map[string]string{"api": "sha256:a"}, nil
}

func TestCatalogInputsGetTheEnvironmentsView(t *testing.T) {
	doc := "version: 1\nresources:\n  llm:\n    kind: optin\n    provider: server\n    target: " + targetA + "\n" +
		"    settings: {model: acme/x, size: \"2\"}\n" +
		"    environments:\n      staging: {enabled: true}\n      production: {enabled: true, target: " + targetB + ", settings: {size: \"4\"}}\n"
	m := parse(t, doc)
	for env, want := range map[string]struct {
		target   string
		settings map[string]string
	}{
		"staging":    {targetA, map[string]string{"model": "acme/x", "size": "2"}},
		"production": {targetB, map[string]string{"model": "acme/x", "size": "4"}},
	} {
		rec := &recordedInputs{}
		s := &Service{kinds: copyKinds(t), inputs: CatalogInputMap{"optin": rec}}
		plan := &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: env}
		if _, err := s.catalogInputs(t.Context(), plan, m, []string{"llm"}); err != nil {
			t.Fatal(err)
		}
		if len(rec.got) != 1 || rec.got[0].Target != want.target || !maps.Equal(rec.got[0].Settings, want.settings) {
			t.Fatalf("%s: digest got %+v, want target %s and settings %v", env, rec.got, want.target, want.settings)
		}
	}
	if m.Resources["llm"].Target != targetA || m.Resources["llm"].Settings["size"] != "2" {
		t.Fatalf("the manifest's resource changed: %+v", m.Resources["llm"])
	}
}

// TestEndpointsAreCheckedFromTheEnvironmentsTarget: an external endpoint
// is checked from the target the environment's copy runs on, and the
// plan finds that check, so production's copy never uses staging's.
func TestEndpointsAreCheckedFromTheEnvironmentsTarget(t *testing.T) {
	doc := "version: 1\nresources:\n  llm:\n    kind: sample\n    provider: server\n    target: " + targetA + "\n" +
		"    uses: {postgres: {external: " + prodPG + "}}\n" +
		"    environments:\n      production: {target: " + targetB + "}\n"
	for env, want := range map[string]string{"staging": targetA, "production": targetB} {
		endpoints, dialer := &fakeEndpoints{}, &fakeDialer{}
		service := NewService(Deps{
			Store: catalogStore{}, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(),
			Connections: serverClouds{}, Kinds: recipestest.Registry(t), Endpoints: endpoints, Dialer: dialer,
		})
		checks := service.checkEndpoints(t.Context(), "org", env, parse(t, doc))
		if !slices.Equal(dialer.asked, []string{want}) {
			t.Fatalf("%s: dialed %v, want %s", env, dialer.asked, want)
		}
		r := Compute(Input{
			Environment: env, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: composedConnections(t), Kinds: recipestest.Registry(t), Endpoints: checks,
		})
		if hasCode(r.Problems, "catalog-endpoint-unavailable") || findAction(r, "resource:llm").ConnectionID != want {
			t.Fatalf("%s: problems %+v", env, r.Problems)
		}
	}

	// Checked from both targets, each environment's plan reads the check
	// made from its own: only production sees targetB's failure.
	req, consumer := *recipestest.Sample().Dependencies[0].External, catalogconnect.ConsumerOf(recipestest.Sample(), "postgres")
	both := map[string]EndpointCheck{
		endpointKey(prodPG, targetA, req, consumer): {ID: prodPG, Name: "prod-pg", Engine: "postgres", Found: true, Report: &catalogconnect.Report{Version: "16.4"}},
		endpointKey(prodPG, targetB, req, consumer): {ID: prodPG, Name: "prod-pg", Engine: "postgres", Found: true, Report: &catalogconnect.Report{Problems: []string{"db.example:5432 isn't reachable from the target: i/o timeout"}}},
	}
	for env, failed := range map[string]bool{"staging": false, "production": true} {
		r := Compute(Input{
			Environment: env, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: composedConnections(t), Kinds: recipestest.Registry(t), Endpoints: both,
		})
		if got := hasCode(r.Problems, "catalog-endpoint-unavailable"); got != failed {
			t.Fatalf("%s: unavailable %v, want %v: %+v", env, got, failed, r.Problems)
		}
	}
}

// TestEnvironmentGPUIsCountedOnItsOwnServer: production's copy asks for
// its own GPUs on its own server, and a shortage there points at
// production's gpu.
func TestEnvironmentGPUIsCountedOnItsOwnServer(t *testing.T) {
	doc := "version: 1\nresources:\n  llm:\n    kind: gpusample\n    provider: server\n    target: " + targetA + "\n" +
		"    gpu: {kind: nvidia-l4, count: 1}\n" +
		"    environments:\n      production: {target: " + targetB + ", gpu: {kind: nvidia-l4, count: 2}}\n"
	held := gpuBinding(t, "other--server", targetB, provider.GPURequest{Kind: "nvidia-l4", Count: 1})
	plan := func(env string) *Result {
		return Compute(Input{Environment: env, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t), Bindings: []*Binding{held}})
	}
	r := plan("production")
	if r.Executable || !hasFinding(r.Problems, "gpu-count-unavailable", "resources.llm.environments.production.gpu") {
		t.Fatalf("production: problems %+v", r.Problems)
	}
	// Staging runs one GPU on targetA, where nothing holds any.
	s := plan("staging")
	a := findAction(s, "resource:llm--server")
	if !s.Executable || a == nil || a.ConnectionID != targetA || a.Resource.Component.GPU.Count != 1 {
		t.Fatalf("staging: %+v, problems %+v", a, s.Problems)
	}
}

func TestNeedOffAdviceFromTheKind(t *testing.T) {
	doc := optInLLM + "services:\n  checkout: {source: cmd/api, provider: server, target: " + targetA + ", public: false, needs: [llm]}\n"
	r := copyPlan(t, "production", doc)
	want := "checkout gets no OPTIN_URL or OPTIN_ADMIN_URL in production: llm doesn't run there. Deploy llm to production from its model page."
	if !r.Executable || !slices.ContainsFunc(r.Warnings, func(f Finding) bool {
		return f.Code == "need-off" && f.Name == "checkout" && f.Path == "services.checkout.needs" && f.Message == want
	}) {
		t.Fatalf("warnings %+v, want %q", r.Warnings, want)
	}
}

// TestRemovedEnvironmentWarnsNeedOff: removing production's copy while
// checkout needs llm goes ahead (Decision 4). Production's plan deletes
// the copy and warns that checkout runs without it; staging's is as it was.
func TestRemovedEnvironmentWarnsNeedOff(t *testing.T) {
	services := "services:\n  checkout: {source: cmd/api, provider: server, target: " + targetA + ", public: false, needs: [llm]}\n"
	both := "version: 1\nresources:\n  llm:\n    kind: optin\n    provider: server\n    target: " + targetA + "\n" +
		"    environments:\n      staging: {enabled: true}\n      production: {enabled: true}\n" + services
	removed := strings.Replace(both, "production: {enabled: true}", "production: {enabled: false}", 1)

	bound := boundFrom(t, copyPlan(t, "production", both))
	r := copyPlan(t, "production", removed, bound...)
	if !r.Executable {
		t.Fatalf("production is blocked: %+v", r.Problems)
	}
	want := "checkout gets no OPTIN_URL or OPTIN_ADMIN_URL in production: llm doesn't run there. Deploy llm to production from its model page."
	if !slices.ContainsFunc(r.Warnings, func(f Finding) bool {
		return f.Code == "need-off" && f.Name == "checkout" && f.Path == "services.checkout.needs" && f.Message == want
	}) {
		t.Fatalf("warnings %+v, want %q", r.Warnings, want)
	}
	if a := findAction(r, "resource:llm"); a == nil || a.Op != Delete {
		t.Fatalf("llm in production: %+v", a)
	}
	checkout := findAction(r, "service:checkout")
	if checkout == nil || slices.ContainsFunc(checkout.Service.Env, func(e EnvLink) bool { return e.Key == "OPTIN_URL" }) {
		t.Fatalf("checkout in production: %+v", checkout)
	}

	before, after := copyPlan(t, "staging", both), copyPlan(t, "staging", removed)
	if !after.Executable || len(before.Actions) != len(after.Actions) {
		t.Fatalf("staging changed: %+v, problems %+v", after.Actions, after.Problems)
	}
	for i, a := range before.Actions {
		b := after.Actions[i]
		if a.ID != b.ID || a.Op != b.Op || a.ConnectionID != b.ConnectionID {
			t.Fatalf("staging %s: %s on %s, was %s on %s", a.ID, b.Op, b.ConnectionID, a.Op, a.ConnectionID)
		}
	}
}

// TestConnectedResourceOnTheEnvironmentsTarget: a composed resource and
// the database it connects both move to targetB in production, so they
// share a network there, and the database runs on targetB too.
func TestConnectedResourceOnTheEnvironmentsTarget(t *testing.T) {
	doc := "version: 1\nresources:\n" +
		"  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {resource: db}}, environments: {production: {target: " + targetB + "}}}\n" +
		"  db: {kind: postgres, provider: server, target: " + targetA + ", environments: {production: {target: " + targetB + "}}}\n"
	for env, want := range map[string]string{"staging": targetA, "production": targetB} {
		r := Compute(Input{
			Environment: env, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: composedConnections(t), Kinds: recipestest.Registry(t),
		})
		if !r.Executable {
			t.Fatalf("%s: problems %+v", env, r.Problems)
		}
		for _, id := range []string{"resource:app", "resource:db"} {
			if a := findAction(r, id); a == nil || a.ConnectionID != want {
				t.Fatalf("%s: %s = %+v, want it on %s", env, id, a, want)
			}
		}
	}

	// Only app moving leaves it across targets from db in production,
	// and the finding is about where the connection points.
	split := strings.Replace(doc, "  db: {kind: postgres, provider: server, target: "+targetA+", environments: {production: {target: "+targetB+"}}}\n", "  db: {kind: postgres, provider: server, target: "+targetA+"}\n", 1)
	r := Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, split), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: recipestest.Registry(t),
	})
	if r.Executable || !hasFinding(r.Problems, "catalog-connect-across-targets", "resources.app.uses.postgres.resource") {
		t.Fatalf("split targets: %+v", r.Problems)
	}
}

// TestEnvironmentCacheResizePath: production growing its own cache on a
// cluster is refused where production sets the size.
func TestEnvironmentCacheResizePath(t *testing.T) {
	kinds := cacheKinds(t, "[0-9]{1,10}")
	top := "version: 1\nresources:\n  llm: {kind: gpusample, provider: kubernetes, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 1}, settings: {cache_mb: \"153600\"}}\n"
	plan := func(doc string, bindings ...*Binding) *Result {
		return Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: []ConnectionOption{gpuCluster(t)}, Kinds: kinds, Bindings: bindings})
	}
	first := plan(top)
	if !first.Executable {
		t.Fatalf("first plan: %+v", first.Problems)
	}
	grown := strings.Replace(top, "}}\n", "}, environments: {production: {settings: {cache_mb: \"204800\"}}}}\n", 1)
	r := plan(grown, boundFrom(t, first)...)
	if r.Executable || !hasFinding(r.Problems, "catalog-cache-resize", "resources.llm.environments.production.settings.cache_mb") {
		t.Fatalf("resize in production: %+v", r.Problems)
	}
}

// TestManagedResourceRefusesAnEnvironmentTarget: a managed provider takes
// no target connection, so a target in production's block is refused at
// that block's path, in production alone. A production block that moves
// a server resource onto a managed provider leaves the top-level target
// behind (For drops it), so production has nothing to refuse.
func TestManagedResourceRefusesAnEnvironmentTarget(t *testing.T) {
	in := managedCatalogInput(t, "cloud-sql")
	in.Manifest = parse(t, "version: 1\nresources:\n  database:\n    kind: postgres\n    provider: cloud-sql\n    version: '17'\n    region: europe-west1\n"+
		"    environments:\n      production: {target: "+targetB+"}\n")
	const path = "resources.database.environments.production.target"
	if r := Compute(in); r.Executable || !hasFinding(r.Problems, "resource-target-unsupported", path) {
		t.Fatalf("production: want resource-target-unsupported at %s, got %+v", path, r.Problems)
	}
	in.Environment = "staging"
	if r := Compute(in); slices.ContainsFunc(r.Problems, func(f Finding) bool { return f.Code == "resource-target-unsupported" }) {
		t.Fatalf("staging: %+v", r.Problems)
	}

	in = managedCatalogInput(t, "cloud-sql")
	in.Manifest = parse(t, "version: 1\nresources:\n  database:\n    kind: postgres\n    provider: server\n    target: "+targetA+"\n    version: '17'\n"+
		"    environments:\n      production: {provider: cloud-sql, region: europe-west1}\n")
	r := Compute(in)
	if slices.ContainsFunc(r.Problems, func(f Finding) bool { return f.Code == "resource-target-unsupported" }) {
		t.Fatalf("production on cloud-sql kept the top-level target: %+v", r.Problems)
	}
	if !r.Executable || len(r.Actions) != 1 || r.Actions[0].Resource.Managed == nil || r.Actions[0].Resource.Target != "" {
		t.Fatalf("want a managed production copy with no target: %+v", r)
	}
}
