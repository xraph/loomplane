// Package releaseprep is the Forge extension for release preparation: a
// person asks Loomplane to prepare a production release, a job plans the
// promotion and collects its evidence, and the agent reviews it. The
// extension registers the release_preparation run kind, installs the
// promotion gate in the agent's authorizer, runs the prepare and settle
// jobs, schedules the settle cron and serves the release report routes.
//
// The platform registers it only when ai.enabled is on, after the agent's
// extension. With AI off it doesn't exist: no route, job handler, cron or
// gate, while the AI-off extension answers a job or tick left in Dispatch.
// The release reports table still migrates in the central pass.
package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"reflect"

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
	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releaseprep"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// Name is the extension's name.
const Name = "loomplane-release-preparation"

// settleEvery is how often the settle cron runs.
const settleEvery = "@every 30s"

// The agent's runs refuse a second registration of a kind, and its
// authorizer takes the gate; both are satisfied by 3a's types.
var (
	_ releaseprep.Runs         = (*agentruns.Service)(nil)
	_ releaseprep.Requests     = (*agentrequests.Service)(nil)
	_ releaseprep.Teams        = (*teams.Service)(nil)
	_ releaseprep.PlanStore    = (*planning.PgStore)(nil)
	_ releaseprep.Places       = (*projects.PgStore)(nil)
	_ agentauthz.PromotionGate = (*releaseprep.Gate)(nil)
)

// Extension wires release preparation.
type Extension struct {
	*forge.BaseExtension
	cfg  config.Config
	jobs func() *dispatchengine.Engine
}

// New returns the extension. jobs returns the dispatch extension's engine
// once it has registered; the prepare and settle jobs go there.
func New(cfg config.Config, jobs func() *dispatchengine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Release preparation and the promotion gate"),
		cfg:           cfg,
		jobs:          jobs,
	}
}

// Dependencies implements forge.Extension: the agent, which provides the
// runs, requests, reports, binder, scanner and authorizer, and the
// services a preparation reads.
func (e *Extension) Dependencies() []string {
	return []string{
		identity.Name, aiagent.Name, planningext.Name, notificationsext.Name, projectsext.Name, discoveryext.Name,
	}
}

// sources are what release preparation is built over, as Register finds
// them in the container.
type sources struct {
	pg       *pgdriver.PgDB
	guard    identity.Guard
	authz    *security.Authorizer
	settings *agentsettings.Service
	// reports is *agentreports.Service: the watcher's open reports.
	reports interface {
		Open(ctx context.Context, orgID, projectID, environmentID string) ([]*agentreports.Report, error)
	}
	runs        *agentruns.Service
	requests    *agentrequests.Service
	gate        *agentauthz.Authorizer
	binder      *agentscope.Binder
	scanner     *agentsafety.Scanner
	connections *repositories.Connections
	deployments *deployments.Service
	plans       *planning.Service
	planStore   *planning.PgStore
	watches     *releasewatch.Service
	projects    *projects.Service
	teams       *teams.Service
	audit       projects.Auditor
}

// Register builds the service, registers the run kind, installs the gate
// and the jobs, provides the service and serves the routes. A dependency
// missing from the container fails the start, by name: nothing runs with
// a part quietly left out.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	eng := e.jobs()
	if eng == nil {
		return errors.New("release preparation needs the dispatch extension to have registered")
	}
	c := fapp.Container()
	src, err := e.resolve(c)
	if err != nil {
		return err
	}
	d := deps(e.cfg, src, e.jobs, e.warn)
	if cerr := complete(d); cerr != nil {
		return cerr
	}
	svc := releaseprep.NewService(d)
	if ierr := install(svc, src.runs, src.gate); ierr != nil {
		return ierr
	}
	dispatchengine.Register(eng, releaseprep.PrepareJob(svc))
	dispatchengine.Register(eng, releaseprep.SettleJob(svc))
	if err = forge.ProvideValue(c, svc); err != nil {
		return fmt.Errorf("provide release preparation: %w", err)
	}
	return mountRoutes(fapp.Router(), src.guard, src.binder, svc)
}

// resolve finds what release preparation is built over. With AI on every
// one is there: the agent's extension and the services it waits for
// provide them.
func (e *Extension) resolve(c forge.Container) (sources, error) {
	var src sources
	var errs []error
	need := func(name string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("release preparation needs %s: %w", name, err))
		}
	}
	db, err := forge.InjectType[*grove.DB](c)
	need("the grove extension", err)
	if db != nil {
		src.pg = pgdriver.Unwrap(db)
	}
	src.guard, err = forge.InjectType[identity.Guard](c)
	need("the identity extension's guard", err)
	src.authz, err = forge.InjectType[*security.Authorizer](c)
	need("the authorizer", err)
	src.settings, err = forge.InjectType[*agentsettings.Service](c)
	need("the agent's settings", err)
	reports, err := forge.InjectType[*agentreports.Service](c)
	need("the agent's reports", err)
	if reports != nil {
		src.reports = reports
	}
	src.runs, err = forge.InjectType[*agentruns.Service](c)
	need("the agent's runs", err)
	src.requests, err = forge.InjectType[*agentrequests.Service](c)
	need("the agent's requests", err)
	src.gate, err = forge.InjectType[*agentauthz.Authorizer](c)
	need("the agent's authorizer", err)
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
	src.projects, err = forge.InjectType[*projects.Service](c)
	need("the projects service", err)
	src.teams, err = forge.InjectType[*teams.Service](c)
	need("the teams service", err)
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	need("the audit log", err)
	if audit != nil {
		src.audit = projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger())
	}
	return src, errors.Join(errs...)
}

