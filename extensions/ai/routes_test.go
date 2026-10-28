package ai_test

import (
	"context"
	"slices"
	"testing"

	"github.com/xraph/chronicle"
	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/store/memory"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	aiext "github.com/xraph/loomplane/extensions/ai"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platform"
	"github.com/xraph/loomplane/internal/projects"
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
	if names := extensionNames(cfg); slices.Contains(names, aiext.Name) || !slices.Contains(names, aiext.OffName) {
		t.Errorf("AI off, and the platform registers %v", names)
	}
	cfg.AI.Enabled = true
	names := extensionNames(cfg)
	if !slices.Contains(names, aiext.Name) || slices.Contains(names, aiext.OffName) {
		t.Fatalf("AI on, and the platform registers %v", names)
	}
	// It registers after what it depends on, and before the dashboard's
	// catch-all.
	ai := slices.Index(names, aiext.Name)
	for _, before := range []string{"loomplane-identity", "loomplane-planning", "loomplane-cloud-infrastructure"} {
		if i := slices.Index(names, before); i < 0 || i > ai {
			t.Errorf("%s at %d, %s at %d: %v", before, i, aiext.Name, ai, names)
		}
	}
	if names[len(names)-1] == aiext.Name {
		t.Errorf("%s registers after the dashboard: %v", aiext.Name, names)
	}
}

func TestModelHooksAreRegistered(t *testing.T) {
	// What identity, projects, connections and planning provide.
	// Registering opens nothing, so a pool that was never connected and
	// services that are never called will do.
	db, err := grove.Open(pgdriver.New())
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := dispatch.New(dispatch.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(dispatcher)
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
		forge.ProvideValue(c, new(manifests.Service)),
		forge.ProvideValue(c, new(planning.Service)),
		forge.ProvideValue(c, gpus.NewService(gpus.Deps{})),
		forge.ProvideValue(c, new(connections.Service)),
		forge.ProvideValue(c, new(projects.Service)),
		forge.ProvideValue(c, new(cloudinfra.Service)),
		forge.ProvideValue(c, new(cloudprofiles.Service)),
		forge.ProvideValue(c, new(deployments.CatalogAccess)),
		forge.ProvideValue(c, new(deployments.VaultResourceSecrets)),
		forge.ProvideValue[security.PrincipalVerifier](c, rejectAll{}),
		forge.ProvideValue(c, eng),
	} {
		if provide != nil {
			t.Fatal(provide)
		}
	}
	var cfg config.Config
	cfg.AI.Enabled = true
	cfg.SecretKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	ext := aiext.New(cfg)
	if err = ext.Register(app); err != nil {
		t.Fatal(err)
	}
	// The reconcile job is registered, and Start schedules it every minute
	// on Loomplane's queue.
	if _, ok := eng.Registry().Get(aiext.JobReconcileModels); !ok {
		t.Errorf("no %s job: %v", aiext.JobReconcileModels, eng.Registry().Names())
	}
	if err = ext.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	crons, err := eng.CronStore().ListCrons(t.Context())
	if err != nil || len(crons) != 1 || crons[0].JobName != aiext.JobReconcileModels || crons[0].Schedule != "@every 1m" ||
		crons[0].Queue != orchestration.Queue {
		t.Errorf("crons %+v, %v", crons, err)
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
	if svc, err := forge.InjectType[*models.Service](c); err != nil || svc == nil {
		t.Errorf("the models service not provided: %v", err)
	}
}

// rejectAll is an API key check that rejects every key.
type rejectAll struct{}

func (rejectAll) ValidatePrincipal(context.Context, security.Principal) error {
	return security.ErrForbidden
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// With AI off, the reconcile's cron entry left from when it was on keeps
// firing, and its job runs and does nothing instead of failing for want
// of a handler. Off deletes no schedule, so a replica with AI on during a
// rollout keeps its own.
func TestAIOffQuietsItsSchedule(t *testing.T) {
	dispatcher, err := dispatch.New(dispatch.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, name := range []string{aiext.JobReconcileModels, orchestration.JobDriftCheck} {
		if err = engine.RegisterCron(ctx, eng, &cron.Definition[struct{}]{Name: name, JobName: name, Schedule: "@every 1m", Queue: orchestration.Queue}); err != nil {
			t.Fatal(err)
		}
	}
	app := forge.New(forge.WithAppName("ai-off-test"))
	off := aiext.NewOff(func() *engine.Engine { return eng })
	if err = off.Register(app); err != nil {
		t.Fatal(err)
	}
	if err = off.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, ok := eng.Registry().Get(aiext.JobReconcileModels)
	if !ok {
		t.Fatalf("no %s handler with AI off: %v", aiext.JobReconcileModels, eng.Registry().Names())
	}
	// What the cron enqueues runs, and does nothing.
	for _, payload := range [][]byte{nil, []byte("{}")} {
		if err = run(ctx, payload); err != nil {
			t.Errorf("the reconcile with AI off, payload %q: %v", payload, err)
		}
	}
	if names := eng.Registry().Names(); len(names) != 1 {
		t.Errorf("AI off registers %v", names)
	}
	crons, err := eng.CronStore().ListCrons(ctx)
	if err != nil || len(crons) != 2 {
		t.Errorf("crons %+v, %v", crons, err)
	}
}
