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
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

const Name = "loomplane-cloud-infrastructure"

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
	return []string{connectionsext.Name, identity.Name, projectsext.Name}
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
	e.svc = cloudinfra.NewService(cloudinfra.Deps{Store: cloudinfra.NewPgStore(pg), Clouds: cloudprofiles.NewPgStore(pg), Connections: creds, RuntimeConnections: connections.NewPgStore(pg), Secrets: secrets, Authz: authz, Adapters: cloudinfra.BuiltinAdapters(e.cfg.Clouds.DigitalOceanEndpoint, e.cfg.Clouds.VercelEndpoint), Workflow: e.workflows, EnrollmentURL: endpoint, Audit: projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger())})
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
		for {
			if err := e.svc.RecoverRoutes(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover domain route operations", forge.F("error", err.Error()))
			}
			if err := e.svc.RecoverInfrastructure(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover infrastructure operations", forge.F("error", err.Error()))
			}
			if err := e.svc.ReconcileExpansions(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover capacity expansions", forge.F("error", err.Error()))
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
