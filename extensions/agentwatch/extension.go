// Package agentwatch is the Forge extension for the agent's watcher: it
// runs the watcher's jobs, schedules its tick, registers the
// investigation kind and serves the reports and the watcher's status. The
// platform registers it only when ai.enabled is on, after the agent's
// extension; with AI off nothing watches a workspace and no report route
// exists, while the AI-off extension answers a tick left in Dispatch.
package agentwatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/chronicle"
	"github.com/xraph/dispatch/cron"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/extensions/aiagent"
	discoveryext "github.com/xraph/loomplane/extensions/discovery"
	"github.com/xraph/loomplane/extensions/identity"
	notificationsext "github.com/xraph/loomplane/extensions/notifications"
	planningext "github.com/xraph/loomplane/extensions/planning"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	realtimeext "github.com/xraph/loomplane/extensions/realtime"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	watch "github.com/xraph/loomplane/internal/agentwatch"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-agent-watch"

// Extension runs the agent's watcher.
type Extension struct {
	*forge.BaseExtension
	cfg  config.Config
	jobs func() *dispatchengine.Engine
}

// New returns the extension. jobs returns the dispatch extension's engine
// once it has registered; the watcher's jobs and its tick go there.
func New(cfg config.Config, jobs func() *dispatchengine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "The AI agent's watcher and reports"),
		cfg:           cfg,
		jobs:          jobs,
	}
}

// Dependencies implements forge.Extension: the agent, which provides the
// reports, the runs, the binder and the scanner, and the services the
// watcher reads.
func (e *Extension) Dependencies() []string {
	return []string{
		identity.Name, aiagent.Name, planningext.Name, discoveryext.Name, notificationsext.Name, realtimeext.Name,
	}
}

// sources are what the watcher is built over, as Register finds them in
// the container.
type sources struct {
	db          *grove.DB
	guard       identity.Guard
	authz       *security.Authorizer
	settings    *agentsettings.Service
	reports     *agentreports.Service
	runs        *agentruns.Service
	binder      *agentscope.Binder
	scanner     *agentsafety.Scanner
	connections *repositories.Connections
	deployments *deployments.Service
	plans       *planning.Service
	planStore   *planning.PgStore
	watches     *releasewatch.Service
	health      releasehealth.Reader
	projects    *projects.Service
	discovery   *discovery.Service
	events      *realtime.PgStore
	audit       *chronicle.Chronicle
}

// Register builds the watcher, registers the investigation kind and the
// watcher's jobs, provides the watcher and serves the reports.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	eng := e.jobs()
	if eng == nil {
		return errors.New("the agent's watcher needs the dispatch extension to have registered")
	}
	c := fapp.Container()
	src, err := resolve(c)
	if err != nil {
		return err
	}
	w := e.watcher(src, eng.WorkerID().String())
	if err = src.runs.RegisterKind(watch.InvestigationSpec()); err != nil {
		return fmt.Errorf("register the investigation kind: %w", err)
	}
	dispatchengine.Register(eng, watch.TickJob(w))
	dispatchengine.Register(eng, watch.OrgJob(w))
	dispatchengine.Register(eng, watch.DeliverJob(src.reports, w.Owner))
	if err = forge.ProvideValue(c, w); err != nil {
		return fmt.Errorf("provide the agent's watcher: %w", err)
	}
	return mountRoutes(fapp.Router(), src.guard, src.binder, src.reports, w)
}

