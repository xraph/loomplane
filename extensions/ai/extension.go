// Package ai is the Forge extension for Loomplane's AI section: the
// workspace AI settings, the model kinds' catalog hooks and plan inputs,
// and the models API. The platform registers it only when ai.enabled is
// on, so with AI off none of it exists, routes included.
package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	cloudinfraext "github.com/xraph/loomplane/extensions/cloudinfra"
	"github.com/xraph/loomplane/extensions/identity"
	notificationsext "github.com/xraph/loomplane/extensions/notifications"
	planningext "github.com/xraph/loomplane/extensions/planning"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-ai"

// JobReconcileModels carries on the model deployments waiting for a GPU
// server, and tidies the ones a deploy stopped writing. Its cron runs it
// every minute.
const JobReconcileModels = "ai-models.reconcile"

// reconcileModelsEvery is how often the reconcile runs.
const reconcileModelsEvery = "@every 1m"

// reconcilePayload is the reconcile job's input: there is none.
type reconcilePayload struct{}

// JobAccrueModels adds what each GPU model cost since its last run to its
// month and sends the budget notices that are due. Its cron runs it every
// five minutes.
const JobAccrueModels = "ai-models.accrue"

// accrueModelsEvery is how often the accrual runs.
const accrueModelsEvery = "@every 5m"

// accruePayload is the accrual job's input: there is none.
type accruePayload struct{}

// Extension builds the AI settings and models services and registers the
// model kinds' hooks and plan inputs with the planning extension, and the
// model reconcile with Dispatch.
type Extension struct {
	*forge.BaseExtension
	cfg      config.Config
	engineOf func() *engine.Engine
	settings *aisettings.Service
	models   *models.Service
	hooks    *models.Hooks
	engine   *engine.Engine
}

// New returns the extension. engineOf returns the dispatch extension's
// engine once it has registered.
func New(cfg config.Config, engineOf func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "AI settings and models"),
		cfg:           cfg,
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension. Identity checks permissions and
// keeps the audit trail; planning provides the catalog hook and input maps
// this extension adds the model kinds to, and the manifests, plans, GPU
// targets and cloud assignments models deploy through; cloudinfra creates
// the GPU servers a model needs first; Dispatch runs the reconcile.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, dispatchext.ExtensionName, cloudinfraext.Name, planningext.Name, notificationsext.DeliveryName}
}

// Register builds the settings service over its own Vault app, the
// models service, and the model kinds' hooks and inputs, adds the hooks
// and inputs to planning's maps, provides both services, and serves the
// models API.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("ai needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("ai needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("ai needs the identity extension's audit trail: %w", err)
	}
	hookMap, err := forge.InjectType[deployments.CatalogHookMap](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's catalog hooks: %w", err)
	}
	inputMap, err := forge.InjectType[planning.CatalogInputMap](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's catalog inputs: %w", err)
	}
	if hookMap == nil || inputMap == nil {
		return errors.New("ai needs the planning extension's catalog hook and input maps")
	}

	// Provider keys and the Hugging Face token live in a Vault app of
	// their own, under a key derived from secret_key that nothing else
	// uses.
	key, err := security.DeriveKey(e.cfg.SecretKey, aisettings.VaultKeyLabel)
	if err != nil {
		return err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return fmt.Errorf("vault encryptor: %w", err)
	}
	vault := secret.NewService(vaultpg.New(db), enc, secret.WithAppID(aisettings.VaultAppID))
	pg := pgdriver.Unwrap(db)
	e.settings = aisettings.NewService(aisettings.Deps{
		Store: aisettings.NewPgStore(pg), Keys: aisettings.NewVaultKeys(vault),
		HuggingFace: aisettings.NewVaultHuggingFaceTokens(vault),
		Authz:       authz, Audit: projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()),
		Projects: projects.NewPgStore(pg),
	})

	modelStore := models.NewPgStore(pg)
	if err := e.registerModels(c, authz, projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()), modelStore, pg); err != nil {
		return err
	}

	// Until the AI gateway lands, the hooks tell no gateway. Models pasted
	// from Hugging Face are found in the workspace's stored entries.
	hub := e.cfg.AI.Models.HuggingFaceURL
	e.hooks = models.NewHooks(models.HooksDeps{Registry: registry.Default(), Custom: modelStore, Settings: e.settings, HuggingFaceURL: hub})
	inputs := models.Inputs{Registry: registry.Default(), Custom: modelStore, Settings: e.settings, HuggingFaceURL: hub}
	for _, kind := range recipes.ModelKinds() {
		hookMap[kind] = e.hooks
		inputMap[kind] = inputs
	}
	if err := forge.ProvideValue(c, e.settings); err != nil {
		return fmt.Errorf("provide the AI settings: %w", err)
	}
	guard, gerr := forge.InjectType[identity.Guard](c)
	if gerr != nil {
		return fmt.Errorf("ai needs the identity extension's guard: %w", gerr)
	}
	return mountModels(fapp.Router(), guard, e.models, e.settings)
}

