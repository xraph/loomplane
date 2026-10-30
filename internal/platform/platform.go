// Package platform composes Loomplane: one Forge app with the ecosystem's
// extensions and Loomplane's own, registered in dependency order.
//
// Lifecycle (Forge central migrations):
//
//  1. Forge registers every extension in dependency order. Nothing touches
//     the database yet; the grove extension only collects migration groups.
//  2. One migration pass runs every group: Loomplane's own, authsome,
//     Warden, Chronicle, Dispatch and Ctrl Plane (database/migrations).
//  3. Forge starts every extension in the same order, then binds HTTP.
//
// Shutdown runs the other way, and the grove extension closes the pool last.
package platform

import (
	"context"
	"fmt"
	"os"
	"time"

	cpext "github.com/xraph/ctrlplane/extension"
	drt "github.com/xraph/dispatch/durable/runtime"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard"
	"github.com/xraph/grove"
	groveDriver "github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	groveext "github.com/xraph/grove/extension"

	"github.com/xraph/loomplane/database/migrations"
	aiext "github.com/xraph/loomplane/extensions/ai"
	cloudinfraext "github.com/xraph/loomplane/extensions/cloudinfra"
	connectionsext "github.com/xraph/loomplane/extensions/connections"
	dbgatewayext "github.com/xraph/loomplane/extensions/dbgateway"
	discoveryext "github.com/xraph/loomplane/extensions/discovery"
	environmentsext "github.com/xraph/loomplane/extensions/environments"
	federationext "github.com/xraph/loomplane/extensions/federation"
	"github.com/xraph/loomplane/extensions/identity"
	infraext "github.com/xraph/loomplane/extensions/infrastructure"
	notificationsext "github.com/xraph/loomplane/extensions/notifications"
	octopusext "github.com/xraph/loomplane/extensions/octopus"
	planningext "github.com/xraph/loomplane/extensions/planning"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	realtimeext "github.com/xraph/loomplane/extensions/realtime"
	"github.com/xraph/loomplane/extensions/web"
	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/plugins"
)

const (
	shutdownTimeout = 30 * time.Second
	openTimeout     = 30 * time.Second
	maxConnLifetime = 30 * time.Minute
	maxConnIdleTime = 5 * time.Minute
	apiCronLease    = 10 * time.Second
	workerCronLease = 5 * time.Second
	// forgeEnvPrefix is one nobody sets: Loomplane reads its own LOOMPLANE_
	// variables (internal/config), and Forge shouldn't read them too.
	forgeEnvPrefix = "LOOMPLANE_FORGE_"
)

// New builds the app. It does no I/O: Forge registers and starts the
// extensions when the app starts.
func New(cfg config.Config) (forge.App, error) {
	app := forge.New(
		forge.WithAppName("loomplane"),
		forge.WithAppVersion(buildinfo.Version),
		forge.WithAppEnvironment(cfg.Environment),
		forge.WithHTTPAddress(cfg.HTTPAddr),
		forge.WithShutdownTimeout(shutdownTimeout),
		forge.WithEnvPrefix(forgeEnvPrefix),
		forge.WithCentralMigrations(),
		forge.WithAppRouterOptions(forge.WithOpenAPI(openAPI(cfg)), forge.WithAsyncAPI(forge.AsyncAPIConfig{Title: "Loomplane realtime", Version: buildinfo.Version, SpecEnabled: true, UIEnabled: cfg.IsDevelopment()})),
	)
	for _, ext := range Extensions(cfg) {
		if err := app.RegisterExtension(ext); err != nil {
			return nil, fmt.Errorf("register %s: %w", ext.Name(), err)
		}
	}
	return app, nil
}

