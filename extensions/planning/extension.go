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
	"io"
	"net/http"
	"sync"
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
	"github.com/xraph/trove"
	troveext "github.com/xraph/trove/extension"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	cloudinfraext "github.com/xraph/loomplane/extensions/cloudinfra"
	connectionsext "github.com/xraph/loomplane/extensions/connections"
	discoveryext "github.com/xraph/loomplane/extensions/discovery"
	environmentsext "github.com/xraph/loomplane/extensions/environments"
	"github.com/xraph/loomplane/extensions/identity"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	realtimeext "github.com/xraph/loomplane/extensions/realtime"
	"github.com/xraph/loomplane/internal/activity"
	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/render"
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
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
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
	cfg                 config.Config
	engineOf            func() *engine.Engine
	ctrlplane           func() *app.CtrlPlane
	deployments         *orchestration.Deployments
	plugins             *plugins.Set
	manifests           *manifests.Service
	plans               *planning.Service
	deploys             *deployments.Service
	activity            *activity.Service
	artifacts           *artifacts.Store
	backups             *recovery.Service
	nativeBackups       *recovery.NativeService
	restoreTargets      func(context.Context) error
	cloudProfiles       *cloudprofiles.Service
	endpoints           *catalogconnect.Service
	catalogAdmin        *catalogproxy.Service
	endpointChecks      *catalogconnect.Checker
	endpointDialer      endpointDialer
	checkLimit          *checkLimiter // nil is checksPerMinute (see checks)
	checkLimitOnce      sync.Once
	catalogKinds        *recipes.Registry // nil is recipes.Default()
	cloudStore          *cloudprofiles.PgStore
	cloudRecoveryCancel context.CancelFunc
	cloudRecoveryDone   chan struct{}
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
		cloudinfraext.Name, connectionsext.Name, environmentsext.Name, realtimeext.Name, troveext.ExtensionName,
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
	logStore, err := forge.InjectType[*realtime.PgStore](c)
	if err != nil {
		return fmt.Errorf("planning needs the realtime store: %w", err)
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
	files, err := forge.InjectType[*trove.Trove](c)
	if err != nil {
		return fmt.Errorf("planning needs the trove extension: %w", err)
	}
	bucket := e.cfg.Storage.ArtifactBucket
	if bucket == "" {
		bucket = artifacts.Bucket
	}
	e.artifacts = artifacts.New(files, bucket)
	if e.engineOf() == nil {
		return errors.New("planning needs the dispatch extension to have registered")
	}
	cp := e.ctrlplane()
	if cp == nil {
		return errors.New("planning needs the ctrlplane extension to have registered")
	}

	pg := pgdriver.Unwrap(db)
	log := e.Logger()
	catalog := providers.Registry(e.cfg.Providers.Paused...)
	metrics := telemetry.New(fapp.Metrics())
	auditor := projectsext.NewAuditor(audit, e.cfg.Identity.AppID, log)
	prj := projects.NewPgStore(pg)
	planStore := planning.NewPgStore(pg)
	e.cloudStore = cloudprofiles.NewPgStore(pg)
	e.cloudProfiles = cloudprofiles.NewService(cloudprofiles.Deps{Store: e.cloudStore, Projects: prj, Connections: clouds, Providers: catalog, Authz: authz, Audit: auditor})
	e.manifests = manifests.NewService(manifests.NewPgStore(pg), prj, authz, auditor)
	deployStore := deployments.NewPgStore(pg)
	e.deploys = deployments.NewService(deployStore, authz, e.deployments, auditor)
	e.deploys.Projects = prj
	e.activity = activity.NewService(pg, authz)
	executors := map[string]deployments.Executor{
		"cloud-run": &deployments.CloudRun{
			Google:      gcp.New(e.cfg.Clouds.GoogleEndpoint),
			Ctrl:        deployments.CtrlPlane{Providers: cp.Providers(), Instances: cp.Instances, Deploys: cp.Deploys},
			Credentials: clouds,
		},
		"vercel":               &deployments.Vercel{Client: vercel.New(e.cfg.Clouds.VercelEndpoint)},
		"render":               &deployments.Render{Client: render.New(e.cfg.Clouds.RenderEndpoint)},
		"digitalocean":         &deployments.DigitalOcean{Client: digitalocean.New(e.cfg.Clouds.DigitalOceanEndpoint)},
		"azure-container-apps": &deployments.Azure{Client: azure.New(e.cfg.Clouds.AzureEndpoint)},
		"azure-database":       &deployments.AzureDatabase{Client: azure.New(e.cfg.Clouds.AzureEndpoint)},
		"azure-managed-redis":  &deployments.AzureRedis{Client: azure.New(e.cfg.Clouds.AzureEndpoint)},
		"cross-cloud-vpn": &deployments.VPN{
			Google: gcp.New(e.cfg.Clouds.GoogleEndpoint), AWS: aws.New(e.cfg.Clouds.AWSEndpoint), Credentials: clouds,
		},
		"cloud-sql":     &deployments.CloudSQL{Google: gcp.New(e.cfg.Clouds.GoogleEndpoint)},
		"cloud-storage": &deployments.CloudStorage{Google: gcp.New(e.cfg.Clouds.GoogleEndpoint)},
		"memorystore":   &deployments.Memorystore{Google: gcp.New(e.cfg.Clouds.GoogleEndpoint)},
		"rds":           &deployments.RDS{AWS: aws.New(e.cfg.Clouds.AWSEndpoint)},
		"s3":            &deployments.S3{AWS: aws.New(e.cfg.Clouds.AWSEndpoint)},
		"elasticache":   &deployments.ElastiCache{AWS: aws.New(e.cfg.Clouds.AWSEndpoint)},
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
	paused := map[string]bool{}
	for _, name := range e.cfg.Providers.Paused {
		paused[name] = true
	}
	for _, p := range plugs {
		desc := p.Description.Provider
		if paused[desc.Name] {
			providers.Pause(&desc)
		}
		if rerr := catalog.Register(desc); rerr != nil {
			return fmt.Errorf("provider plugin %s: %w", p.Name(), rerr)
		}
		executors[p.Name()] = &deployments.Plugin{Client: p.Client, Every: e.cfg.Orchestration.PollInterval, OperationLogs: p.Description.OperationLogs}
	}
	for name := range paused {
		if _, gerr := catalog.Get(name); gerr != nil {
			return fmt.Errorf("providers.paused names %q, which isn't a target this Loomplane has", name)
		}
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
	// An external endpoint's DSN is a resource credential too.
	e.endpoints = catalogconnect.NewService(catalogconnect.NewPgStore(pg), resourceSecrets, authz)
	e.endpoints.Audit = endpointAuditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: log}
	ctrl := deployments.CtrlPlane{Providers: cp.Providers(), Instances: cp.Instances, Deploys: cp.Deploys}
	routeService, err := forge.InjectType[*cloudinfra.Service](c)
	if err != nil {
		return err
	}
	// Plans check the external endpoints composed resources connect from
	// the target each resource runs on, and each release checks them again.
	checker := &catalogconnect.Checker{Endpoints: e.endpoints, Probers: map[string]catalogconnect.Prober{
		catalogconnect.EnginePostgres: catalogconnect.PostgresProber{}, catalogconnect.EngineRedis: catalogconnect.RedisProber{},
	}}
	e.endpointChecks = checker
	// catalogHooks are what composed catalog kinds do beyond their
	// recipes, by kind. A kind registers its own here.
	catalogHooks := map[string]deployments.CatalogHooks{}
	// observers hear of each service binding a deployment writes or
	// deletes. A kind that tracks its consumers registers its own here.
	observers := []deployments.ReleaseObserver{}
	serverTarget := &deployments.OwnedTarget{RouteGuard: routeService, Cloud: connections.Server, Ctrl: ctrl, Credentials: clouds, Secrets: resourceSecrets, Ports: owned.NewReservations(pg), Hooks: catalogHooks, Endpoints: e.endpoints, Checker: checker}
	// A cluster's endpoint checks dial from Loomplane's own network, so
	// they never reach what Loomplane itself depends on.
	dialGuard := &deployments.DialGuard{Denied: e.cfg.Planning.EndpointDeniedCIDRs, Services: loomplaneServices(e.cfg)}
	clusterTarget := &deployments.OwnedTarget{Cloud: connections.Kubernetes, Ctrl: ctrl, Credentials: clouds, Secrets: resourceSecrets, Controllers: kubernetes.NewControllerStore(pg), Hooks: catalogHooks, Endpoints: e.endpoints, Checker: checker, DialGuard: dialGuard}
	serverTarget.Bindings, clusterTarget.Bindings = planStore.ListBindings, planStore.ListBindings
	executors[connections.Server], executors[connections.Kubernetes] = serverTarget, clusterTarget
	dialer := ownedDialer{credentials: clouds, targets: map[string]*deployments.OwnedTarget{connections.Server: serverTarget, connections.Kubernetes: clusterTarget}}
	e.endpointDialer = dialer
	// A kind's own API reaches its running components and generated
	// secrets through CatalogAccess, which it resolves from the container.
	catalogAccess := deployments.NewCatalogAccess(deployments.CatalogAccessDeps{Authz: authz, Bindings: planStore.ListBindings, Executors: executors, Secrets: resourceSecrets})
	if err := forge.ProvideValue(c, catalogAccess); err != nil {
		return err
	}
	// A kind's admin UI opens through Loomplane's sandboxed proxy, which
	// reaches the component over the same private channel. The proxy is
	// mounted outside the identity guard: a session's token, in its path,
	// is the credential.
	e.catalogAdmin = catalogproxy.NewService(catalogproxy.Deps{
		Store: catalogproxy.NewPgStore(pg), Access: catalogAccess, Authz: authz, Bindings: planStore.ListBindings,
		Warn: func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	})
	if err := forge.ProvideValue(c, e.catalogAdmin); err != nil {
		return err
	}
	if err := fapp.Router().Handle(catalogAdminRoute, e.catalogAdmin.Handler()); err != nil {
		return fmt.Errorf("mount the catalog admin proxy: %w", err)
	}
	e.plans = planning.NewService(planning.Deps{
		Store: planStore, Projects: prj, Analyses: discovery.NewPgStore(pg), Manifests: e.manifests,
		CloudProfiles: e.cloudStore, CloudSyncWorkflow: e.deployments,
		Providers: catalog, Connections: clouds, Authz: authz, Workflow: e.deployments, Audit: auditor,
		Window: e.cfg.Planning.ApprovalWindow, Telemetry: metrics,
		Warn:      func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
		Endpoints: checker,
		Dialer:    dialer,
	})
	if err := e.setupBackups(fapp, pg, prj, planStore, authz, recovery.Targets{connections.Server: serverTarget, connections.Kubernetes: clusterTarget}, &deployments.NativeDatabases{AWS: aws.New(e.cfg.Clouds.AWSEndpoint), Credentials: clouds}, auditor); err != nil {
		return err
	}
	e.restoreTargets = func(ctx context.Context) error {
		// Restore provider keys after central migrations have run.
		rows, err := pg.Query(ctx, "SELECT org_id,id,cloud FROM loomplane_cloud_connections WHERE cloud IN ('server','kubernetes')")
		if err != nil {
			return fmt.Errorf("restore owned target providers: %w", err)
		}
		for rows.Next() {
			var org, connection, cloud string
			if err = rows.Scan(&org, &connection, &cloud); err != nil {
				rows.Close()
				return err
			}
			if cloud == connections.Server {
				serverTarget.Register(org, connection)
			} else {
				clusterTarget.Register(org, connection)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		return nil
	}
	runner := deployments.NewRunner(deployments.RunnerDeps{
		Logs: logStore, Routes: routeService,
		CloudProfiles: e.cloudStore, CloudSyncAuth: e.plans,
		Store: deployStore, Plans: planStore, Credentials: clouds, Variables: variables,
		Sources: &deployments.GitHubSources{
			GitHub: repositories.NewGitHub(e.cfg.GitHub.APIURL), Tokens: github, Artifacts: e.artifacts,
		},
		Providers:    catalog,
		Executors:    executors,
		Resources:    resourceSecrets,
		Telemetry:    metrics,
		CatalogHooks: catalogHooks,
		Observers:    observers,
	})
	if err := e.poolRecoveryRoutes(fapp.Router(), guard, cloudinfra.NewService(cloudinfra.Deps{Store: cloudinfra.NewPgStore(pg), Clouds: e.cloudStore, Authz: authz, PoolCleanup: runner})); err != nil {
		return err
	}
	e.plans.BindCloudSyncRunner(runner, runner)
	e.deployments.Bind(e.plans, runner, e.engineOf)
	e.deployments.BindCloudSync(e.plans)
	// Drift is checked on Dispatch's cron leader, so one replica checks.
	engine.Register(e.engineOf(), orchestration.DriftJob(&deployments.DriftChecker{
		Store: planStore, Credentials: clouds, Executors: executors, Telemetry: metrics,
		Warn: func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	}))
	if err := e.backupRoutes(fapp.Router(), guard); err != nil {
		return err
	}
	if err := e.cloudRoutes(fapp.Router(), guard); err != nil {
		return err
	}
	return e.routes(fapp.Router(), guard)
}

// Start schedules the drift check. Dispatch runs the workflow and the
// check; the schedule needs the database, which is migrated by now.
func (e *Extension) Start(ctx context.Context) error {
	if e.restoreTargets != nil {
		if err := e.restoreTargets(ctx); err != nil {
			return err
		}
	}
	if every := e.cfg.Drift.Interval; every > 0 {
		def := &cron.Definition[orchestration.DriftPayload]{
			Name: orchestration.CronDriftCheck, Schedule: "@every " + every.String(),
			JobName: orchestration.JobDriftCheck, Queue: orchestration.Queue,
		}
		if err := engine.RegisterCron(ctx, e.engineOf(), def); err != nil {
			return fmt.Errorf("schedule the drift check: %w", err)
		}
	}
	if err := e.scheduleBackupReconciliation(ctx); err != nil {
		return err
	}
	if err := e.startCloudRecovery(ctx, 30*time.Second); err != nil {
		return err
	}
	e.MarkStarted()
	return nil
}

// Stop closes the artifact storage.
func (e *Extension) Stop(ctx context.Context) error {
	if e.cloudRecoveryCancel != nil {
		e.cloudRecoveryCancel()
		select {
		case <-e.cloudRecoveryDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if e.artifacts != nil {
		if err := e.artifacts.Close(ctx); err != nil {
			return fmt.Errorf("close artifact storage: %w", err)
		}
	}
	e.MarkStopped()
	return nil
}

// ownedDialer dials from the server or cluster a connection belongs to.
type ownedDialer struct {
	credentials deployments.Credentials
	targets     map[string]*deployments.OwnedTarget
}

// endpointDialer dials from a target for an endpoint check, and says
// which cloud the target is.
type endpointDialer interface {
	planning.TargetDialer
	TargetCloud(ctx context.Context, orgID, connectionID string) (string, error)
}

var _ endpointDialer = ownedDialer{}

// TargetCloud is the cloud of a target connection: "server" or
// "kubernetes".
func (d ownedDialer) TargetCloud(ctx context.Context, orgID, connectionID string) (string, error) {
	c, _, err := d.credentials.Credential(ctx, orgID, connectionID)
	if err != nil {
		return "", err
	}
	return c.Cloud, nil
}

func (d ownedDialer) TargetDial(ctx context.Context, orgID, connectionID string) (catalogconnect.DialFunc, io.Closer, error) {
	c, _, err := d.credentials.Credential(ctx, orgID, connectionID)
	if err != nil {
		return nil, nil, err
	}
	target, ok := d.targets[c.Cloud]
	if !ok {
		return nil, nil, fmt.Errorf("endpoints are checked from a server or Kubernetes target, and %s is a %s connection", c.Name, c.Cloud)
	}
	return target.TargetDial(ctx, orgID, connectionID)
}

// fail maps a service error to its HTTP response.
func fail(err error) error {
	switch {
	case errors.Is(err, cloudprofiles.ErrNotFound), errors.Is(err, recovery.ErrNotFound), errors.Is(err, catalogconnect.ErrNotFound), errors.Is(err, planning.ErrNotFound), errors.Is(err, projects.ErrNotFound), errors.Is(err, connections.ErrNotFound),
		errors.Is(err, deployments.ErrNotFound), errors.Is(err, deployments.ErrComponentNotFound), errors.Is(err, catalogproxy.ErrNotFound),
		errors.Is(err, projects.ErrEnvironmentNotFound), errors.Is(err, manifests.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, cloudprofiles.ErrConflict), errors.Is(err, cloudprofiles.ErrExists), errors.Is(err, cloudprofiles.ErrInUse), errors.Is(err, cloudprofiles.ErrBusy), errors.Is(err, recovery.ErrConflict), errors.Is(err, planning.ErrConflict), errors.Is(err, manifests.ErrConflict), errors.Is(err, deployments.ErrNotFailed),
		errors.Is(err, planning.ErrStale), errors.Is(err, planning.ErrSuperseded), errors.Is(err, planning.ErrNotReady),
		errors.Is(err, catalogconnect.ErrInUse):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, planning.ErrNoManifest), errors.Is(err, planning.ErrNoAnalysis), errors.Is(err, discovery.ErrNoGraph),
		errors.Is(err, planning.ErrNoSources), errors.Is(err, planning.ErrNeverDeployed):
		return forge.NewHTTPError(http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, cloudprofiles.ErrInvalid), errors.Is(err, recovery.ErrInvalid), errors.Is(err, planning.ErrInvalid), errors.Is(err, manifests.ErrInvalid), errors.Is(err, manifest.ErrInvalid),
		errors.Is(err, catalogconnect.ErrInvalid), errors.Is(err, deployments.ErrNotControlPort), errors.Is(err, catalogproxy.ErrInvalid):
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
