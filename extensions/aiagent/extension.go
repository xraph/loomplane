// Package aiagent is the Forge extension for the AI infra agent. The
// platform registers it, and the libraries it runs on (nexus, shield,
// weave when knowledge is configured, and cortex), only when ai.enabled is
// on. With AI off none of it exists: no route, library, table write or
// call.
//
// The agent's settings live in the AI section (extensions/ai), which
// builds agentsettings.Service; this extension binds nexus and the
// guardrail seeder to it, which lets a workspace turn the agent on.
package aiagent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	cortexext "github.com/xraph/cortex/extension"
	"github.com/xraph/dispatch/cron"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	nexusext "github.com/xraph/nexus/extension"
	shieldext "github.com/xraph/shield/extension"
	weaveext "github.com/xraph/weave/extension"

	aiext "github.com/xraph/loomplane/extensions/ai"
	connectionsext "github.com/xraph/loomplane/extensions/connections"
	discoveryext "github.com/xraph/loomplane/extensions/discovery"
	environmentsext "github.com/xraph/loomplane/extensions/environments"
	"github.com/xraph/loomplane/extensions/identity"
	notificationsext "github.com/xraph/loomplane/extensions/notifications"
	planningext "github.com/xraph/loomplane/extensions/planning"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	realtimeext "github.com/xraph/loomplane/extensions/realtime"
	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/projects"
)

// Name is the extension's name.
const Name = "loomplane-ai-agent"

// Queue is the Dispatch queue agent jobs run on. The platform works it only
// when AI is on.
const Queue = agentruns.Queue

// runLease is how long a worker's lease on a running run lasts before
// another replica may reclaim it (engine.WithRunLease).
const runLease = time.Minute

// sweepEvery is how often the run driver's sweep runs.
const sweepEvery = "@every 15s"

// RunsPerProcess caps the agent runs one process works at once
// (engine.WithRunConcurrency). Cortex's extension leaves it at 0, no
// limit; runs beyond the cap wait in cortex's admission until a slot frees
// or their job times out.
const RunsPerProcess = 4

// Extension runs the agent: it builds the libraries' extensions, binds
// nexus and shield to the agent's settings, and serves the agent's status
// and each project's autonomy.
type Extension struct {
	*forge.BaseExtension
	cfg  config.Config
	jobs func() *dispatchengine.Engine

	nexus  *nexusExtension
	shield *shieldext.Extension
	weave  *knowledgeExtension
	cortex *cortexext.Extension

	// settings is set in Register. The libraries' options read it on each
	// call, and nexus and cortex make none before Start.
	settings atomic.Pointer[agentsettings.Service]
	db       *grove.DB
	binder   *agentscope.Binder
	scanner  *agentsafety.Scanner

	// ops are the agent's operations, registered in New because cortex
	// takes its tools when it builds its engine; Register fills deps, which
	// the operations read at call time.
	ops  *agentops.Registry
	deps *agentops.Deps

	// authz is the agent's risk table, and codec carries a run's principal
	// across a pause. Cortex takes both when it builds its engine, before
	// Register fills their services in; toolAuthorizer is the table
	// layered under shield, bound in Register once shield's engine exists.
	authz          *agentauthz.Authorizer
	codec          *agentauthz.Codec
	toolAuthorizer *lateAuthorizer

	// hooks are cortex's run hooks, which cortex takes when it builds its
	// engine; Register gives them the run driver, runs.
	hooks *agentruns.Hooks
	runs  *agentruns.Service
}

// New returns the extension and builds the libraries' extensions, which
// LibraryExtensions hands to the platform. jobs returns the dispatch
// extension's engine once it has registered; the agent's jobs go there.
func New(cfg config.Config, jobs func() *dispatchengine.Engine) *Extension {
	e := &Extension{
		BaseExtension:  forge.NewBaseExtension(Name, "0.1.0", "The AI infra agent"),
		cfg:            cfg,
		jobs:           jobs,
		ops:            agentops.NewRegistry(),
		deps:           &agentops.Deps{},
		codec:          &agentauthz.Codec{},
		toolAuthorizer: &lateAuthorizer{},
		hooks:          &agentruns.Hooks{},
	}
	e.authz = &agentauthz.Authorizer{Registry: e.ops, ApprovalWindow: cfg.Planning.ApprovalWindow}
	// approve_plan asks the same question as the authorizer just before it
	// approves, through whichever gate is installed then.
	e.deps.Promote = e.authz.Promote
	// The registry's only way in from outside runs the same authorizer
	// cortex does, so the registry the container provides is no bypass.
	e.ops.BindAuthorizer(e.toolAuthorizer)
	registerOps(e.ops, e.deps)
	e.buildLibraries()
	return e
}

