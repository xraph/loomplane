package aiagent

import (
	"errors"
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// The real services are what agentrequests asks of them.
var (
	_ agentrequests.Runs        = (*agentruns.Service)(nil)
	_ agentrequests.Plans       = (*planning.Service)(nil)
	_ agentrequests.Projects    = (*projects.Service)(nil)
	_ agentrequests.Variables   = (*environments.Service)(nil)
	_ agentrequests.GitHub      = (*repositories.Connections)(nil)
	_ agentrequests.Connections = (*connections.Service)(nil)
	_ agentrequests.Authorizer  = (*security.Authorizer)(nil)
	_ agentrequests.Teams       = (*teams.Service)(nil)
)

// registerRequests builds the agent's requests over the run driver and
// Loomplane's services, makes it the handler of every pause and finished
// task, and provides it.
func (e *Extension) registerRequests(c forge.Container, runs *agentruns.Service, projectSvc *projects.Service) (*agentrequests.Service, error) {
	d := e.deps
	if d.Plans == nil || d.GitHub == nil || d.Authz == nil || d.Audit == nil {
		return nil, errors.New("the agent's requests need the services its operations use")
	}
	conns, err := forge.InjectType[*connections.Service](c)
	if err != nil {
		return nil, fmt.Errorf("the agent's requests need the connections service: %w", err)
	}
	vars, err := forge.InjectType[*environments.Service](c)
	if err != nil {
		return nil, fmt.Errorf("the agent's requests need the environments service: %w", err)
	}
	teamSvc, err := forge.InjectType[*teams.Service](c)
	if err != nil {
		return nil, fmt.Errorf("the agent's requests need the teams service: %w", err)
	}
	pg := pgdriver.Unwrap(e.db)
	log := e.Logger()
	requests := agentrequests.NewService(agentrequests.Deps{
		Store:  agentrequests.NewPgStore(pg),
		Engine: e.resumer,
		Runs:   runs, Plans: d.Plans, Projects: projectSvc, Variables: vars, GitHub: d.GitHub, Connections: conns,
		Authz: d.Authz, Safety: e.scanner, Teams: teamSvc,
		People: agentrequests.PgPeople{DB: pg, AppID: e.cfg.Identity.AppID},
		DB:     pg, Audit: d.Audit,
		Warn: func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	})
	runs.SetSuspensionHandler(requests)
	if err = forge.ProvideValue(c, requests); err != nil {
		return nil, fmt.Errorf("provide the agent's requests: %w", err)
	}
	e.requests = requests
	return requests, nil
}

// resumer is cortex's engine, once it's built.
func (e *Extension) resumer() agentrequests.Resumer {
	eng := e.cortex.Engine()
	if eng == nil {
		return nil
	}
	return eng
}
