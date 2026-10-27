package planning

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// settingKinds is recipestest.Registry with Sample taking a required model
// and a size that defaults to 1.
func settingKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	sample := recipestest.Sample()
	sample.Settings = []recipes.Setting{
		{Name: "model", Title: "Model", Pattern: "[a-z0-9-]+/[a-z0-9.-]+", Required: true},
		{Name: "size", Title: "Size", Pattern: "[0-9]+", Default: "1"},
	}
	r := recipes.NewRegistry()
	for _, k := range []recipes.Kind{sample, recipestest.GPUSample()} {
		if err := r.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func settingsApp(settings string) string {
	return "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + ", settings: " + settings + "}\n"
}

var sampleComponents = []string{"resource:app--api", "resource:app--migrate", "resource:app--worker"}

func TestSettingsLandOnComponents(t *testing.T) {
	r := composedPlan(t, settingKinds(t), settingsApp("{model: acme/x}"))
	if !r.Executable {
		t.Fatalf("problems: %+v", r.Problems)
	}
	want := map[string]string{"model": "acme/x", "size": "1"}
	for _, id := range sampleComponents {
		a := findAction(r, id)
		if a == nil || a.Resource.Component == nil || !maps.Equal(a.Resource.Component.Settings, want) {
			t.Fatalf("%s: %+v, want settings %v", id, a, want)
		}
	}
	// Each component has its own copy.
	findAction(r, "resource:app--api").Resource.Component.Settings["model"] = "changed"
	if got := findAction(r, "resource:app--worker").Resource.Component.Settings["model"]; got != "acme/x" {
		t.Fatalf("components share their settings: worker has %q", got)
	}

	// A set value wins over the default.
	r = composedPlan(t, settingKinds(t), settingsApp("{model: acme/x, size: \"4\"}"))
	if got := findAction(r, "resource:app--api").Resource.Component.Settings["size"]; !r.Executable || got != "4" {
		t.Fatalf("size %q, problems %+v", got, r.Problems)
	}

	// A kind without settings puts none on its components.
	r = composedPlan(t, recipestest.Registry(t), sampleApp)
	if a := findAction(r, "resource:app--api"); a.Resource.Component.Settings != nil {
		t.Fatalf("settings without a kind that takes them: %v", a.Resource.Component.Settings)
	}
}

func TestSettingProblems(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kinds *recipes.Registry
		doc   string
		want  Finding
	}{
		{"unknown", settingKinds(t), settingsApp("{model: acme/x, colour: red}"),
			Finding{Code: "catalog-setting-unknown", Path: "resources.app.settings.colour", Message: "app has no setting colour."}},
		{"invalid", settingKinds(t), settingsApp("{model: \"Acme X\"}"),
			Finding{Code: "catalog-setting-invalid", Path: "resources.app.settings.model", Message: "app's Model doesn't look right."}},
		{"invalid default override", settingKinds(t), settingsApp("{model: acme/x, size: big}"),
			Finding{Code: "catalog-setting-invalid", Path: "resources.app.settings.size", Message: "app's Size doesn't look right."}},
		{"missing required", settingKinds(t), settingsApp("{size: \"2\"}"),
			Finding{Code: "catalog-setting-required", Path: "resources.app.settings.model", Message: "app needs Model."}},
		{"kind without settings", recipestest.Registry(t), settingsApp("{model: acme/x}"),
			Finding{Code: "catalog-setting-unknown", Path: "resources.app.settings.model", Message: "app has no setting model."}},
		{"resource that takes none", recipestest.Registry(t), "version: 1\nresources:\n  app: {kind: postgres, provider: server, target: " + targetA + ", settings: {model: acme/x}}\n",
			Finding{Code: "catalog-setting-unknown", Path: "resources.app.settings.model", Message: "app has no setting model."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := composedPlan(t, tc.kinds, tc.doc)
			if r.Executable {
				t.Fatal("the plan is executable")
			}
			if !slices.ContainsFunc(r.Problems, func(f Finding) bool {
				return f.Code == tc.want.Code && f.Subject == ResourceSubject && f.Name == "app" && f.Path == tc.want.Path && f.Message == tc.want.Message
			}) {
				t.Fatalf("problems %+v, want %+v", r.Problems, tc.want)
			}
		})
	}
}

