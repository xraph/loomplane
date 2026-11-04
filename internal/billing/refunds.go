//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/provider/stripe"
	"github.com/xraph/ledger/types"

	"github.com/xraph/loomplane/internal/pglock"
)

var (
	// ErrNotRefundable is Refund's error for an invoice that isn't paid in
	// ledger, was never sent to Stripe (a zero invoice), or has nothing
	// left to refund.
	ErrNotRefundable = errors.New("billing: only a paid invoice sent to Stripe can be refunded, and only what is left of it")
	// ErrRefundTooLarge is Refund's error for an amount above what is left
	// to refund on the invoice.
	ErrRefundTooLarge = errors.New("billing: the refund is more than is left to refund on the invoice")
	// ErrRefundKeyReused is Refund's error for a refund key already used
	// for another refund: another invoice or another amount.
	ErrRefundKeyReused = errors.New("billing: that refund key was used for a different refund")
)

// refundLock, per invoice, is held while one refund of it is checked,
// recorded and sent, so two refunds never both pass the check of what is
// left.
const refundLock = "loomplane:billing:refund:"

// RefundKeyPattern is what a refund key may be: what the dashboard makes
// per click (a UUID) fits, and so does any key of up to 200 letters,
// digits, dashes and underscores, the most ledger's Stripe provider sends.
var RefundKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// Refund refunds a paid invoice through Stripe and records it once per
// refund key. It never voids the invoice in ledger. cents nil is the rest
// of the invoice.
//
// The refund reaches Stripe through ledger's Stripe provider, which finds
// the payment that paid the Stripe invoice and refunds its payment intent
// (or its charge) with the secret key, server side, under the idempotency
// key "refund:<stripe invoice>:<refund key>". Loomplane records the refund
// first, under the invoice's refund lock: a repeat of a key whose refund
// Stripe made returns the stored result without calling Stripe; a repeat
// of one whose send never finished (the process stopped) sends it again
// under the same key, which Stripe's idempotency answers with the first
// refund if it was made. A Stripe error deletes the row, so the person can
// retry with the same key.
//
// What is left to refund is the invoice's total less every refund recorded
// for it; a refund above it is ErrRefundTooLarge. by names who refunded.
func (s *Service) Refund(ctx context.Context, invoiceID string, cents *int64, refundKey, by string) (full bool, err error) {
	if readyErr := s.ready(); readyErr != nil {
		return false, readyErr
	}
	by = strings.TrimSpace(by)
	switch {
	case invoiceID == "":
		return false, ErrInvoiceNotFound
	case !RefundKeyPattern.MatchString(refundKey):
		return false, errors.New("billing: a refund key is 1 to 200 letters, digits, dashes or underscores")
	case by == "":
		return false, errors.New("billing: a refund needs to say who made it")
	case cents != nil && *cents <= 0:
		return false, errors.New("billing: a refund is at least one cent")
	}
	if s.payments == nil {
		return false, errors.New("billing: nothing to refund with")
	}
	err = pglock.Do(ctx, s.pg, refundLock+invoiceID, func(ctx context.Context) error {
		var lockErr error
		full, lockErr = s.refund(ctx, invoiceID, cents, refundKey, by)
		return lockErr
	})
	return full, err
}

// storedRefund is a refund Loomplane recorded.
type storedRefund struct {
	invoiceID, orgID string
	cents            int64
	full             bool
	sent             bool
}

// refund is Refund under the invoice's refund lock.
func (s *Service) refund(ctx context.Context, invoiceID string, cents *int64, refundKey, by string) (bool, error) {
	if prev, found, err := s.storedRefund(ctx, refundKey); err != nil || found {
		if err != nil {
			return false, err
		}
		return s.repeatRefund(ctx, prev, invoiceID, cents, refundKey)
	}

	orgID, providerID, total, err := s.refundableInvoice(ctx, invoiceID)
	if err != nil {
		return false, err
	}
	var refunded int64
	if err = s.pg.QueryRow(ctx, `SELECT COALESCE(sum(cents), 0)::bigint FROM loomplane_billing_refunds WHERE invoice_id = $1`,
		invoiceID).Scan(&refunded); err != nil {
		return false, fmt.Errorf("billing: read the refunds of invoice %s: %w", invoiceID, err)
	}
	left := total - refunded
	if left <= 0 {
		return false, fmt.Errorf("%w: invoice %s is refunded in full", ErrNotRefundable, invoiceID)
	}
	amount := left
	if cents != nil {
		if *cents > left {
			return false, fmt.Errorf("%w: %d cents asked, %d left on invoice %s", ErrRefundTooLarge, *cents, left, invoiceID)
		}
		amount = *cents
	}

	var mine bool
	err = s.pg.QueryRow(ctx, `INSERT INTO loomplane_billing_refunds (refund_key, invoice_id, org_id, cents, full_refund, refunded_by, created_at)
		VALUES ($1, $2, $3, $4, false, $5, $6) ON CONFLICT (refund_key) DO NOTHING RETURNING true`,
		refundKey, invoiceID, orgID, amount, by, s.now().UTC()).Scan(&mine)
	if errors.Is(err, pgx.ErrNoRows) {
		// The same key, recorded since by a refund of another invoice,
		// which holds another lock.
		prev, found, readErr := s.storedRefund(ctx, refundKey)
		if readErr != nil || !found {
			return false, errors.Join(fmt.Errorf("billing: read refund %s: %w", refundKey, err), readErr)
		}
		return s.repeatRefund(ctx, prev, invoiceID, cents, refundKey)
	}
	if err != nil {
		return false, fmt.Errorf("billing: record the refund of invoice %s: %w", invoiceID, err)
	}
	return s.sendRefund(ctx, storedRefund{invoiceID: invoiceID, orgID: orgID, cents: amount}, providerID, refundKey)
}

