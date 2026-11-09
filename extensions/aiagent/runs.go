package aiagent

import (
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"

	dispatchengine "github.com/xraph/dispatch/engine"
)

// registerRuns builds the run driver: it registers its jobs on Dispatch,
// with the requests' Settle joining its sweep, gives cortex's hooks the
// service, lets turning the agent off cancel its runs (and with them their
// pending requests), counts a release a person named through the task record, and
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
	plans, err := forge.InjectType[*planning.Service](c)
	if err != nil {
		return fmt.Errorf("the agent's runs need the planning service: %w", err)
	}
	wake, err := forge.InjectType[realtime.WakeSource](c)
	if err != nil {
		return fmt.Errorf("the agent's runs need the realtime wake source: %w", err)
	}
	log := e.Logger()
	runs := agentruns.NewService(agentruns.Deps{
		Store: agentruns.NewPgStore(pgdriver.Unwrap(e.db)), Engine: e.cortex.Engine, Jobs: e.jobs,
		Settings: settings, Binder: e.binder, Registry: e.ops, Authz: authz, Projects: projectSvc, Plans: plans,
		Codec: e.codec, Wake: wake,
		Warn: func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
		Debug: func(msg string, keyvals ...any) {
			fields := make([]forge.Field, 0, len(keyvals)/2)
			for i := 0; i+1 < len(keyvals); i += 2 {
				fields = append(fields, forge.F(fmt.Sprint(keyvals[i]), keyvals[i+1]))
			}
			log.Debug(msg, fields...)
		},
	})
	if kerr := runs.RegisterKind(agentruns.ChatSpec()); kerr != nil {
		return kerr
	}
	requests, err := e.registerRequests(c, runs, projectSvc)
	if err != nil {
		return err
	}
	dispatchengine.Register(eng, agentruns.StartJob(runs))
	dispatchengine.Register(eng, agentruns.ContinueJob(runs))
	dispatchengine.Register(eng, agentruns.SweepJob(runs, requests.Settle))
	e.hooks.Service = runs
	e.authz.Requested = runs.Requested
	settings.BindCanceller(runs)
	e.runs = runs
	if err = forge.ProvideValue(c, runs); err != nil {
		return fmt.Errorf("provide the agent's runs: %w", err)
	}
	return nil
}
