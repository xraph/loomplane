//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger/provider/stripe"
)

// pgCustomers is stripe.CustomerStore on loomplane_billing_accounts.
// SetCustomerID upserts: the account row may not exist yet.
type pgCustomers struct{ pg *pgdriver.PgDB }

var _ stripe.CustomerStore = pgCustomers{}

// CustomerID is the workspace's Stripe customer, or "" when it has none or
// no account row yet.
func (c pgCustomers) CustomerID(ctx context.Context, orgID string) (string, error) {
	var id string
	err := c.pg.QueryRow(ctx,
		`SELECT stripe_customer_id FROM loomplane_billing_accounts WHERE org_id=$1`, orgID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("billing: read the stripe customer of %s: %w", orgID, err)
	}
	return id, nil
}

// SetCustomerID records the workspace's Stripe customer, making its account
// row when there is none.
func (c pgCustomers) SetCustomerID(ctx context.Context, orgID, customerID string) error {
	_, err := c.pg.Exec(ctx,
		`INSERT INTO loomplane_billing_accounts (org_id, stripe_customer_id, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (org_id) DO UPDATE SET stripe_customer_id=EXCLUDED.stripe_customer_id, updated_at=now()`,
		orgID, customerID)
	if err != nil {
		return fmt.Errorf("billing: save the stripe customer of %s: %w", orgID, err)
	}
	return nil
}