func TestSettingChangePlansAnUpdate(t *testing.T) {
	bound := boundFrom(t, composedPlan(t, settingKinds(t), settingsApp("{model: acme/a}")))

	r := composedPlan(t, settingKinds(t), settingsApp("{model: acme/b}"), bound...)
	if !r.Executable {
		t.Fatalf("problems: %+v", r.Problems)
	}
	want := Change{Field: "settings", From: "model=acme/a, size=1", To: "model=acme/b, size=1"}
	for _, id := range sampleComponents {
		a := findAction(r, id)
		if a.Op != Update || !slices.Equal(a.Changes, []Change{want}) {
			t.Fatalf("%s: op %s, changes %+v, want %+v", id, a.Op, a.Changes, want)
		}
	}
	if a := findAction(r, "resource:app"); a.Op != Keep {
		t.Fatalf("app: op %s, reason %q", a.Op, a.Reason)
	}

	r = composedPlan(t, settingKinds(t), settingsApp("{model: acme/a}"), bound...)
	for _, a := range r.Actions {
		if a.Op != Keep {
			t.Fatalf("same settings: %s op %s, changes %+v", a.ID, a.Op, a.Changes)
		}
	}
}

// optInKinds is recipestest.Registry with the opt-in kind.
func optInKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	r := recipestest.Registry(t)
	if err := r.Register(recipestest.OptIn()); err != nil {
		t.Fatal(err)
	}
	return r
}

func optInPlan(t *testing.T, environment, doc string) *Result {
	t.Helper()
	return Compute(Input{
		Environment: environment, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: optInKinds(t),
	})
}

const optInLLM = "version: 1\nresources:\n  llm: {kind: optin, provider: server, target: " + targetA + ", environments: {staging: {enabled: true}}}\n"

func TestOptInKindRunsOnlyWhereEnabled(t *testing.T) {
	staging := optInPlan(t, "staging", optInLLM)
	if !staging.Executable || findAction(staging, "resource:llm") == nil || findAction(staging, "resource:llm--api") == nil {
		t.Fatalf("staging: actions %+v, problems %+v", staging.Actions, staging.Problems)
	}

	production := optInPlan(t, "production", optInLLM)
	if !production.Executable || len(production.Actions) != 0 || len(production.Problems) != 0 {
		t.Fatalf("production: actions %+v, problems %+v", production.Actions, production.Problems)
	}

	// enabled: false is off too, and an ordinary kind left unset stays on.
	off := optInPlan(t, "staging", "version: 1\nresources:\n  llm: {kind: optin, provider: server, target: "+targetA+", environments: {staging: {enabled: false}}}\n")
	if len(off.Actions) != 0 || len(off.Problems) != 0 {
		t.Fatalf("enabled false: actions %+v, problems %+v", off.Actions, off.Problems)
	}
	plain := optInPlan(t, "production", "version: 1\nresources:\n  llm: {kind: sample, provider: server, target: "+targetA+", environments: {staging: {enabled: true}}}\n")
	if findAction(plain, "resource:llm") == nil {
		t.Fatalf("a kind that doesn't opt in stopped running: %+v", plain.Problems)
	}

	// Where it's off, what was deployed there is deleted, as for enabled: false.
	bound := boundFrom(t, staging)
	gone := Compute(Input{
		Environment: "staging", Graph: infrastructureGraph(), Manifest: parse(t, "version: 1\nresources:\n  llm: {kind: optin, provider: server, target: "+targetA+"}\n"),
		Providers: providers.Registry(), Connections: composedConnections(t), Kinds: optInKinds(t), Bindings: bound,
	})
	if a := findAction(gone, "resource:llm"); a == nil || a.Op != Delete {
		t.Fatalf("unset opt-in resource with a binding: %+v, problems %+v", a, gone.Problems)
	}
}

