//go:build cloud

package identity

import (
	"errors"
	"os"

	authsome "github.com/xraph/authsome"
	"github.com/xraph/authsome/plugin"
	"github.com/xraph/authsome/plugins/subscription"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/provider/stripe"

	"github.com/xraph/loomplane/internal/billing"
)

// editionState is what only this edition builds: the ledger authsome bills
// through, its Stripe provider, and billing's config. Billing takes all
// three from DI.
type editionState struct {
	ledger   *ledger.Ledger
	payments *stripe.Provider
	billing  billing.Config
}

// editionOptions builds the ledger and returns the authsome options that
// carry it. Without billing's config the cloud API refuses to start.
func (e *Extension) editionOptions(db *grove.DB) ([]authsome.Option, error) {
	cfg, err := billing.LoadConfig(os.Getenv)
	if err != nil {
		return nil, err // the cloud API refuses to start
	}
	l, p, err := billing.NewLedger(db, cfg)
	if err != nil {
		return nil, err
	}
	e.edition = editionState{ledger: l, payments: p, billing: cfg}
	opts := []authsome.Option{authsome.WithLedgerEngine(l)}
	for _, pl := range e.editionPlugins(db, l) {
		opts = append(opts, authsome.WithPlugin(pl))
	}
	return opts, nil
}

// editionPlugins are the authsome plugins this edition registers, in the
// order authsome runs their hooks.
func (e *Extension) editionPlugins(db *grove.DB, l *ledger.Ledger) []plugin.Plugin {
	return []plugin.Plugin{
		// Before the subscription plugin: authsome runs AfterOrgDelete hooks
		// in registration order, and a deleted workspace's overage is
		// invoiced before the plugin cancels its subscriptions.
		billing.NewOrgDeleteHook(db, l, e.cfg.Identity.AppID),
		// Billing creates every subscription itself, with no trial, and
		// serves its own routes.
		subscription.New(subscription.Config{DisableRoutes: true, DisableAutoSubscribe: true, TrialDays: 0}),
	}
}

// editionProvide puts the ledger, its Stripe provider and billing's config
// into DI, each under its own concrete type.
func (e *Extension) editionProvide(c forge.Container) error {
	return errors.Join(
		forge.ProvideValue(c, e.edition.ledger),
		forge.ProvideValue(c, e.edition.payments),
		forge.ProvideValue(c, e.edition.billing),
	)
}
