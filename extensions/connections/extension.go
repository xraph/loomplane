// Package connections is the Forge extension for cloud connections: the
// Google Cloud and Vercel accounts an organization deploys to, with their
// credentials in Vault.
package connections

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

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/plugins"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
)

// Name is the extension's name.
const Name = "loomplane-connections"

// Extension serves the cloud connection API, and provides the service to
// the extensions that deploy with it.
type Extension struct {
	*forge.BaseExtension
	cfg     config.Config
	plugins *plugins.Set
	svc     *connections.Service
}

// New returns the extension.
func New(cfg config.Config, plugs *plugins.Set) *Extension {
	return &Extension{
		plugins:       plugs,
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Cloud connections"),
		cfg:           cfg,
	}
}

// Dependencies implements forge.Extension.
func (e *Extension) Dependencies() []string { return []string{identity.Name} }

// Service is the connection service, once the extension has registered.
func (e *Extension) Service() *connections.Service { return e.svc }

// Register builds the service over Vault and mounts the routes.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("connections needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("connections needs the identity extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("connections needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("connections needs the audit trail: %w", err)
	}

	// Credentials get a key of their own, derived from secret_key, apart
	// from the variables' key.
	key, err := security.DeriveKey(e.cfg.SecretKey, connections.VaultKeyLabel)
	if err != nil {
		return err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return fmt.Errorf("vault encryptor: %w", err)
	}
	secrets := connections.NewVaultSecrets(secret.NewService(vaultpg.New(db), enc, secret.WithAppID(connections.VaultAppID)))
	log := e.Logger()
	checkers := map[string]connections.Checker{
		connections.GCP:          connections.GCPChecker{Client: gcp.New(e.cfg.Clouds.GoogleEndpoint)},
		connections.Vercel:       connections.VercelChecker{Client: vercel.New(e.cfg.Clouds.VercelEndpoint)},
		connections.AWS:          connections.AWSChecker{Client: aws.New(e.cfg.Clouds.AWSEndpoint)},
		connections.Render:       connections.RenderChecker{Client: render.New(e.cfg.Clouds.RenderEndpoint)},
		connections.DigitalOcean: connections.DigitalOceanChecker{Client: digitalocean.New(e.cfg.Clouds.DigitalOceanEndpoint)},
		connections.Server:       connections.ServerChecker{},
		connections.Kubernetes:   connections.KubernetesChecker{},
		connections.Registry:     connections.RegistryChecker{},
	}
	// A provider plugin's cloud is connected through the plugin.
	plugs, err := e.plugins.Plugins()
	if err != nil {
		return err
	}
	for _, p := range plugs {
		if _, taken := checkers[p.Cloud()]; !taken {
			checkers[p.Cloud()] = connections.PluginChecker{Client: p.Client}
		}
	}
	e.svc = connections.NewService(
		connections.NewPgStore(pgdriver.Unwrap(db)), secrets,
		checkers,
		authz, auditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: log},
		func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	)
	e.svc.Telemetry = telemetry.New(fapp.Metrics())
	if err := forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide the connection service: %w", err)
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
	case errors.Is(err, connections.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, connections.ErrExists), errors.Is(err, connections.ErrInUse), errors.Is(err, connections.ErrChanged):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, connections.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, connections.ErrUnavailable):
		return forge.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}

// auditor records connection changes in Chronicle, in the organization's
// stream: the cloud, the account and the check result, never the
// credential.
type auditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a auditor) Record(ctx context.Context, action string, p security.Principal, c *connections.Connection) {
	b := a.chronicle.Warning(ctx, action, "cloud_connection", c.ID).
		Category("cloud_connections").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success").
		Meta("cloud", c.Cloud).Meta("name", c.Name).Meta("account", c.Account).Meta("status", c.Status)
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