// repeatRefund answers a refund key already recorded: the stored result
// when Stripe made it, or the refund sent again under the same key when
// its first send never finished. A key recorded for another invoice or
// another amount is ErrRefundKeyReused.
func (s *Service) repeatRefund(ctx context.Context, prev storedRefund, invoiceID string, cents *int64, refundKey string) (bool, error) {
	if prev.invoiceID != invoiceID || (cents != nil && *cents != prev.cents) {
		return false, ErrRefundKeyReused
	}
	if prev.sent {
		return prev.full, nil
	}
	slog.WarnContext(ctx, "billing: sending a refund again whose first send didn't finish", "invoice", invoiceID, "org", prev.orgID)
	_, providerID, _, err := s.refundableInvoice(ctx, invoiceID)
	if err != nil {
		return false, err
	}
	return s.sendRefund(ctx, prev, providerID, refundKey)
}

// sendRefund asks Stripe for a recorded refund and records the answer. A
// Stripe error deletes the record, so nothing is counted as refunded that
// Stripe refused, and the same key can be tried again.
func (s *Service) sendRefund(ctx context.Context, r storedRefund, providerID, refundKey string) (bool, error) {
	amount := types.Money{Amount: r.cents, Currency: currency}
	full, err := s.payments.RefundInvoice(ctx, providerID, &amount, refundKey)
	if err != nil {
		// The person sees the failure; the record must go even if their
		// request was canceled.
		if _, delErr := s.pg.Exec(context.WithoutCancel(ctx), `DELETE FROM loomplane_billing_refunds WHERE refund_key = $1 AND sent_at IS NULL`,
			refundKey); delErr != nil {
			slog.ErrorContext(ctx, "billing: a refund Stripe didn't make stays recorded; retry it with the same key",
				"invoice", r.invoiceID, "error", delErr)
		}
		if errors.Is(err, stripe.ErrNotPaid) {
			return false, fmt.Errorf("%w: Stripe has no paid payment on invoice %s", ErrNotRefundable, r.invoiceID)
		}
		return false, fmt.Errorf("billing: refund invoice %s in Stripe: %w", r.invoiceID, err)
	}
	if _, err = s.pg.Exec(context.WithoutCancel(ctx), `UPDATE loomplane_billing_refunds SET full_refund = $2, sent_at = $3 WHERE refund_key = $1`,
		refundKey, full, s.now().UTC()); err != nil {
		// Stripe refunded; a repeat of the key sends it again, and Stripe's
		// idempotency answers with this refund.
		return false, fmt.Errorf("billing: record that Stripe refunded invoice %s: %w", r.invoiceID, err)
	}
	return full, nil
}

// storedRefund reads the refund recorded under key.
func (s *Service) storedRefund(ctx context.Context, key string) (storedRefund, bool, error) {
	var (
		r     storedRefund
		cents *int64
		sent  *time.Time
	)
	err := s.pg.QueryRow(ctx, `SELECT invoice_id, org_id, cents, full_refund, sent_at FROM loomplane_billing_refunds WHERE refund_key = $1`,
		key).Scan(&r.invoiceID, &r.orgID, &cents, &r.full, &sent)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedRefund{}, false, nil
	}
	if err != nil {
		return storedRefund{}, false, fmt.Errorf("billing: read refund %s: %w", key, err)
	}
	if cents != nil {
		r.cents = *cents
	}
	r.sent = sent != nil
	return r, true, nil
}

// refundableInvoice is the workspace, the Stripe invoice and the total of
// an invoice that can be refunded: recorded by Loomplane, paid in ledger
// and sent to Stripe. An invoice Loomplane never recorded is
// ErrInvoiceNotFound.
func (s *Service) refundableInvoice(ctx context.Context, invoiceID string) (orgID, providerID string, total int64, err error) {
	err = s.pg.QueryRow(ctx, `SELECT org_id, provider_id FROM loomplane_billing_invoices WHERE invoice_id = $1`, invoiceID).
		Scan(&orgID, &providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", 0, ErrInvoiceNotFound
	}
	if err != nil {
		return "", "", 0, fmt.Errorf("billing: read invoice %s: %w", invoiceID, err)
	}
	iid, err := id.ParseInvoiceID(invoiceID)
	if err != nil {
		return "", "", 0, ErrInvoiceNotFound
	}
	inv, err := s.l.Store().GetInvoice(ctx, iid)
	if err != nil {
		return "", "", 0, fmt.Errorf("billing: read invoice %s: %w", invoiceID, err)
	}
	if providerID == "" {
		providerID = inv.ProviderID
	}
	if inv.Status != invoice.StatusPaid {
		return "", "", 0, fmt.Errorf("%w: invoice %s is %s", ErrNotRefundable, invoiceID, inv.Status)
	}
	if providerID == "" {
		return "", "", 0, fmt.Errorf("%w: invoice %s was never sent to Stripe", ErrNotRefundable, invoiceID)
	}
	return orgID, providerID, inv.Total.Amount, nil
}
