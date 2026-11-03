package planning

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/providers"
)

// computeOnly plans acme in production against live, moving only the
// services named.
func computeOnly(t *testing.T, yaml string, live []*Binding, only ...string) *Result {
	t.Helper()
	in := Input{
		Environment: "production", Graph: polyglot(t), Manifest: parse(t, yaml),
		Providers: providers.Registry(), Bindings: live, Connections: connected,
	}
	if len(only) > 0 {
		in.Only = map[string]bool{}
		for _, name := range only {
			in.Only[name] = true
		}
	}
	return Compute(in)
}

// acmeChanged is acme with api and worker both changed, so a plan that
// moves everything updates both.
var acmeChanged = strings.NewReplacer(
	"    scale: {min: 1, max: 3}\n", "    scale: {min: 1, max: 5}\n",
	"    provider: ecs-fargate\n", "    provider: ecs-fargate\n    scale: {min: 2, max: 4}\n",
).Replace(acme)

func TestCompute_aPlanThatNamesServicesKeepsTheRestAsTheyRun(t *testing.T) {
	// Everything but gateway runs in production.
	live := slices.DeleteFunc(deployed(t, compute(t, acme, "production")), func(b *Binding) bool { return b.Name == "gateway" })

	all := computeOnly(t, acmeChanged, live)
	if action(t, all, "service:worker").Op != Update || action(t, all, "service:api").Op != Update {
		t.Fatalf("the test needs api and worker changed: %+v", all.Actions)
	}

	r := computeOnly(t, acmeChanged, live, "api")
	if len(r.Problems) != 0 {
		t.Fatalf("problems %+v", r.Problems)
	}
	if api := action(t, r, "service:api"); api.Op != Update {
		t.Fatalf("api %+v", api)
	}
	worker := action(t, r, "service:worker")
	if worker.Op != Keep || worker.Reason != "Not part of this deploy." || worker.Service == nil {
		t.Fatalf("worker %+v", worker)
	}
	if web := action(t, r, "service:web"); web.Op != Keep {
		t.Fatalf("web %+v", web)
	}
	// Gateway runs nowhere yet, so it isn't created, and a warning says why.
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:gateway" }) {
		t.Fatalf("a service the plan doesn't name was created: %+v", r.Actions)
	}
	w := warning(r, "not-in-deploy", "gateway")
	if w == nil || !strings.Contains(w.Message, "isn't part of this deploy") || !strings.Contains(w.Message, "isn't created") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	// Worker's manifest change waits, and says so.
	if w := warning(r, "not-in-deploy", "worker"); w == nil || !strings.Contains(w.Message, "wait") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	if got := codes(r.Warnings); slices.Contains(got, "service-left-out:gateway") || slices.Contains(got, "service-left-out:worker") {
		t.Fatalf("a service the manifest deploys was reported as left out: %v", got)
	}
	// Nothing that runs is deleted.
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.Op == Delete }) {
		t.Fatalf("deletes %+v", r.Actions)
	}
}

func TestCompute_aServiceNeedingOneOutsideTheDeployThatRunsNowhereIsAProblem(t *testing.T) {
	r := computeOnly(t, acme, nil, "web")
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:api" }) {
		t.Fatal("a service the plan doesn't name was created")
	}
	var need *Finding
	for i := range r.Problems {
		if r.Problems[i].Code == "need-held" && r.Problems[i].Name == "web" {
			need = &r.Problems[i]
		}
	}
	if need == nil || !strings.Contains(need.Message, "isn't part of this deploy") || !strings.Contains(need.Message, "add api to this deploy") {
		t.Fatalf("problems %+v", r.Problems)
	}
}

func TestCompute_noServicesNamedPlansEverything(t *testing.T) {
	live := deployed(t, compute(t, acme, "production"))
	want := computeOnly(t, acmeChanged, live)
	got := Compute(Input{
		Environment: "production", Graph: polyglot(t), Manifest: parse(t, acmeChanged),
		Providers: providers.Registry(), Bindings: live, Connections: connected, Only: map[string]bool{},
	})
	if len(got.Actions) != len(want.Actions) || got.Summary != want.Summary {
		t.Fatalf("an empty list planned %+v, want %+v", got.Summary, want.Summary)
	}
}

// servicesWorld is promotionWorld with api and web in every manifest
// revision.
func servicesWorld(t *testing.T) *promoWorld {
	t.Helper()
	w := promotionWorld(t)
	for _, rev := range w.manifests.byID {
		rev.Manifest = parse(t, "version: 1\nservices:\n  api: {repo: api, source: .}\n  web: {repo: web, source: .}\n")
	}
	return w
}

func TestCreate_refusesAServiceTheManifestDoesntHave(t *testing.T) {
	w := servicesWorld(t)
	for _, in := range []CreatePlanInput{
		{Environment: "production", Services: []string{"api", "nope"}},
		{Environment: "production", From: "staging", Services: []string{"nope"}},
	} {
		_, err := w.create(t, in)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("%+v: %v", in, err)
		}
	}
}

func TestCreate_recordsTheServicesItNames(t *testing.T) {
	w := servicesWorld(t)
	plan, err := w.create(t, CreatePlanInput{Environment: "production", Services: []string{" web", "api", "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Services, []string{"api", "web"}) {
		t.Fatalf("services %q", plan.Services)
	}
	stored, err := w.store.GetPlan(t.Context(), "org", plan.ID)
	if err != nil || !slices.Equal(stored.Services, []string{"api", "web"}) {
		t.Fatalf("stored %+v %v", stored, err)
	}

	// An empty list is every service, as before.
	plan, err = w.create(t, CreatePlanInput{Environment: "production", Services: []string{}})
	if err != nil || plan.Services != nil {
		t.Fatalf("services %q, %v", plan.Services, err)
	}

	// A promotion names them too.
	plan, err = w.create(t, CreatePlanInput{Environment: "production", From: "staging", Services: []string{"api"}})
	if err != nil || !slices.Equal(plan.Services, []string{"api"}) {
		t.Fatalf("promotion services %q, %v", plan.Services, err)
	}
}

func TestCreate_aCatalogPlanNamesNoServices(t *testing.T) {
	w := servicesWorld(t)
	w.manifests.byID["man_7"] = &manifests.Revision{ID: "man_7", Revision: 7, Manifest: parse(t, "version: 1\n"), Digest: "d"}
	if _, err := w.create(t, CreatePlanInput{Environment: "production", Catalog: true, Services: []string{"api"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestCreate_theServicesNamedAreInTheRevision(t *testing.T) {
	w := servicesWorld(t)
	all, err := w.create(t, CreatePlanInput{Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	// Naming every service plans the same thing, but it's a different
	// request, so it's a different revision.
	named, err := w.create(t, CreatePlanInput{Environment: "production", Services: []string{"api", "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if all.Revision == "" || all.Revision == named.Revision {
		t.Fatalf("revisions %q and %q", all.Revision, named.Revision)
	}
	again, err := w.create(t, CreatePlanInput{Environment: "production"})
	if err != nil || again.Revision != all.Revision {
		t.Fatalf("revision %q, want %q (%v)", again.Revision, all.Revision, err)
	}
}
