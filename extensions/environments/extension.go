// Package environments is the Forge extension for environment
// configuration: organization, project and service variables, with
// secrets kept in Vault.
package environments

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	discoveryext "github.com/xraph/loomplane/extensions/discovery"
	"github.com/xraph/loomplane/extensions/identity"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-environments"

// Extension serves the variable API.
type Extension struct {
	*forge.BaseExtension
	cfg config.Config
	svc *environments.Service
}

// New returns the extension.
func New(cfg config.Config) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Environment variables and secrets"),
		cfg:           cfg,
	}
}

// Dependencies implements forge.Extension. The projects extension provides
// the service this one cleans up after.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, projectsext.Name, discoveryext.Name}
}

// Register builds the service over Vault and mounts the routes.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("environments needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("environments needs the identity extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("environments needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("environments needs the audit trail: %w", err)
	}
	projectSvc, err := forge.InjectType[*projects.Service](c)
	if err != nil {
		return fmt.Errorf("environments needs the projects extension: %w", err)
	}

	// Vault encrypts with a key of its own, derived from secret_key, so
	// it shares no key material with authsome or install states.
	key, err := security.DeriveKey(e.cfg.SecretKey, environments.VaultKeyLabel)
	if err != nil {
		return err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return fmt.Errorf("vault encryptor: %w", err)
	}
	secrets := environments.NewVaultSecrets(secret.NewService(vaultpg.New(db), enc, secret.WithAppID(environments.VaultAppID)))

	pg := pgdriver.Unwrap(db)
	log := e.Logger()
	e.svc = environments.NewService(
		environments.NewPgStore(pg), secrets, projects.NewPgStore(pg),
		discovery.ServiceCatalog{Store: discovery.NewPgStore(pg)}, authz,
		auditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: log},
		func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	)
	projectSvc.AddCleaner(e.svc)
	// Deployments read resolved variables, secrets included, through it.
	if err := forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide the variable service: %w", err)
	}
	return e.routes(fapp.Router(), guard)
}

// Start has nothing to start.
func (e *Extension) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

// fail maps a service error to its HTTP response.
func fail(err error) error {
	switch {
	case errors.Is(err, environments.ErrNotFound), errors.Is(err, projects.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, environments.ErrExists):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, environments.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}

// auditor records variable changes in Chronicle, in the organization's
// stream. Never a value: the key, the layer and whether it's secret.
type auditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a auditor) Record(ctx context.Context, action string, p security.Principal, v *environments.Variable) {
	b := a.chronicle.Info(ctx, action, "variable", v.ID).
		Category("variables").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success").
		Meta("key", v.Key).Meta("scope", v.Scope).Meta("secret", v.Secret)
	if v.ProjectID != "" {
		b = b.Meta("project_id", v.ProjectID)
	}
	if v.Service != "" {
		b = b.Meta("service", v.Service)
	}
	if v.Environment != "" {
		b = b.Meta("environment", v.Environment)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
