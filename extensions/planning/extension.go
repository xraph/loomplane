// Package planning is the Forge extension for deployment planning: each
// project's manifest, the plans made from it, and the deployment workflow
// that waits for someone to approve one.
//
// The workflow's handlers have to be given to Dispatch when its extension
// is built, before this one exists, so the platform builds them
// (orchestration.NewDeployments) and this extension binds the planner to
// them while it registers. Dispatch takes no work until every extension
// has started.
package planning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/xraph/chronicle"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	connectionsext "github.com/xraph/loomplane/extensions/connections"
	discoveryext "github.com/xraph/loomplane/extensions/discovery"
	environmentsext "github.com/xraph/loomplane/extensions/environments"
	"github.com/xraph/loomplane/extensions/identity"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// Name is the extension's name.
const Name = "loomplane-planning"

// Extension serves the manifest and plan API.
type Extension struct {
	*forge.BaseExtension
	cfg         config.Config
	engineOf    func() *engine.Engine
	deployments *orchestration.Deployments
	manifests   *manifests.Service
	plans       *planning.Service
	deploys     *deployments.Service
	artifacts   *artifacts.Store
}

// New returns the extension. engineOf returns the dispatch extension's
// engine, which starts and signals each plan's workflow; workflow is the
// deployment workflow the platform gave Dispatch.
func New(cfg config.Config, engineOf func() *engine.Engine, workflow *orchestration.Deployments) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Manifests and deployment plans"),
		cfg:           cfg,
		engineOf:      engineOf,
		deployments:   workflow,
	}
}

// Dependencies implements forge.Extension. Discovery owns the analyses a
// plan is made from.
func (e *Extension) Dependencies() []string {
	return []string{
		identity.Name, dispatchext.ExtensionName, projectsext.Name, discoveryext.Name, connectionsext.Name,
		environmentsext.Name,
	}
}

// Register builds the services, binds the workflow and mounts the routes.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("planning needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("planning needs the identity extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("planning needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("planning needs the audit trail: %w", err)
	}
	clouds, err := forge.InjectType[*connections.Service](c)
	if err != nil {
		return fmt.Errorf("planning needs the connections extension: %w", err)
	}
	variables, err := forge.InjectType[*environments.Service](c)
	if err != nil {
		return fmt.Errorf("planning needs the environments extension: %w", err)
	}
	github, err := forge.InjectType[*repositories.Connections](c)
	if err != nil {
		return fmt.Errorf("planning needs the discovery extension: %w", err)
	}
	if e.artifacts, err = artifacts.Open(context.Background(), storageURL(e.cfg)); err != nil {
		return err
	}
	if e.engineOf() == nil {
		return errors.New("planning needs the dispatch extension to have registered")
	}

	pg := pgdriver.Unwrap(db)
	log := e.Logger()
	catalog := providers.Registry()
	auditor := projectsext.NewAuditor(audit, e.cfg.Identity.AppID, log)
	prj := projects.NewPgStore(pg)
	planStore := planning.NewPgStore(pg)
	e.manifests = manifests.NewService(manifests.NewPgStore(pg), prj, authz, auditor)
	e.plans = planning.NewService(planning.Deps{
		Store: planStore, Projects: prj, Analyses: discovery.NewPgStore(pg), Manifests: e.manifests,
		Providers: catalog, Connections: clouds, Authz: authz, Workflow: e.deployments, Audit: auditor,
		Window: e.cfg.Planning.ApprovalWindow,
		Warn:   func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	})
	deployStore := deployments.NewPgStore(pg)
	e.deploys = deployments.NewService(deployStore, authz)
	runner := deployments.NewRunner(deployments.RunnerDeps{
		Store: deployStore, Plans: planStore, Credentials: clouds, Variables: variables,
		Sources: &deployments.GitHubSources{
			GitHub: repositories.NewGitHub(e.cfg.GitHub.APIURL), Tokens: github, Artifacts: e.artifacts,
		},
		Providers: catalog,
		Executors: map[string]deployments.Executor{},
	})
	e.deployments.Bind(e.plans, runner, e.engineOf)
	return e.routes(fapp.Router(), guard)
}

// Start has nothing to start: Dispatch runs the workflow.
func (e *Extension) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

// Stop closes the artifact storage.
func (e *Extension) Stop(ctx context.Context) error {
	if e.artifacts != nil {
		if err := e.artifacts.Close(ctx); err != nil {
			return fmt.Errorf("close artifact storage: %w", err)
		}
	}
	e.MarkStopped()
	return nil
}

// storageURL is the configured artifact storage, or a directory under
// the working directory for development.
func storageURL(cfg config.Config) string {
	if cfg.Storage.URL != "" {
		return cfg.Storage.URL
	}
	dir, err := filepath.Abs(filepath.Join(".loomplane", "storage"))
	if err != nil {
		dir = filepath.Join(os.TempDir(), "loomplane-storage")
	}
	return "file://" + filepath.ToSlash(dir)
}

// fail maps a service error to its HTTP response.
func fail(err error) error {
	switch {
	case errors.Is(err, planning.ErrNotFound), errors.Is(err, projects.ErrNotFound), errors.Is(err, connections.ErrNotFound),
		errors.Is(err, deployments.ErrNotFound),
		errors.Is(err, projects.ErrEnvironmentNotFound), errors.Is(err, manifests.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, planning.ErrConflict), errors.Is(err, manifests.ErrConflict),
		errors.Is(err, planning.ErrStale), errors.Is(err, planning.ErrSuperseded), errors.Is(err, planning.ErrNotReady):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, planning.ErrNoManifest), errors.Is(err, planning.ErrNoAnalysis), errors.Is(err, discovery.ErrNoGraph),
		errors.Is(err, planning.ErrNoSources), errors.Is(err, planning.ErrNeverDeployed):
		return forge.NewHTTPError(http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, planning.ErrInvalid), errors.Is(err, manifests.ErrInvalid), errors.Is(err, manifest.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, planning.ErrUnavailable):
		return forge.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}
