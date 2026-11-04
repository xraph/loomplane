package aiagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// reportSources are the services reports are built over, as Register
// finds them in the container.
type reportSources struct {
	pg           *pgdriver.PgDB
	authz        *security.Authorizer
	projects     *projects.Service
	environments *environments.Service
	deployments  *deployments.Service
	connections  *repositories.Connections
	teams        *teams.Service
	audit        projects.Auditor
}

// registerReports builds the agent's reports, provides them, and gives
// them to the report operations. It runs after registerKnowledge: the
// knowledge observer goes on them there.
func (e *Extension) registerReports(c forge.Container, projectSvc *projects.Service) error {
	src, err := e.reportSources(c, projectSvc)
	if err != nil {
		return err
	}
	reports := agentreports.NewService(e.reportDeps(src))
	if err = forge.ProvideValue(c, reports); err != nil {
		return fmt.Errorf("provide the agent's reports: %w", err)
	}
	e.deps.Reports = reports
	e.deps.InvestigationOf = investigationOf(func() runsByRun {
		if e.runs == nil {
			return nil
		}
		return e.runs
	})
	return nil
}

// reportSources finds what reports are built over in the container. Every
// one must be there: with AI on, so is each of them.
func (e *Extension) reportSources(c forge.Container, projectSvc *projects.Service) (reportSources, error) {
	src := reportSources{pg: pgdriver.Unwrap(e.db), projects: projectSvc, audit: e.deps.Audit}
	var errs []error
	resolve := func(name string, fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, fmt.Errorf("the agent's reports need %s: %w", name, err))
		}
	}
	resolve("the authorizer", func() (err error) { src.authz, err = forge.InjectType[*security.Authorizer](c); return })
	resolve("the environments service", func() (err error) {
		src.environments, err = forge.InjectType[*environments.Service](c)
		return
	})
	resolve("the deployments service", func() (err error) {
		src.deployments, err = forge.InjectType[*deployments.Service](c)
		return
	})
	resolve("the GitHub connections", func() (err error) {
		src.connections, err = forge.InjectType[*repositories.Connections](c)
		return
	})
	resolve("the teams service", func() (err error) { src.teams, err = forge.InjectType[*teams.Service](c); return })
	if src.projects == nil || src.audit == nil {
		errs = append(errs, errors.New("the agent's reports need the projects service and the audit log"))
	}
	return src, errors.Join(errs...)
}

// reportDeps are the reports' ports over src. Each one is filled: a
// missing Secrets would drop the known-secret redaction from everything
// reports send.
func (e *Extension) reportDeps(src reportSources) agentreports.Deps {
	log := e.Logger()
	return agentreports.Deps{
		Store:         agentreports.NewPgStore(src.pg),
		DB:            src.pg,
		Authz:         src.authz,
		Projects:      src.projects,
		ProjectReader: projects.NewPgStore(src.pg),
		People:        agentrequests.PgPeople{DB: src.pg, AppID: e.cfg.Identity.AppID},
		Teams:         src.teams,
		Safety:        e.scanner,
		Binder:        e.binder,
		Issues:        src.connections,
		Secrets:       secretValues{env: src.environments},
		Starters:      deploymentStarters{deployments: src.deployments},
		Audit:         src.audit,
		PublicURL:     e.cfg.PublicURL,
		Warn: func(msg string, err error) {
			if log != nil {
				log.Warn(msg, forge.F("error", err.Error()))
			}
		},
	}
}

// secretValues reads a project's secrets for delivery to redact.
type secretValues struct{ env *environments.Service }

func (s secretValues) SecretValues(ctx context.Context, orgID, projectID, environment string) ([]string, error) {
	return s.env.SecretValuesFor(ctx, orgID, projectID, environment)
}

// deploymentStarters names who started a deployment, read as the
// workspace's agent alone: delivery runs without a person.
type deploymentStarters struct{ deployments *deployments.Service }

func (s deploymentStarters) StartedBy(ctx context.Context, orgID, deploymentID string) (string, error) {
	d, err := s.deployments.Get(ctx, security.AgentPrincipal(orgID, nil), deploymentID)
	if err != nil {
		return "", err
	}
	return d.StartedBy, nil
}

// runsByRun is agentruns.Service's task read by run.
type runsByRun interface {
	ByRun(ctx context.Context, orgID, runID string) (*agentruns.Task, error)
}

// investigationOf is agentops.Deps.InvestigationOf over the run driver's
// task records: the task and its report when runID is a report's
// investigation that hasn't finished, else "" and "". A finished task's
// record names its report still, but its run has nothing left to write.
func investigationOf(runs func() runsByRun) func(ctx context.Context, orgID, runID string) (string, string, error) {
	return func(ctx context.Context, orgID, runID string) (string, string, error) {
		r := runs()
		if r == nil || orgID == "" || runID == "" {
			return "", "", nil
		}
		t, err := r.ByRun(ctx, orgID, runID)
		if errors.Is(err, agentruns.ErrNotFound) {
			return "", "", nil
		}
		if err != nil {
			return "", "", err
		}
		if t.OrgID != orgID || t.RunID != runID || t.Kind != agentruns.KindInvestigation || agentruns.Terminal(t.Status) {
			return "", "", nil
		}
		return t.ID, t.Metadata["report_id"], nil
	}
}
