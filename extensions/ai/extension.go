// Package ai is the Forge extension for Loomplane's AI section: the
// workspace AI settings, and the model kinds' catalog hooks and plan
// inputs. The platform registers it only when ai.enabled is on, so with AI
// off none of it exists.
package ai

import (
	"errors"
	"fmt"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/extensions/identity"
	planningext "github.com/xraph/loomplane/extensions/planning"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-ai"

// Extension builds the AI settings and models services and registers the
// model kinds' hooks and plan inputs with the planning extension.
//
// The model crons to come (cost accrual and budget alerts) will need
// Dispatch's engine, which New will then take as the octopus extension's
// does.
type Extension struct {
	*forge.BaseExtension
	cfg      config.Config
	settings *aisettings.Service
	models   *models.Service
	hooks    *models.Hooks
}

// New returns the extension.
func New(cfg config.Config) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "AI settings and models"),
		cfg:           cfg,
	}
}

// Dependencies implements forge.Extension. Identity checks permissions and
// keeps the audit trail; planning provides the catalog hook and input maps
// this extension adds the model kinds to, and the manifests, plans and GPU
// targets models deploy through.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, planningext.Name}
}

// Register builds the settings service over its own Vault app, the
// models service, and the model kinds' hooks and inputs, adds the hooks
// and inputs to planning's maps, and provides both services.
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
	e.hooks = models.NewHooks(models.HooksDeps{Registry: registry.Default(), Custom: modelStore, Settings: e.settings})
	inputs := models.Inputs{Registry: registry.Default(), Custom: modelStore, Settings: e.settings}
	for _, kind := range recipes.ModelKinds() {
		hookMap[kind] = e.hooks
		inputMap[kind] = inputs
	}
	if err := forge.ProvideValue(c, e.settings); err != nil {
		return fmt.Errorf("provide the AI settings: %w", err)
	}
	return nil
}

// registerModels builds the models service over what the planning and
// connections extensions provide, binds it and the Hugging Face client to
// AI settings, has project and environment deletions take their model
// rows, and provides it.
//
// The Hub is huggingface.co. The ai.models.huggingface_url key that points
// it at a mirror comes with the models API.
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
	hub := hfhub.New(hfhub.DefaultURL, nil)
	e.models = models.NewService(models.Deps{
		Registry: registry.Default(), Hub: hub, Settings: e.settings, Custom: store, GPUs: targets, Connections: conns,
		Authz: authz, Store: store, Manifests: mf, Plans: plans, Projects: projects.NewPgStore(pg), TokenGuard: e.settings,
		Audit: auditor,
	})
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
