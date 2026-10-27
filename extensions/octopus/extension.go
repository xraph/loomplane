// Package octopus is the Forge extension for hosted Octopus gateways: the
// API the dashboard's Routes, Domains, Discovery and Settings tabs use,
// and the reconcile that pushes every gateway's routes every 30 seconds.
// The planning extension builds the gateways' store and syncer, which its
// catalog hooks share, and provides them here.
package octopus

import (
	"context"
	"fmt"

	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"

	cloudinfraext "github.com/xraph/loomplane/extensions/cloudinfra"
	"github.com/xraph/loomplane/extensions/identity"
	planningext "github.com/xraph/loomplane/extensions/planning"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/octopusloom"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-octopus"

// reconcileEvery is how often every gateway is pushed again.
const reconcileEvery = "@every 30s"

// Extension serves the Octopus API and schedules the reconcile.
type Extension struct {
	*forge.BaseExtension
	cfg      config.Config
	engineOf func() *engine.Engine
}

// New returns the extension. engineOf returns the dispatch extension's
// engine, which runs the reconcile.
func New(cfg config.Config, engineOf func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Octopus gateway routes"),
		cfg:           cfg,
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension. Identity guards the routes and
// checks permissions; cloudinfra lists the domains attached to a gateway;
// planning provides the gateways' store, syncer and component access.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, cloudinfraext.Name, planningext.Name}
}

// Register builds the service, registers the reconcile job and mounts the
// routes.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("octopus needs the identity extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("octopus needs the identity extension: %w", err)
	}
	store, err := forge.InjectType[octopusloom.Store](c)
	if err != nil {
		return fmt.Errorf("octopus needs the planning extension's gateway store: %w", err)
	}
	syncer, err := forge.InjectType[*octopusloom.Syncer](c)
	if err != nil {
		return fmt.Errorf("octopus needs the planning extension's syncer: %w", err)
	}
	access, err := forge.InjectType[*deployments.CatalogAccess](c)
	if err != nil {
		return fmt.Errorf("octopus needs the planning extension's component access: %w", err)
	}
	domains, err := forge.InjectType[*cloudinfra.Service](c)
	if err != nil {
		return fmt.Errorf("octopus needs the cloud infrastructure extension: %w", err)
	}
	svc := octopusloom.NewService(octopusloom.ServiceDeps{
		Store: store, Syncer: syncer, Access: access, Hostnames: domains, Bindings: syncer.Bindings,
		Authz: authz, Domains: domains,
		Warn: func(msg string, err error) { e.Logger().Warn(msg, forge.F("error", err.Error())) },
	})
	engine.Register(e.engineOf(), octopusloom.ReconcileJob(syncer))
	return mount(fapp.Router(), guard, svc)
}

// Start schedules the reconcile, which runs on the cron leader only. The
// schedule needs the database, which is migrated by now.
func (e *Extension) Start(ctx context.Context) error {
	def := &cron.Definition[octopusloom.ReconcilePayload]{
		Name: octopusloom.CronOctopusReconcile, Schedule: reconcileEvery,
		JobName: octopusloom.JobOctopusReconcile, Queue: orchestration.Queue,
	}
	if err := engine.RegisterCron(ctx, e.engineOf(), def); err != nil {
		return fmt.Errorf("schedule the Octopus reconcile: %w", err)
	}
	e.MarkStarted()
	return nil
}
