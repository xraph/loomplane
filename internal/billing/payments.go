//go:build cloud

package billing

import (
	"context"

	"github.com/xraph/ledger/provider"
	"github.com/xraph/ledger/provider/stripe"
	"github.com/xraph/ledger/types"
)

// Payments is the part of the Stripe provider billing calls directly:
// saving a card, listing cards, refunds and webhook signatures. Ledger
// itself syncs invoices through the provider's plugin.
type Payments interface {
	EnsureCustomer(ctx context.Context, tenantID, email, name string) (string, error)
	CreateSetupSession(ctx context.Context, tenantID, successURL, cancelURL string) (string, error)
	ListPaymentMethods(ctx context.Context, tenantID string) ([]provider.PaymentMethod, error)
	RefundInvoice(ctx context.Context, stripeInvoiceID string, amount *types.Money, refundKey string) (bool, error)
	Verify(payload []byte, signatureHeader string) error
}

var _ Payments = (*stripe.Provider)(nil)