// Extensions lists Loomplane's extensions. Each declares what it depends
// on, and Forge orders them from that.
func Extensions(cfg config.Config) []forge.Extension {
	db := groveext.New(
		groveext.WithDSN("postgres", cfg.Database.URL),
		groveext.WithDriverFactory("postgres", postgres(cfg.Database.PoolSize)),
		groveext.WithMigrations(migrations.All()...),
		groveext.WithCentralMigrations(),
		// Grove's CRDT sync routes; Loomplane doesn't use CRDTs.
		groveext.WithDisableRoutes(),
	)

	// The deployment workflow runs here, beside the API, not in the
	// worker: it reads and writes Loomplane's own records and never runs a
	// repository's code. Its handlers go to Dispatch now; the planning
	// extension binds them to the planner when it registers.
	deployments := orchestration.NewDeployments()
	jobs := newJobs(deployments.Options(owner(), cfg.Orchestration.Concurrency),
		dispatchext.WithConcurrency(cfg.Orchestration.Concurrency),
		dispatchext.WithPollInterval(cfg.Orchestration.PollInterval),
		dispatchext.WithQueues([]string{"default", orchestration.Queue}),
		// Followers check for a leader every half lease. A short lease
		// means a leader that stops, a restarted replica or a stopped
		// worker, is replaced in seconds, and the drift check keeps its
		// schedule, for one small write every five seconds.
		dispatchext.WithCronLeaderTTL(apiCronLease),
		// Dispatch's admin API needs a Forge auth registry and, for writes,
		// its durable delivery audit. Loomplane exposes job state through
		// its own API instead.
		dispatchext.WithDisableRoutes(),
		dispatchext.WithDisableMigrate(),
	)
	jobs.WithCapacity(deployments.CapacityOptions(owner(), cfg.Orchestration.Concurrency), deployments.BindCapacityWorker)
	jobs.WithBuild(deployments.CanaryOptions(owner(), cfg.Orchestration.Concurrency), func(w *drt.Worker) { deployments.BindWorker(orchestration.CanaryBuild, w) })

	jobs.WithBuild(deployments.ManagedWorkloadOptions(owner(), cfg.Orchestration.Concurrency), func(w *drt.Worker) { deployments.BindWorker(orchestration.ManagedWorkloadBuild, w) })

	jobs.WithBuild(deployments.ManagedCleanupOptions(owner(), cfg.Orchestration.Concurrency), func(w *drt.Worker) { deployments.BindWorker(orchestration.ManagedCleanupBuild, w) })

	jobs.WithBuild(deployments.RuntimeAccessOptions(owner(), cfg.Orchestration.Concurrency), func(w *drt.Worker) { deployments.BindWorker(orchestration.RuntimeAccessBuild, w) })

	// Provider plugins, described when the first extension that needs
	// them registers.
	plugs := plugins.NewSet(cfg.Providers.Plugins)

	cpAuth := infrastructure.NewAuthProvider()
	ctrl := cpext.New(
		cpext.WithAuthProvider(cpAuth),
		// Ctrl Plane's handlers check the tenant but never authorize the
		// action; Loomplane's API reaches Ctrl Plane in-process instead.
		cpext.WithDisableRoutes(),
		cpext.WithDisableMigrate(),
		// Its scheduler (reconciler, health checks, garbage collection, cert
		// renewal) runs on one API replica, elected through a Postgres
		// advisory lock. Without this, every replica runs every worker.
		cpext.WithLeaderElection(""),
	)

	exts := []forge.Extension{
		newSchemaGuard(cfg),
		db,
		newFiles(cfg),
		notificationsext.NewDelivery(cfg),
		identity.New(cfg),
		notificationsext.New(cfg),
		jobs,
		ctrl,
		projectsext.New(cfg, jobs.Engine),
		discoveryext.New(cfg, jobs.Engine),
		environmentsext.New(cfg),
		connectionsext.New(cfg, plugs),
		cloudinfraext.New(cfg, deployments),
		federationext.New(cfg, ctrl.CtrlPlane),
		realtimeext.New(cfg),
		planningext.New(cfg, jobs.Engine, deployments, ctrl.CtrlPlane, plugs),
		octopusext.New(cfg, jobs.Engine),
		dbgatewayext.New(cfg),
		infraext.New(cpAuth, ctrl.CtrlPlane),
	}
	// The AI section exists only when ai.enabled is on: with it off, no AI
	// route, hook or setting is registered at all.
	if cfg.AI.Enabled {
		exts = append(exts, aiext.New(cfg, jobs.Engine))
	} else {
		// What AI scheduled while it was on stays in the database and keeps
		// firing; this gives those jobs handlers that do nothing, and adds
		// nothing else of the AI section.
		exts = append(exts, aiext.NewOff(jobs.Engine))
	}
	exts = append(exts, console(exts))
	return append(exts, web.New(cfg.Dashboard.Dir, "/api/", cfg.Identity.BasePath+"/", "/_/", "/.well-known/", "/catalog-admin/"))
}