func TestOptInResourceIsOffForNeeds(t *testing.T) {
	doc := optInLLM + "services:\n  checkout: {source: cmd/api, provider: server, target: " + targetA + ", public: false, needs: [llm]}\n"

	production := optInPlan(t, "production", doc)
	if !production.Executable {
		t.Fatalf("production is blocked: %+v", production.Problems)
	}
	want := Finding{Code: "need-off", Subject: ServiceSubject, Name: "checkout", Path: "services.checkout.needs", Message: "checkout gets no OPTIN_URL or OPTIN_ADMIN_URL in production: llm doesn't run there."}
	if !slices.ContainsFunc(production.Warnings, func(f Finding) bool {
		return f.Code == want.Code && f.Subject == want.Subject && f.Name == want.Name && f.Path == want.Path && f.Message == want.Message
	}) {
		t.Fatalf("warnings %+v, want %+v", production.Warnings, want)
	}
	checkout := findAction(production, "service:checkout")
	if checkout == nil || len(checkout.Service.Env) != 0 || slices.Contains(checkout.DependsOn, "resource:llm") {
		t.Fatalf("checkout in production: %+v", checkout)
	}

	staging := optInPlan(t, "staging", doc)
	checkout = findAction(staging, "service:checkout")
	if !staging.Executable || hasCode(staging.Warnings, "need-off") || checkout == nil || len(checkout.Service.Env) != 2 {
		t.Fatalf("staging: checkout %+v, problems %+v, warnings %+v", checkout, staging.Problems, staging.Warnings)
	}

	// A kind that doesn't opt in still blocks a service that needs it while it's off.
	plain := strings.Replace(doc, "kind: optin", "kind: sample", 1)
	plain = strings.Replace(plain, "staging: {enabled: true}", "production: {enabled: false}", 1)
	r := optInPlan(t, "production", plain)
	if r.Executable || !slices.ContainsFunc(r.Problems, func(f Finding) bool {
		return f.Code == "need-off" && f.Message == "checkout needs llm, which is off in production."
	}) {
		t.Fatalf("ordinary need-off: problems %+v", r.Problems)
	}
}

func TestPerGPUResourcesScale(t *testing.T) {
	gpu := recipestest.GPUSample()
	gpu.Components[0].Resources.PerGPU = &recipes.ResourceStep{CPUMillis: 1500, MemoryMB: 6144, ShmMB: 2048}
	kinds := recipes.NewRegistry()
	if err := kinds.Register(gpu); err != nil {
		t.Fatal(err)
	}
	doc := "version: 1\nresources:\n  llm: {kind: gpusample, provider: server, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 2}}\n"
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: kinds})
	server := findAction(r, "resource:llm--server")
	if !r.Executable || server == nil {
		t.Fatalf("problems %+v", r.Problems)
	}
	got := server.Resource.Component.Component.Resources
	if got.CPUMillis != 5000 || got.MemoryMB != 20480 || got.ShmMB != 6144 || got.PerGPU != nil || got.GPU == nil {
		t.Fatalf("resources %+v", got)
	}
	if sidecar := findAction(r, "resource:llm--sidecar").Resource.Component.Component.Resources; sidecar.CPUMillis != 500 || sidecar.MemoryMB != 256 {
		t.Fatalf("the CPU sidecar was scaled: %+v", sidecar)
	}

	// The same plan again keeps what it deployed.
	again := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: kinds, Bindings: boundFrom(t, r)})
	if a := findAction(again, "resource:llm--server"); a.Op != Keep {
		t.Fatalf("again: op %s, changes %+v", a.Op, a.Changes)
	}
}
