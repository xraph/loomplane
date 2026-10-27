package planning

import (
	"maps"
	"slices"
	"testing"

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
