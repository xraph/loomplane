// Package cloudinfra registers organization server inventory and durable operations.
package cloudinfra

import (
	"context"
	"fmt"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	connectionsext "github.com/xraph/loomplane/extensions/connections"
	"github.com/xraph/loomplane/extensions/identity"
	notificationsext "github.com/xraph/loomplane/extensions/notifications"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

const Name = "loomplane-cloud-infrastructure"

// registrationSweepEvery spaces renewal sweeps; each one reads a domain
// back from the registrar at most daily.
const registrationSweepEvery = time.Hour

type Extension struct {
	*forge.BaseExtension
	cfg       config.Config
	workflows *orchestration.Deployments
	svc       *cloudinfra.Service
	cancel    context.CancelFunc
	done      chan struct{}
}

func New(cfg config.Config, workflows *orchestration.Deployments) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Cloud server infrastructure"), cfg: cfg, workflows: workflows}
}
func (*Extension) Dependencies() []string {
	return []string{connectionsext.Name, identity.Name, projectsext.Name, notificationsext.DeliveryName}
}
func (e *Extension) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}
	c := app.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return err
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return err
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return err
	}
	creds, err := forge.InjectType[*connections.Service](c)
	if err != nil {
		return err
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return err
	}
	notices, err := forge.InjectType[*notifications.Service](c)
	if err != nil {
		return err
	}
	key, err := security.DeriveKey(e.cfg.SecretKey, connections.VaultKeyLabel)
	if err != nil {
		return err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return err
	}
	secrets := connections.NewVaultSecrets(secret.NewService(vaultpg.New(db), enc, secret.WithAppID(connections.VaultAppID)))
	pg := pgdriver.Unwrap(db)
	endpoint := e.cfg.Clouds.EnrollmentURL
	if endpoint == "" {
		endpoint = e.cfg.PublicURL
	}
	principal, err := forge.InjectType[security.PrincipalVerifier](c)
	if err != nil {
		return err
	}
	e.svc = cloudinfra.NewService(cloudinfra.Deps{CapacityPrincipal: principal, CapacityDemandWakeup: e.workflows, Store: cloudinfra.NewPgStore(pg), Clouds: cloudprofiles.NewPgStore(pg), Connections: creds, RuntimeConnections: connections.NewPgStore(pg), Secrets: secrets, Authz: authz, Adapters: cloudinfra.BuiltinAdapters(cloudinfra.Endpoints{DigitalOcean: e.cfg.Clouds.DigitalOceanEndpoint, Vercel: e.cfg.Clouds.VercelEndpoint, Cloudflare: e.cfg.Clouds.CloudflareEndpoint, CloudflareRegistrarSandbox: e.cfg.Clouds.CloudflareRegistrarSandbox}), Workflow: e.workflows, Notices: notices, EnrollmentURL: endpoint, ACMEDirectory: e.cfg.Clouds.ACMEDirectory, Audit: projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()),
		Warn: func(msg string, err error) { e.Logger().Warn(msg, forge.F("error", err.Error())) }})
	e.workflows.BindInfrastructure(e.svc)
	if err = forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide cloud infrastructure: %w", err)
	}
	return e.routes(app.Router(), guard)
}
func (e *Extension) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var swept time.Time
		for {
			if err := e.svc.RecoverRoutes(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover domain route operations", forge.F("error", err.Error()))
			}
			if err := e.svc.RecoverInfrastructure(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover infrastructure operations", forge.F("error", err.Error()))
			}
			if err := e.workflows.RecoverCapacity(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("recover capacity workflow wakes", forge.F("error", err.Error()))
			}
			if err := e.svc.ReconcileExpansions(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover capacity expansions", forge.F("error", err.Error()))
			}
			if err := e.svc.RecoverRegistrations(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover domain registrations", forge.F("error", err.Error()))
			}
			// Each managed zone waiting on its TXT proof is looked at no more
			// than every two minutes, however often this runs, and each
			// verified zone's record once a day.
			if err := e.svc.CheckZoneProofs(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Check domain zone proofs", forge.F("error", err.Error()))
			}
			if time.Since(swept) >= registrationSweepEvery {
				swept = time.Now()
				if err := e.svc.SweepRegistrations(ctx); err != nil && ctx.Err() == nil {
					e.Logger().Warn("Sweep domain registrations", forge.F("error", err.Error()))
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	e.MarkStarted()
	return nil
}
func (e *Extension) Stop(ctx context.Context) error {
	if e.cancel != nil {
		e.cancel()
		select {
		case <-e.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
