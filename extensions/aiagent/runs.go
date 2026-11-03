package aiagent

import (
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"

	dispatchengine "github.com/xraph/dispatch/engine"
)

// registerRuns builds the run driver: it registers its jobs on Dispatch,
// gives cortex's hooks the service, lets turning the agent off cancel its
// runs, counts a release asked for in a chat through the task record, and
// provides the service. The chat kind is registered here; 3c and 3d add
// theirs.
func (e *Extension) registerRuns(c forge.Container, settings *agentsettings.Service, projectSvc *projects.Service) error {
	eng := e.jobs()
	if eng == nil {
		return fmt.Errorf("the agent's runs need the dispatch extension to have registered")
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("the agent's runs need the identity extension's authorizer: %w", err)
	}
	wake, err := forge.InjectType[realtime.WakeSource](c)
	if err != nil {
		return fmt.Errorf("the agent's runs need the realtime wake source: %w", err)
	}
	log := e.Logger()
	runs := agentruns.NewService(agentruns.Deps{
		Store: agentruns.NewPgStore(pgdriver.Unwrap(e.db)), Engine: e.cortex.Engine, Jobs: e.jobs,
		Settings: settings, Binder: e.binder, Registry: e.ops, Authz: authz, Projects: projectSvc,
		Codec: e.codec, Wake: wake,
		Warn: func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	})
	if kerr := runs.RegisterKind(agentruns.ChatSpec()); kerr != nil {
		return kerr
	}
	dispatchengine.Register(eng, agentruns.StartJob(runs))
	dispatchengine.Register(eng, agentruns.ContinueJob(runs))
	dispatchengine.Register(eng, agentruns.SweepJob(runs))
	e.hooks.Service = runs
	e.authz.Requested = runs.Requested
	e.deps.PlanCreated = runs.RecordPlan
	settings.BindCanceller(runs)
	e.runs = runs
	if err = forge.ProvideValue(c, runs); err != nil {
		return fmt.Errorf("provide the agent's runs: %w", err)
	}
	return nil
}
