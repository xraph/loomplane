//go:build cloud

// Package billing is Loomplane Cloud's billing extension. It exists only in
// binaries built with -tags cloud; the public build never links it.
package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/provider/stripe"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/extensions/notifications"
	planningext "github.com/xraph/loomplane/extensions/planning"
	platformcloudext "github.com/xraph/loomplane/extensions/platformcloud"
	"github.com/xraph/loomplane/internal/billing"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// Name is the extension's name.
const Name = "loomplane-billing"

// Queue is the Dispatch queue billing's jobs run on.
const Queue = "billing"

// Extension is Loomplane Cloud's billing.
type Extension struct {
	*forge.BaseExtension
	cfg    config.Config
	jobs   func() *engine.Engine
	engine *engine.Engine
	ledger *ledger.Ledger
	svc    *billing.Service
}

// New returns the extension. jobs returns the dispatch extension's engine.
func New(cfg config.Config, jobs func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Loomplane Cloud billing"),
		cfg:           cfg,
		jobs:          jobs,
	}
}

// Dependencies implements forge.Extension. Identity owns the accounts that
// get billed and builds the ledger; notifications sends what billing has to
// say; planning provides platform Cloud's service, which billing suspends
// and restores; platform Cloud configures that service.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, notifications.Name, planningext.Name, platformcloudext.Name}
}

// Register builds billing's service, provides its gate and meter, which
// the deploy, build, platform Cloud and AI seams pick up when they start,
// and registers billing's jobs.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	return e.register(fapp.Container())
}

// register is Register without forge's own bookkeeping: provide, then the
// jobs. The engine comes from the dispatch extension itself, as the AI
// extension's does: resolving it from the container starts it before the
// database is migrated.
func (e *Extension) register(c forge.Container) error {
	if err := e.provide(c); err != nil {
		return err
	}
	if e.jobs != nil {
		e.engine = e.jobs()
	}
	if e.engine == nil {
		return errors.New("billing needs the dispatch extension's engine")
	}
	l := e.ledger
	registerJobs(e.engine, e.svc, func(ctx context.Context, now time.Time) error {
		_, err := l.Advance(ctx, now)
		return err
	}, time.Now)
	return nil
}

// provide is the part of Register that builds the service from what
// identity and planning provide, then provides billing's metering.Gate and
// metering.Meter.
func (e *Extension) provide(c forge.Container) error {
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("billing needs the grove extension: %w", err)
	}
	l, err := forge.InjectType[*ledger.Ledger](c)
	if err != nil {
		return fmt.Errorf("billing needs identity's ledger: %w", err)
	}
	payments, err := forge.InjectType[*stripe.Provider](c)
	if err != nil {
		return fmt.Errorf("billing needs identity's Stripe provider: %w", err)
	}
	cfg, err := forge.InjectType[billing.Config](c)
	if err != nil {
		return fmt.Errorf("billing needs its config from identity: %w", err)
	}
	platform, err := forge.InjectType[*platformcloud.Service](c)
	if err != nil {
		return fmt.Errorf("billing needs the planning extension's platform Cloud service: %w", err)
	}
	svc := billing.New(billing.Deps{
		DB: db, Ledger: l, AppID: e.cfg.Identity.AppID,
		Payments: payments, Suspender: platform, Config: cfg,
	})
	if err = forge.ProvideValue[metering.Gate](c, svc.Gate()); err != nil {
		return fmt.Errorf("provide billing's gate: %w", err)
	}
	if err = forge.ProvideValue[metering.Meter](c, svc); err != nil {
		return fmt.Errorf("provide billing's meter: %w", err)
	}
	e.ledger, e.svc = l, svc
	return nil
}

// Start seeds the plans in ledger, then schedules billing's crons, which
// run on the cron leader only. The cloud API doesn't start without the
// plans: no workspace could subscribe.
func (e *Extension) Start(ctx context.Context) error {
	if err := billing.SeedCatalog(ctx, e.ledger, e.cfg.Identity.AppID); err != nil {
		return fmt.Errorf("billing: seed the plans: %w", err)
	}
	if e.engine == nil {
		return errors.New("billing: the jobs aren't registered; Register runs before Start")
	}
	if err := scheduleJobs(ctx, e.engine); err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	e.MarkStarted()
	return nil
}

// Stop implements forge.Extension. It never stops the ledger: its store's
// Close would close Loomplane's shared database.
func (e *Extension) Stop(context.Context) error {
	e.MarkStopped()
	return nil
}