// resolve finds what the watcher is built over. With AI on every one is
// there: the agent's extension and the services it waits for provide them.
func resolve(c forge.Container) (sources, error) {
	var src sources
	var errs []error
	need := func(name string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("the agent's watcher needs %s: %w", name, err))
		}
	}
	var err error
	src.db, err = forge.InjectType[*grove.DB](c)
	need("the grove extension", err)
	src.guard, err = forge.InjectType[identity.Guard](c)
	need("the identity extension's guard", err)
	src.authz, err = forge.InjectType[*security.Authorizer](c)
	need("the authorizer", err)
	src.settings, err = forge.InjectType[*agentsettings.Service](c)
	need("the agent's settings", err)
	src.reports, err = forge.InjectType[*agentreports.Service](c)
	need("the agent's reports", err)
	src.runs, err = forge.InjectType[*agentruns.Service](c)
	need("the agent's runs", err)
	src.binder, err = forge.InjectType[*agentscope.Binder](c)
	need("the agent's scope binder", err)
	src.scanner, err = forge.InjectType[*agentsafety.Scanner](c)
	need("the agent's safety scanner", err)
	src.connections, err = forge.InjectType[*repositories.Connections](c)
	need("the GitHub connections", err)
	src.deployments, err = forge.InjectType[*deployments.Service](c)
	need("the deployments service", err)
	src.plans, err = forge.InjectType[*planning.Service](c)
	need("the planning service", err)
	src.planStore, err = forge.InjectType[*planning.PgStore](c)
	need("the planning store", err)
	src.watches, err = forge.InjectType[*releasewatch.Service](c)
	need("the release watches", err)
	src.health, err = forge.InjectType[releasehealth.Reader](c)
	need("the release health reader", err)
	src.projects, err = forge.InjectType[*projects.Service](c)
	need("the projects service", err)
	src.discovery, err = forge.InjectType[*discovery.Service](c)
	need("the discovery service", err)
	src.events, err = forge.InjectType[*realtime.PgStore](c)
	need("the realtime store", err)
	src.audit, err = forge.InjectType[*chronicle.Chronicle](c)
	need("the audit log", err)
	return src, errors.Join(errs...)
}

// watcher builds the watcher over src, as replica owner: detectors read
// through the services as the workspace's agent alone, and what a pass
// skips is logged.
func (e *Extension) watcher(src sources, owner string) *watch.Watcher {
	log := e.Logger()
	warn := warnTo(log)
	pg := pgdriver.Unwrap(src.db)
	world := &watch.ServiceWorld{
		Deployments: src.deployments, Plans: src.plans, PlanStore: src.planStore, Watches: src.watches,
		Projects: src.projects, Discovery: src.discovery, SourceStore: discovery.NewPgStore(pg), Bindings: src.planStore,
	}
	return &watch.Watcher{
		Store:    watch.NewPgStore(pg),
		Events:   src.events,
		Detector: &watch.Detector{World: world, Health: src.health, Warn: warn},
		Reports:  src.reports,
		Budget:   src.settings,
		Runs:     src.runs,
		Safety:   src.scanner,
		Binder:   src.binder,
		DB:       pg,
		Jobs:     watch.DispatchEnqueuer{Engine: e.jobs, Queue: agentruns.SweepQueue},
		Audit:    projectsext.NewAuditor(src.audit, e.cfg.Identity.AppID, log),
		Issues:   src.connections,
		Authz:    src.authz,
		Owner:    owner,
		Warn:     warn,
	}
}

// warnTo is the watcher's Warn: it logs what a pass skipped to log, and
// ignores a nil error.
func warnTo(log forge.Logger) func(msg string, err error) {
	return func(msg string, err error) {
		if log != nil && err != nil {
			log.Warn(msg, forge.F("error", err.Error()))
		}
	}
}

// tickCron schedules the watcher's tick. It runs on the default queue,
// which every server works: Dispatch keeps the schedule, and a server
// later run with AI off answers it with the AI-off handler instead of
// piling ticks on the agent's queue, which nothing would work.
func tickCron() *cron.Definition[struct{}] {
	return &cron.Definition[struct{}]{Name: watch.CronTick, JobName: watch.JobTick, Schedule: watch.TickSchedule, Queue: agentruns.SweepQueue}
}

// Start schedules the watcher's tick. Only here: with AI off this
// extension doesn't exist, and nothing is scheduled.
func (e *Extension) Start(ctx context.Context) error {
	if eng := e.jobs(); eng != nil {
		if err := dispatchengine.RegisterCron(ctx, eng, tickCron()); err != nil {
			return fmt.Errorf("schedule the agent's watcher: %w", err)
		}
	}
	e.MarkStarted()
	return nil
}
