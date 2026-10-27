package ai_test

import (
	"slices"
	"testing"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	aiext "github.com/xraph/loomplane/extensions/ai"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

func extensionNames(cfg config.Config) []string {
	exts := platform.Extensions(cfg)
	names := make([]string, 0, len(exts))
	for _, ext := range exts {
		names = append(names, ext.Name())
	}
	return names
}

func TestRoutesAreAbsentWhenAIIsOff(t *testing.T) {
	var cfg config.Config
	if names := extensionNames(cfg); slices.Contains(names, aiext.Name) {
		t.Errorf("AI off, and the platform registers %s: %v", aiext.Name, names)
	}
	cfg.AI.Enabled = true
	names := extensionNames(cfg)
	if !slices.Contains(names, aiext.Name) {
		t.Fatalf("AI on, and the platform doesn't register %s: %v", aiext.Name, names)
	}
	// It registers after what it depends on, and before the dashboard's
	// catch-all.
	ai := slices.Index(names, aiext.Name)
	for _, before := range []string{"loomplane-identity", "loomplane-planning"} {
		if i := slices.Index(names, before); i < 0 || i > ai {
			t.Errorf("%s at %d, %s at %d: %v", before, i, aiext.Name, ai, names)
		}
	}
	if names[len(names)-1] == aiext.Name {
		t.Errorf("%s registers after the dashboard: %v", aiext.Name, names)
	}
}

func TestModelHooksAreRegistered(t *testing.T) {
	// What identity and planning provide. Registering opens nothing, so a
	// pool that was never connected will do.
	db, err := grove.Open(pgdriver.New())
	if err != nil {
		t.Fatal(err)
	}
	hooks, inputs := deployments.CatalogHookMap{}, planning.CatalogInputMap{}
	app := forge.New(forge.WithAppName("ai-test"))
	c := app.Container()
	for _, provide := range []error{
		forge.ProvideValue(c, db),
		forge.ProvideValue(c, security.NewAuthorizer(nil, nil, nil)),
		forge.ProvideValue(c, &chronicle.Chronicle{}),
		forge.ProvideValue(c, hooks),
		forge.ProvideValue(c, inputs),
	} {
		if provide != nil {
			t.Fatal(provide)
		}
	}
	var cfg config.Config
	cfg.AI.Enabled = true
	cfg.SecretKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	if err = aiext.New(cfg).Register(app); err != nil {
		t.Fatal(err)
	}

	// The maps planning shares hold every model kind, and only those.
	wantKinds := recipes.ModelKinds()
	slices.Sort(wantKinds)
	for name, m := range map[string][]string{"hooks": keys(hooks), "inputs": keys(inputs)} {
		slices.Sort(m)
		if !slices.Equal(m, wantKinds) {
			t.Errorf("%s: %v, want %v", name, m, wantKinds)
		}
	}
	for _, kind := range wantKinds {
		if _, ok := hooks[kind].(*models.Hooks); !ok {
			t.Errorf("%s hooks: %T", kind, hooks[kind])
		}
		if _, ok := inputs[kind].(models.Inputs); !ok {
			t.Errorf("%s inputs: %T", kind, inputs[kind])
		}
	}
	// What DI resolves is the same map, so planning sees them.
	shared, err := forge.InjectType[deployments.CatalogHookMap](c)
	if err != nil || len(shared) != len(wantKinds) {
		t.Errorf("DI hook map: %d kinds, %v", len(shared), err)
	}
	if svc, err := forge.InjectType[*aisettings.Service](c); err != nil || svc == nil {
		t.Errorf("AI settings not provided: %v", err)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
