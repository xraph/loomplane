// Package dbgateway is the Forge extension for the database gateway's
// control plane: the endpoints each deployed gateway polls for its signed
// routing snapshot, reports to and asks for holds.
package dbgateway

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	planningext "github.com/xraph/loomplane/extensions/planning"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/deployments"
)

// Name is the extension's name.
const Name = "loomplane-dbgateway"

// Extension serves the gateway endpoints.
type Extension struct {
	*forge.BaseExtension
	cfg config.Config
	svc *dbgateway.Service
}

// New returns the extension.
func New(cfg config.Config) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Database gateway control plane"),
		cfg:           cfg,
	}
}

// Dependencies implements forge.Extension. Planning owns the Vault service
// resource addresses are kept in.
func (e *Extension) Dependencies() []string {
	return []string{planningext.Name}
}

// Register builds the service and mounts the routes. Without a signing key
// the routes still mount, and answer 503.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("the database gateway needs the grove extension: %w", err)
	}
	secrets, err := forge.InjectType[*deployments.VaultResourceSecrets](c)
	if err != nil {
		return fmt.Errorf("the database gateway needs the planning extension's resource secrets: %w", err)
	}
	var key ed25519.PrivateKey
	var trust plane.TrustKey
	if path := e.cfg.DBGateway.SigningKeyFile; path != "" {
		if key, trust, err = dbgateway.LoadSigningKey(path); err != nil {
			return fmt.Errorf("dbgateway.signing_key_file: %w", err)
		}
	} else {
		e.Logger().Warn("dbgateway.signing_key_file isn't set: the database gateway endpoints answer 503")
	}
	e.svc = dbgateway.NewService(dbgateway.NewStore(pgdriver.Unwrap(db)), resourceURLs{secrets: secrets}, key, trust, time.Now)
	e.svc.OnSkip = func(gatewayID string, skipped []dbgateway.Skipped) {
		for _, s := range skipped {
			e.Logger().Warn("a database gateway route is left out of its snapshot",
				forge.F("gateway_id", gatewayID), forge.F("resource", s.Resource), forge.F("reason", s.Reason))
		}
	}
	// Deploying a gateway hands it the service's trust keys.
	if err := forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide the database gateway service: %w", err)
	}
	return mount(fapp.Router(), &handlers{
		svc: e.svc, wait: pollWait, every: pollEvery, now: time.Now,
		warn: func(msg string, err error) { e.Logger().Warn(msg, forge.F("error", err.Error())) },
	})
}

// Start has nothing to start.
func (e *Extension) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

// resourceURLs reads resource addresses from the Vault resource secrets the
// deployment runner writes them to.
type resourceURLs struct {
	secrets deployments.ResourceSecrets
}

var _ dbgateway.ResourceURLs = resourceURLs{}

func (u resourceURLs) URL(ctx context.Context, org, environmentID, resource string) (string, error) {
	v, err := u.secrets.Get(ctx, deployments.ResourceURLKey(org, environmentID, resource))
	if errors.Is(err, deployments.ErrNoCredential) {
		return "", dbgateway.ErrNoURL
	}
	if err != nil {
		return "", err
	}
	return string(v), nil
}
