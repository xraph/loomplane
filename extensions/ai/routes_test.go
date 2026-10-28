package ai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/notifications"
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

// With AI off, the platform registers only the extension that quiets the
// AI schedules. It serves no route, so the models API answers 404.
func TestModelRoutesAbsentWhenAIIsOff(t *testing.T) {
	var cfg config.Config
	if names := extensionNames(cfg); slices.Contains(names, aiext.Name) {
		t.Fatalf("AI off, and the platform registers %v", names)
	}
	dispatcher, err := dispatch.New(dispatch.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	app := forge.New(forge.WithAppName("ai-off-routes"))
	if err = aiext.NewOff(func() *engine.Engine { return eng }).Register(app); err != nil {
		t.Fatal(err)
	}
	for _, info := range app.Router().Routes() {
		if strings.Contains(info.Path, "/ai/") {
			t.Errorf("AI off serves %s %s", info.Method, info.Path)
		}
	}
	for _, path := range []string{"/api/v1/ai/models/registry", "/api/v1/ai/settings", "/api/v1/ai/models/deployments"} {
		out := httptest.NewRecorder()
		app.Router().ServeHTTP(out, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if out.Code != http.StatusNotFound {
			t.Errorf("AI off: GET %s answered %d", path, out.Code)
		}
	}
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
		forge.ProvideValue(c, new(notifications.Service)),
		forge.ProvideValue[security.PrincipalVerifier](c, rejectAll{}),
		forge.ProvideValue(c, identity.Guard{}),
	} {
		if provide != nil {
			t.Fatal(provide)
		}
	}
	var cfg config.Config
	cfg.AI.Enabled = true
	cfg.SecretKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	ext := aiext.New(cfg, func() *engine.Engine { return eng })
	if err = ext.Register(app); err != nil {
		t.Fatal(err)
	}
	// The reconcile and accrual jobs are registered, and Start schedules
	// them on Loomplane's queue: the reconcile every minute, the accrual
	// every five.
	for _, name := range []string{aiext.JobReconcileModels, aiext.JobAccrueModels} {
		if _, ok := eng.Registry().Get(name); !ok {
			t.Errorf("no %s job: %v", name, eng.Registry().Names())
		}
	}
	if err = ext.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	crons, err := eng.CronStore().ListCrons(t.Context())
	schedules := map[string]string{}
	for _, entry := range crons {
		if entry.Queue != orchestration.Queue {
			t.Errorf("cron %s on queue %q", entry.JobName, entry.Queue)
		}
		schedules[entry.JobName] = entry.Schedule
	}
	if err != nil || len(crons) != 2 || schedules[aiext.JobReconcileModels] != "@every 1m" || schedules[aiext.JobAccrueModels] != "@every 5m" {
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
	// And it serves the models API.
	served := 0
	for _, info := range app.Router().Routes() {
		if strings.HasPrefix(info.Name, "aiModels.") || strings.HasPrefix(info.Name, "aiModelDeployments.") || strings.HasPrefix(info.Name, "aiSettings.") {
			served++
		}
	}
	if served != 17 {
		t.Errorf("%d AI routes on the app's router", served)
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

// With AI off, the reconcile's and the accrual's cron entries left from
// when it was on keep firing, and their jobs run and do nothing instead of
// failing for want of a handler. With AI off the accrual counts nothing
// and sends no budget notice. Off deletes no schedule, so a replica with AI on during a
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
	for _, name := range []string{aiext.JobReconcileModels, aiext.JobAccrueModels, orchestration.JobDriftCheck} {
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
	for _, name := range []string{aiext.JobReconcileModels, aiext.JobAccrueModels} {
		run, ok := eng.Registry().Get(name)
		if !ok {
			t.Fatalf("no %s handler with AI off: %v", name, eng.Registry().Names())
		}
		// What the cron enqueues runs, and does nothing.
		for _, payload := range [][]byte{nil, []byte("{}")} {
			if err = run(ctx, payload); err != nil {
				t.Errorf("%s with AI off, payload %q: %v", name, payload, err)
			}
		}
	}
	if names := eng.Registry().Names(); len(names) != 2 {
		t.Errorf("AI off registers %v", names)
	}
	crons, err := eng.CronStore().ListCrons(ctx)
	if err != nil || len(crons) != 3 {
		t.Errorf("crons %+v, %v", crons, err)
	}
}