// registerModels builds the models service over what the planning and
// connections extensions provide, binds it and the Hugging Face client to
// AI settings, has project and environment deletions take their model
// rows, and provides it.
//
// The Hub is ai.models.huggingface_url, huggingface.co unless the operator
// points it at a mirror. Config refuses one that isn't https, except on a
// loopback host, since the workspace's token is sent there.
func (e *Extension) registerModels(c forge.Container, authz *security.Authorizer, auditor projects.Auditor, store *models.PgStore, pg *pgdriver.PgDB) error {
	mf, err := forge.InjectType[*manifests.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's manifests: %w", err)
	}
	plans, err := forge.InjectType[*planning.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's plans: %w", err)
	}
	targets, err := forge.InjectType[*gpus.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's GPU targets: %w", err)
	}
	conns, err := forge.InjectType[*connections.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the connections extension: %w", err)
	}
	projectSvc, err := forge.InjectType[*projects.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the projects extension: %w", err)
	}
	servers, err := forge.InjectType[*cloudinfra.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the cloud infrastructure extension: %w", err)
	}
	clouds, err := forge.InjectType[*cloudprofiles.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's clouds: %w", err)
	}
	keys, err := forge.InjectType[security.PrincipalVerifier](c)
	if err != nil {
		return fmt.Errorf("ai needs the identity extension's API keys: %w", err)
	}
	notices, err := forge.InjectType[*notifications.Service](c)
	if err != nil {
		return fmt.Errorf("ai needs the notifications extension: %w", err)
	}
	access, err := forge.InjectType[*deployments.CatalogAccess](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's catalog access: %w", err)
	}
	resourceSecrets, err := forge.InjectType[*deployments.VaultResourceSecrets](c)
	if err != nil {
		return fmt.Errorf("ai needs the planning extension's resource secrets: %w", err)
	}
	// The engine comes from the dispatch extension itself, as planning's
	// does. Resolving it from the container instead starts it, before the
	// database is migrated, and the dispatch extension then starts it a
	// second time, leaking the first wake listener's connection so the
	// process never finishes stopping.
	if e.engineOf != nil {
		e.engine = e.engineOf()
	}
	if e.engine == nil {
		return errors.New("ai needs the dispatch extension's engine")
	}
	hub := hfhub.New(e.cfg.AI.Models.HuggingFaceURL, nil)
	planStore, runStore := planning.NewPgStore(pg), deployments.NewPgStore(pg)
	deployed := &models.DeployedFacts{
		ListBindings: planStore.ListBindings, ListPlans: planStore.ListPlans, ListRuns: runStore.ListDeployments,
		Secrets: resourceSecrets, Generated: access, ListConnections: connections.NewPgStore(pg).ListConnections,
	}
	e.models = models.NewService(models.Deps{
		Registry: registry.Default(), Hub: hub, Settings: e.settings, Custom: store, GPUs: targets, Connections: conns,
		Authz: authz, Store: store, Manifests: mf, Plans: plans, Projects: projects.NewPgStore(pg), TokenGuard: e.settings,
		Audit: auditor, Servers: servers, Clouds: clouds, Waiting: store, Principals: keys, Deployed: deployed,
		Prices: targets, Budget: notices,
		Warn: func(msg string, err error) { e.Logger().Warn(msg, forge.F("error", err.Error())) },
	})
	// The reconcile is the one caller of the store's cross-organization
	// read, through Reconcile.
	svc := e.models
	engine.Register(e.engine, job.NewDefinition(JobReconcileModels, func(ctx context.Context, _ reconcilePayload) error {
		return svc.Reconcile(ctx)
	}, job.WithMaxRetries(0)))
	// So is the accrual, through Accrue, which reads only organization IDs
	// across organizations and each one's rows on their own.
	engine.Register(e.engine, job.NewDefinition(JobAccrueModels, func(ctx context.Context, _ accruePayload) error {
		return svc.Accrue(ctx, time.Now())
	}, job.WithMaxRetries(0)))
	e.settings.SetTokenChecker(hub)
	e.settings.SetGatedModels(e.models)
	log := e.Logger()
	projectSvc.AddReaper(&models.Reaper{Store: store, Warn: func(msg string, err error) {
		log.Warn(msg, forge.F("error", err.Error()))
	}})
	if err := forge.ProvideValue(c, e.models); err != nil {
		return fmt.Errorf("provide the models service: %w", err)
	}
	return nil
}

// Start schedules the model reconcile and the accrual, which run on the
// cron leader only. The schedules need the database, which is migrated by
// now.
func (e *Extension) Start(ctx context.Context) error {
	def := &cron.Definition[reconcilePayload]{
		Name: JobReconcileModels, JobName: JobReconcileModels, Schedule: reconcileModelsEvery, Queue: orchestration.Queue,
	}
	if err := engine.RegisterCron(ctx, e.engine, def); err != nil {
		return fmt.Errorf("schedule the model reconcile: %w", err)
	}
	accrue := &cron.Definition[accruePayload]{
		Name: JobAccrueModels, JobName: JobAccrueModels, Schedule: accrueModelsEvery, Queue: orchestration.Queue,
	}
	if err := engine.RegisterCron(ctx, e.engine, accrue); err != nil {
		return fmt.Errorf("schedule the model accrual: %w", err)
	}
	e.MarkStarted()
	return nil
}