// console is the Forge dashboard's API, mounted for platform admins as the
// instance console. The identity extension signs people in to it, gates
// every request to it and decides which intents it may call.
//
// While it starts, the dashboard asks every other extension for its
// contributors, so it depends on all of them: each has registered and
// started by then.
func console(others []forge.Extension) forge.Extension {
	ext, ok := dashboard.NewExtension(dashboard.WithBasePath(identity.ConsoleBasePath), dashboard.WithEnableAuth(true)).(*dashboard.Extension)
	if !ok {
		panic("dashboard.NewExtension no longer returns *dashboard.Extension")
	}
	deps := make([]string, 0, len(others))
	for _, o := range others {
		deps = append(deps, o.Name())
	}
	ext.SetDependencies(deps)
	return ext
}

// NewWorker builds loomplane-worker: the process that analyzes
// repositories. It works the analysis queue and nothing else, serves only
// health checks, and never migrates; loomplane owns the schema, so start
// it first.
func NewWorker(cfg config.Config) (forge.App, error) {
	app := forge.New(
		forge.WithAppName("loomplane-worker"),
		forge.WithAppVersion(buildinfo.Version),
		forge.WithAppEnvironment(cfg.Environment),
		forge.WithHTTPAddress(cfg.Worker.HTTPAddr),
		forge.WithShutdownTimeout(shutdownTimeout),
		forge.WithEnvPrefix(forgeEnvPrefix),
	)
	for _, ext := range WorkerExtensions(cfg) {
		if err := app.RegisterExtension(ext); err != nil {
			return nil, fmt.Errorf("register %s: %w", ext.Name(), err)
		}
	}
	return app, nil
}

// WorkerExtensions lists loomplane-worker's extensions.
func WorkerExtensions(cfg config.Config) []forge.Extension {
	db := groveext.New(
		groveext.WithDSN("postgres", cfg.Database.URL),
		groveext.WithDriverFactory("postgres", postgres(cfg.Database.PoolSize)),
		groveext.WithDisableRoutes(),
	)
	jobs := dispatchext.New(
		dispatchext.WithConcurrency(cfg.Analysis.Concurrency),
		dispatchext.WithPollInterval(cfg.Orchestration.PollInterval),
		dispatchext.WithQueues([]string{orchestration.AnalysisQueue}),
		dispatchext.WithDisableRoutes(),
		dispatchext.WithDisableMigrate(),
		// The worker schedules nothing, but every Dispatch app competes for
		// cron leadership and none gives it up when it stops, so its lease
		// is short too.
		dispatchext.WithCronLeaderTTL(workerCronLease),
	)
	return []forge.Extension{newSchemaGuard(cfg), db, jobs, discoveryext.NewWorker(cfg, jobs.Engine)}
}

// owner names this process to Dispatch's durable runtime, which leases
// each workflow task to one owner at a time.
func owner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "loomplane"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

// postgres opens the pool with Loomplane's settings. Connections are
// recycled, so a pool behind a proxy or a failover doesn't hold dead ones.
func postgres(poolSize int) groveext.DriverFactory {
	return func(ctx context.Context, dsn string) (grove.GroveDriver, error) {
		ctx, cancel := context.WithTimeout(ctx, openTimeout)
		defer cancel()
		pg := pgdriver.New()
		opts := []groveDriver.Option{
			groveDriver.WithMaxConnLifetime(maxConnLifetime),
			groveDriver.WithMaxConnIdleTime(maxConnIdleTime),
		}
		if poolSize > 0 {
			opts = append(opts, groveDriver.WithPoolSize(poolSize))
		}
		if err := pg.Open(ctx, dsn, opts...); err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		if err := pg.Ping(ctx); err != nil {
			_ = pg.Close() // the ping error is the one worth reporting
			return nil, fmt.Errorf("ping postgres: %w", err)
		}
		return pg, nil
	}
}

func openAPI(cfg config.Config) forge.OpenAPIConfig {
	dev := cfg.IsDevelopment()
	return forge.OpenAPIConfig{
		Title:       "Loomplane",
		Description: "Loomplane's API: organizations' projects, environments and infrastructure.",
		Version:     buildinfo.Version,
		Servers:     []forge.OpenAPIServer{{URL: cfg.PublicURL}},
		Security: map[string]forge.SecurityScheme{
			"session": {Type: "http", Scheme: "bearer", Description: "An authsome session token, sent as a bearer token."},
		},
		SpecEnabled: true,
		SpecPath:    "/openapi.json",
		UIEnabled:   dev,
		UIPath:      "/swagger",
		PrettyJSON:  dev,
	}
}
