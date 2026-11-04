//go:build cloud

package billing

import (
	"errors"
	"fmt"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/plugin"
	"github.com/xraph/ledger/provider/stripe"
	pgstore "github.com/xraph/ledger/store/postgres"
)

// currency is what every plan and invoice is in.
const currency = "usd"

// NewLedger builds ledger over Loomplane's database with the Stripe
// provider. It neither migrates (the central pass does) nor starts a
// lifecycle worker (billing's cron calls Advance). Never Stop it: the
// postgres store's Close closes db.
func NewLedger(db *grove.DB, cfg Config) (*ledger.Ledger, *stripe.Provider, error) {
	if db == nil {
		return nil, nil, errors.New("billing: ledger needs Loomplane's database")
	}
	pg, ok := db.Driver().(*pgdriver.PgDB)
	if !ok {
		return nil, nil, errors.New("billing: ledger needs Postgres")
	}
	p, err := stripe.New(stripe.Config{
		SecretKey:     cfg.StripeSecretKey,
		WebhookSecret: cfg.StripeWebhookSecret,
		Customers:     pgCustomers{pg: pg},
		Currency:      currency,
	})
	if err != nil {
		// stripe.New names what is missing and never the key itself.
		return nil, nil, fmt.Errorf("billing: %w", err)
	}
	return newLedgerWith(db, stripe.NewPlugin(p)), p, nil
}

// newLedgerWith is NewLedger with any payment provider plugin, for tests.
func newLedgerWith(db *grove.DB, p plugin.PaymentProviderPlugin) *ledger.Ledger {
	return ledger.New(pgstore.New(db),
		ledger.WithPlugin(p),
		ledger.WithoutMigrate(),
		ledger.WithLifecycleInterval(0),
	)
}
