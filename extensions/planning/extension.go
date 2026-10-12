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
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/ctrlplane/app"
	cpext "github.com/xraph/ctrlplane/extension"
	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	connectionsext "github.com/xraph/loomplane/extensions/connections"
	discoveryext "github.com/xraph/loomplane/extensions/discovery"
	environmentsext "github.com/xraph/loomplane/extensions/environments"
	"github.com/xraph/loomplane/extensions/identity"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/plugins"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
	"github.com/xraph/loomplane/pkg/manifest"
)

// Name is the extension's name.
const Name = "loomplane-planning"

// Extension serves the manifest and plan API.
type Extension struct {
	*forge.BaseExtension
	cfg         config.Config
	engineOf    func() *engine.Engine
	ctrlplane   func() *app.CtrlPlane
	deployments *orchestration.Deployments
	plugins     *plugins.Set
	manifests   *manifests.Service
	plans       *planning.Service
	deploys     *deployments.Service
	artifacts   *artifacts.Store
}

// New returns the extension. engineOf returns the dispatch extension's
// engine, which starts and signals each plan's workflow; workflow is the
// deployment workflow the platform gave Dispatch; ctrlplane is Ctrl
// Plane, which runs Cloud Run services.
func New(cfg config.Config, engineOf func() *engine.Engine, workflow *orchestration.Deployments, ctrlplane func() *app.CtrlPlane, plugs *plugins.Set) *Extension {
	return &Extension{
		plugins:       plugs,
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Manifests and deployment plans"),
		cfg:           cfg,
		engineOf:      engineOf,
		ctrlplane:     ctrlplane,
		deployments:   workflow,
	}
}

// Dependencies implements forge.Extension. Discovery owns the analyses a
// plan is made from.
func (e *Extension) Dependencies() []string {
	return []string{
		identity.Name, dispatchext.ExtensionName, cpext.ExtensionName, projectsext.Name, discoveryext.Name,
		connectionsext.Name, environmentsext.Name,
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
	cp := e.ctrlplane()
	if cp == nil {
		return errors.New("planning needs the ctrlplane extension to have registered")
	}

	pg := pgdriver.Unwrap(db)
	log := e.Logger()
	catalog := providers.Registry()
	metrics := telemetry.New(fapp.Metrics())
	auditor := projectsext.NewAuditor(audit, e.cfg.Identity.AppID, log)
	prj := projects.NewPgStore(pg)
	planStore := planning.NewPgStore(pg)
	e.manifests = manifests.NewService(manifests.NewPgStore(pg), prj, authz, auditor)
	e.plans = planning.NewService(planning.Deps{
		Store: planStore, Projects: prj, Analyses: discovery.NewPgStore(pg), Manifests: e.manifests,
		Providers: catalog, Connections: clouds, Authz: authz, Workflow: e.deployments, Audit: auditor,
		Window: e.cfg.Planning.ApprovalWindow, Telemetry: metrics,
		Warn: func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	})
	deployStore := deployments.NewPgStore(pg)
	e.deploys = deployments.NewService(deployStore, authz, e.deployments, auditor)
	executors := map[string]deployments.Executor{
		"cloud-run": &deployments.CloudRun{
			Google:      gcp.New(e.cfg.Clouds.GoogleEndpoint),
			Ctrl:        deployments.CtrlPlane{Providers: cp.Providers(), Instances: cp.Instances, Deploys: cp.Deploys},
			Credentials: clouds,
		},
		"vercel":    &deployments.Vercel{Client: vercel.New(e.cfg.Clouds.VercelEndpoint)},
		"cloud-sql": &deployments.CloudSQL{Google: gcp.New(e.cfg.Clouds.GoogleEndpoint)},
		"rds":       &deployments.RDS{AWS: aws.New(e.cfg.Clouds.AWSEndpoint)},
		"ecs-fargate": &deployments.ECS{
			AWS:         aws.New(e.cfg.Clouds.AWSEndpoint),
			Ctrl:        deployments.CtrlPlane{Providers: cp.Providers(), Instances: cp.Instances, Deploys: cp.Deploys},
			Credentials: clouds,
		},
	}
	// Provider plugins join the catalog and carry out their own actions.
	plugs, err := e.plugins.Plugins()
	if err != nil {
		return err
	}
	for _, p := range plugs {
		if rerr := catalog.Register(p.Description.Provider); rerr != nil {
			return fmt.Errorf("provider plugin %s: %w", p.Name(), rerr)
		}
		executors[p.Name()] = &deployments.Plugin{Client: p.Client, Every: e.cfg.Orchestration.PollInterval}
	}
	// Resource credentials, such as a database's password, get a Vault
	// key of their own, derived from secret_key.
	resourceKey, err := security.DeriveKey(e.cfg.SecretKey, deployments.ResourceVaultKeyLabel)
	if err != nil {
		return err
	}
	resourceEnc, err := crypto.NewEncryptor(resourceKey)
	if err != nil {
		return fmt.Errorf("vault encryptor: %w", err)
	}
	resourceSecrets := deployments.NewVaultResourceSecrets(
		secret.NewService(vaultpg.New(db), resourceEnc, secret.WithAppID(deployments.ResourceVaultAppID)))
	runner := deployments.NewRunner(deployments.RunnerDeps{
		Store: deployStore, Plans: planStore, Credentials: clouds, Variables: variables,
		Sources: &deployments.GitHubSources{
			GitHub: repositories.NewGitHub(e.cfg.GitHub.APIURL), Tokens: github, Artifacts: e.artifacts,
		},
		Providers: catalog,
		Executors: executors,
		Resources: resourceSecrets,
		Telemetry: metrics,
	})
	e.deployments.Bind(e.plans, runner, e.engineOf)
	// Drift is checked on Dispatch's cron leader, so one replica checks.
	engine.Register(e.engineOf(), orchestration.DriftJob(&deployments.DriftChecker{
		Store: planStore, Credentials: clouds, Executors: executors, Telemetry: metrics,
		Warn: func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	}))
	return e.routes(fapp.Router(), guard)
}

// Start schedules the drift check. Dispatch runs the workflow and the
// check; the schedule needs the database, which is migrated by now.
func (e *Extension) Start(ctx context.Context) error {
	if every := e.cfg.Drift.Interval; every > 0 {
		def := &cron.Definition[orchestration.DriftPayload]{
			Name: orchestration.CronDriftCheck, Schedule: "@every " + every.String(),
			JobName: orchestration.JobDriftCheck, Queue: orchestration.Queue,
		}
		if err := engine.RegisterCron(ctx, e.engineOf(), def); err != nil {
			return fmt.Errorf("schedule the drift check: %w", err)
		}
	}
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
	case errors.Is(err, planning.ErrConflict), errors.Is(err, manifests.ErrConflict), errors.Is(err, deployments.ErrNotFailed),
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

// pluginTimeout bounds asking each provider plugin whether it's there.
const pluginTimeout = 3 * time.Second

// Health reports a provider plugin that doesn't answer: deployments to
// it would fail until it's back.
func (e *Extension) Health(ctx context.Context) error {
	plugs, err := e.plugins.Plugins()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range plugs {
		pctx, cancel := context.WithTimeout(ctx, pluginTimeout)
		if _, derr := p.Describe(pctx); derr != nil {
			errs = append(errs, fmt.Errorf("provider plugin %s doesn't answer: %w", p.Name(), derr))
		}
		cancel()
	}
	return errors.Join(errs...)
}