func (e *Extension) warn(msg string, err error) {
	if log := e.Logger(); log != nil && err != nil {
		log.Warn(msg, forge.F("error", err.Error()))
	}
}

// deps are the service's dependencies over src. Push stays nil until push
// notifications ship an agent mapper; a notice then goes without a push.
// Notify stays nil, so a notice is queued with notifications.Enqueue
// inside the transaction that settles its report.
func deps(cfg config.Config, src sources, jobs func() *dispatchengine.Engine, warn func(string, error)) releaseprep.Deps {
	d := releaseprep.Deps{Jobs: jobs, Warn: warn}
	if src.pg != nil {
		d.Store = releaseprep.NewPgStore(src.pg)
		d.Places = projects.NewPgStore(src.pg)
	}
	set[releaseprep.Runs](&d.Runs, src.runs)
	set[releaseprep.Requests](&d.Requests, src.requests)
	set[releaseprep.Teams](&d.Teams, src.teams)
	set[releaseprep.PlanStore](&d.PlanStore, src.planStore)
	set[projects.Auditor](&d.Audit, src.audit)
	d.Safety, d.Binder = src.scanner, src.binder
	set[releaseprep.Plans](&d.Sources.Plans, src.plans)
	set[releaseprep.Watches](&d.Sources.Watches, src.watches)
	set[releaseprep.Canaries](&d.Sources.Canaries, src.deployments)
	set[releaseprep.Projects](&d.Sources.Projects, src.projects)
	set[releaseprep.Budgets](&d.Sources.Budgets, src.settings)
	set[releaseprep.Permissions](&d.Sources.Authz, src.authz)
	if !isNil(src.reports) {
		d.Sources.Reports = releaseprep.WatcherReports{Reports: src.reports}
	}
	if src.connections != nil {
		d.Sources.Diffs = releaseprep.GitHubDiffs{GitHub: repositories.NewGitHub(cfg.GitHub.APIURL), Tokens: src.connections}
	}
	return d
}

// set assigns v to the interface field only when v isn't nil, so a
// missing dependency stays a nil interface rather than one holding a nil
// pointer.
func set[I any](field *I, v I) {
	if !isNil(v) {
		*field = v
	}
}

// isNil reports whether v is nil, or an interface holding a nil pointer,
// map, slice, func or channel.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// complete refuses a service missing anything it needs. Each of these
// would only show later and quietly: no Teams sends every notice to the
// requester alone, no Places leaves a report whose project is gone open,
// no Binder withholds every concern. Notify must stay nil, so a notice
// never leaves the transaction that settles its report.
func complete(d releaseprep.Deps) error {
	need := map[string]any{
		"the release report store": d.Store, "the agent's runs": d.Runs, "the agent's requests": d.Requests,
		"the job engine": d.Jobs, "the agent's safety scanner": d.Safety, "the agent's scope binder": d.Binder,
		"the teams service": d.Teams, "the planning store": d.PlanStore, "the projects store": d.Places,
		"the audit log": d.Audit, "the planning service": d.Sources.Plans, "the release watches": d.Sources.Watches,
		"the canaries": d.Sources.Canaries, "the projects service": d.Sources.Projects,
		"the agent's settings": d.Sources.Budgets, "the authorizer": d.Sources.Authz,
		"the agent's open reports": d.Sources.Reports, "the commit diffs": d.Sources.Diffs,
	}
	var errs []error
	for name, v := range need {
		if isNil(v) {
			errs = append(errs, fmt.Errorf("release preparation needs %s", name))
		}
	}
	if w, ok := d.Sources.Reports.(releaseprep.WatcherReports); ok && isNil(w.Reports) {
		errs = append(errs, errors.New("release preparation needs the agent's open reports"))
	}
	if g, ok := d.Sources.Diffs.(releaseprep.GitHubDiffs); ok && (g.GitHub == nil || isNil(g.Tokens)) {
		errs = append(errs, errors.New("release preparation needs the commit diffs"))
	}
	if d.Notify != nil {
		errs = append(errs, errors.New("release preparation queues its notices in the settle's own transaction; Notify must be unset"))
	}
	return errors.Join(errs...)
}

// kinds is *agentruns.Service, which registers run kinds.
type kinds interface {
	RegisterKind(spec agentruns.KindSpec) error
}

// gates is *agentauthz.Authorizer, which takes the promotion gate.
type gates interface {
	SetGate(g agentauthz.PromotionGate)
}

// install registers the release_preparation run kind, installs svc's gate
// in the agent's authorizer and makes svc reachable from the agent's
// operations. When the kind won't register, nothing else moves.
func install(svc *releaseprep.Service, runs kinds, authz gates) error {
	if err := runs.RegisterKind(releaseprep.ReleaseSpec()); err != nil {
		return fmt.Errorf("register the release preparation kind: %w", err)
	}
	authz.SetGate(svc.Gate())
	releaseprep.Install(svc)
	return nil
}

// settleCron schedules the settle job. It runs on the default queue,
// which every server works: Dispatch keeps the schedule, and a server
// later run with AI off answers it with the AI-off handler instead of
// piling settles on the agent's queue, which nothing would work.
func settleCron() *cron.Definition[struct{}] {
	return &cron.Definition[struct{}]{Name: releaseprep.CronSettle, JobName: releaseprep.JobSettle, Schedule: settleEvery, Queue: releaseprep.JobQueue}
}

// Start schedules the settle cron. Only here: with AI off this extension
// doesn't exist, and nothing is scheduled.
func (e *Extension) Start(ctx context.Context) error {
	if eng := e.jobs(); eng != nil {
		if err := dispatchengine.RegisterCron(ctx, eng, settleCron()); err != nil {
			return fmt.Errorf("schedule release preparation's settle: %w", err)
		}
	}
	e.MarkStarted()
	return nil
}