// Dependencies implements forge.Extension: Loomplane's services the
// agent reads and acts through, the AI section that provides its
// settings, and the libraries it runs on.
func (e *Extension) Dependencies() []string {
	deps := []string{
		identity.Name, projectsext.Name, discoveryext.Name, environmentsext.Name, connectionsext.Name,
		notificationsext.Name, realtimeext.Name, planningext.Name, aiext.Name,
		nexusext.ExtensionName, shieldext.ExtensionName,
	}
	if e.weave != nil {
		deps = append(deps, weaveext.ExtensionName)
	}
	return append(deps, cortexext.ExtensionName)
}

// agentSettings is the agent's settings, or nil before Register.
func (e *Extension) agentSettings() *agentsettings.Service { return e.settings.Load() }

// Register resolves the agent's settings from the AI section and binds
// nexus and the guardrail seeder to them, so a workspace can turn the
// agent on. It provides the scope binder and the scanner, and serves the
// agent's status and each project's autonomy.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	settings, err := forge.InjectType[*agentsettings.Service](c)
	if err != nil {
		return fmt.Errorf("the agent needs the AI section's settings: %w", err)
	}
	if e.db, err = forge.InjectType[*grove.DB](c); err != nil {
		return fmt.Errorf("the agent needs the grove extension: %w", err)
	}
	projectSvc, err := forge.InjectType[*projects.Service](c)
	if err != nil {
		return fmt.Errorf("the agent needs the projects extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("the agent needs the identity extension's guard: %w", err)
	}
	if e.nexus.Gateway() == nil || e.shield.Engine() == nil || e.cortex.Engine() == nil {
		return errors.New("the agent needs nexus, shield and cortex to have registered first")
	}
	e.settings.Store(settings)
	settings.BindGateway(gatewayAdapter{gw: e.nexus.Gateway}, shieldSeeder{eng: e.shield.Engine})

	log := e.Logger()
	e.binder = &agentscope.Binder{Tenants: settings}
	e.scanner = &agentsafety.Scanner{Engine: e.shield.Engine(), Warn: func(msg string, err error) {
		log.Warn(msg, forge.F("error", err.Error()))
	}}
	if err = forge.ProvideValue(c, e.binder); err != nil {
		return fmt.Errorf("provide the agent's scope binder: %w", err)
	}
	if err = forge.ProvideValue(c, e.scanner); err != nil {
		return fmt.Errorf("provide the agent's safety scanner: %w", err)
	}
	if depsErr := e.fillDeps(c, projectSvc); depsErr != nil {
		return depsErr
	}
	if err = forge.ProvideValue(c, e.ops); err != nil {
		return fmt.Errorf("provide the agent's operations: %w", err)
	}
	if authzErr := e.bindAuthorization(c, settings, projectSvc); authzErr != nil {
		return authzErr
	}
	if runsErr := e.registerRuns(c, settings, projectSvc); runsErr != nil {
		return runsErr
	}
	return mountRoutes(fapp.Router(), guard, settings, projectSvc)
}

// Start records whether this server can give the agent knowledge. The
// library extensions have started by now, and no agent job has run.
func (e *Extension) Start(ctx context.Context) error {
	if s := e.agentSettings(); s != nil {
		s.SetKnowledge(bootKnowledge(ctx, e.cfg.AI, e.db))
	}
	// Only here: with AI off this extension doesn't exist, and nothing is
	// scheduled.
	if eng := e.jobs(); eng != nil {
		sweep := &cron.Definition[struct{}]{Name: agentruns.CronSweep, JobName: agentruns.JobSweep, Schedule: sweepEvery, Queue: Queue}
		if err := dispatchengine.RegisterCron(ctx, eng, sweep); err != nil {
			return fmt.Errorf("schedule the agent's sweep: %w", err)
		}
	}
	e.MarkStarted()
	return nil
}
