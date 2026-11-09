//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	stripego "github.com/stripe/stripe-go/v87"
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
	// ErrRefundUnconfirmed is Refund's error when Stripe didn't say whether
	// it made the refund: the connection failed, Stripe failed, or it
	// answered in a way that doesn't settle it. The refund stays recorded,
	// unsent, and the same refund key sends it again; Stripe's idempotency
	// on that key keeps it from being paid twice.
	ErrRefundUnconfirmed = errors.New("billing: Stripe didn't confirm the refund; retry the same refund key")
	// ErrRefundStale is Refund's error for a refund recorded more than
	// refundResendWindow ago that Stripe never confirmed. Stripe keeps an
	// idempotency key for 24 hours, so sending it again could refund twice:
	// someone checks Stripe first.
	ErrRefundStale = errors.New("billing: the refund was recorded too long ago to send again safely; check Stripe before refunding again")
	// ErrRefundNotFound is SettleRefund's error for a refund key that isn't
	// recorded for the invoice.
	ErrRefundNotFound = errors.New("billing: there's no such refund on this invoice")
	// ErrRefundSettled is SettleRefund's error for a refund Stripe already
	// confirmed: there is nothing to settle.
	ErrRefundSettled = errors.New("billing: Stripe already confirmed this refund")
)

// StripeRefundIDPattern is a Stripe refund id, as the owner copies it from
// Stripe's dashboard to settle a refund by hand.
var StripeRefundIDPattern = regexp.MustCompile(`^(re|pyr)_[A-Za-z0-9]{1,250}$`)

// refundResendWindow is how long after it was recorded an unconfirmed
// refund is sent again under its key: under Stripe's 24 hours of
// idempotency, with an hour to spare.
const refundResendWindow = 23 * time.Hour

// refundTimeout bounds one call to Stripe for a refund. The call never
// takes the request's cancellation: a person closing the page mustn't cut
// it off mid-flight.
const refundTimeout = 30 * time.Second

// RefundResult is how a Refund call went.
type RefundResult struct {
	// Full says the invoice's payment is refunded in total in Stripe.
	Full bool
	// Replay says the key's refund was made before: this call sent nothing.
	Replay bool
	// Resent says the key's refund was recorded unconfirmed and this call
	// sent it again under the same key.
	Resent bool
}

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
// Stripe made returns the stored result without calling Stripe (Replay).
//
// Stripe refusing the refund outright (refundRefused) deletes the record,
// so nothing counts as refunded that Stripe didn't make, and the same key
// can be tried again. Any other failure leaves the outcome unknown: the
// record stays, unsent, still counted against what is left, and Refund
// says ErrRefundUnconfirmed. The same key then sends it again (Resent),
// while it is under refundResendWindow old; after that it is
// ErrRefundStale, and PlatformInvoices lists it for someone to settle in
// Stripe.
//
// What is left to refund is the invoice's total less every refund recorded
// for it, confirmed or not; a refund above it is ErrRefundTooLarge. by
// names who refunded.
func (s *Service) Refund(ctx context.Context, invoiceID string, cents *int64, refundKey, by string) (res RefundResult, err error) {
	if readyErr := s.ready(); readyErr != nil {
		return RefundResult{}, readyErr
	}
	by = strings.TrimSpace(by)
	switch {
	case invoiceID == "":
		return RefundResult{}, ErrInvoiceNotFound
	case !RefundKeyPattern.MatchString(refundKey):
		return RefundResult{}, errors.New("billing: a refund key is 1 to 200 letters, digits, dashes or underscores")
	case by == "":
		return RefundResult{}, errors.New("billing: a refund needs to say who made it")
	case cents != nil && *cents <= 0:
		return RefundResult{}, errors.New("billing: a refund is at least one cent")
	}
	if s.payments == nil {
		return RefundResult{}, errors.New("billing: nothing to refund with")
	}
	err = pglock.Do(ctx, s.pg, refundLock+invoiceID, func(ctx context.Context) error {
		var lockErr error
		res, lockErr = s.refund(ctx, invoiceID, cents, refundKey, by)
		return lockErr
	})
	return res, err
}

// storedRefund is a refund Loomplane recorded.
type storedRefund struct {
	invoiceID, orgID string
	cents            int64
	full             bool
	sent             bool
	createdAt        time.Time
}

// refund is Refund under the invoice's refund lock.
func (s *Service) refund(ctx context.Context, invoiceID string, cents *int64, refundKey, by string) (RefundResult, error) {
	if prev, found, err := s.storedRefund(ctx, refundKey); err != nil || found {
		if err != nil {
			return RefundResult{}, err
		}
		return s.repeatRefund(ctx, prev, invoiceID, cents, refundKey)
	}

	orgID, providerID, total, err := s.refundableInvoice(ctx, invoiceID)
	if err != nil {
		return RefundResult{}, err
	}
	var refunded int64
	if err = s.pg.QueryRow(ctx, `SELECT COALESCE(sum(cents), 0)::bigint FROM loomplane_billing_refunds WHERE invoice_id = $1`,
		invoiceID).Scan(&refunded); err != nil {
		return RefundResult{}, fmt.Errorf("billing: read the refunds of invoice %s: %w", invoiceID, err)
	}
	left := total - refunded
	if left <= 0 {
		return RefundResult{}, fmt.Errorf("%w: invoice %s is refunded in full", ErrNotRefundable, invoiceID)
	}
	amount := left
	if cents != nil {
		if *cents > left {
			return RefundResult{}, fmt.Errorf("%w: %d cents asked, %d left on invoice %s", ErrRefundTooLarge, *cents, left, invoiceID)
		}
		amount = *cents
	}

	now := s.now().UTC()
	var mine bool
	err = s.pg.QueryRow(ctx, `INSERT INTO loomplane_billing_refunds (refund_key, invoice_id, org_id, cents, full_refund, refunded_by, created_at)
		VALUES ($1, $2, $3, $4, false, $5, $6) ON CONFLICT (refund_key) DO NOTHING RETURNING true`,
		refundKey, invoiceID, orgID, amount, by, now).Scan(&mine)
	if errors.Is(err, pgx.ErrNoRows) {
		// The same key, recorded since by a refund of another invoice,
		// which holds another lock.
		prev, found, readErr := s.storedRefund(ctx, refundKey)
		if readErr != nil || !found {
			return RefundResult{}, errors.Join(fmt.Errorf("billing: read refund %s: %w", refundKey, err), readErr)
		}
		return s.repeatRefund(ctx, prev, invoiceID, cents, refundKey)
	}
	if err != nil {
		return RefundResult{}, fmt.Errorf("billing: record the refund of invoice %s: %w", invoiceID, err)
	}
	full, err := s.sendRefund(ctx, storedRefund{invoiceID: invoiceID, orgID: orgID, cents: amount, createdAt: now}, providerID, refundKey)
	return RefundResult{Full: full}, err
}

// repeatRefund answers a refund key already recorded: the stored result
// when Stripe made it, or the refund sent again under the same key when
// Stripe never confirmed it and it is under refundResendWindow old. A key
// recorded for another invoice or another amount is ErrRefundKeyReused.
func (s *Service) repeatRefund(ctx context.Context, prev storedRefund, invoiceID string, cents *int64, refundKey string) (RefundResult, error) {
	if prev.invoiceID != invoiceID || (cents != nil && *cents != prev.cents) {
		return RefundResult{}, ErrRefundKeyReused
	}
	if prev.sent {
		return RefundResult{Full: prev.full, Replay: true}, nil
	}
	if !resendable(prev.createdAt, s.now()) {
		slog.ErrorContext(ctx, "billing: an unconfirmed refund is past Stripe's idempotency window; check Stripe and settle it by hand",
			"invoice", invoiceID, "org", prev.orgID, "recorded_at", prev.createdAt.Format(time.RFC3339))
		return RefundResult{}, fmt.Errorf("%w: invoice %s, recorded %s", ErrRefundStale, invoiceID, prev.createdAt.Format(time.RFC3339))
	}
	slog.WarnContext(ctx, "billing: sending an unconfirmed refund again under its key", "invoice", invoiceID, "org", prev.orgID)
	_, providerID, _, err := s.refundableInvoice(ctx, invoiceID)
	if err != nil {
		return RefundResult{}, err
	}
	full, err := s.sendRefund(ctx, prev, providerID, refundKey)
	return RefundResult{Full: full, Resent: true}, err
}

// resendable says whether an unconfirmed refund recorded at createdAt is
// still inside the window in which its key can be sent again.
func resendable(createdAt, now time.Time) bool {
	return now.Sub(createdAt) < refundResendWindow
}

// sendRefund asks Stripe for a recorded refund and records the answer. The
// call to Stripe ignores the request's cancellation and is bounded by
// refundTimeout. A refusal deletes the record; any other failure keeps it,
// unsent, and is ErrRefundUnconfirmed.
func (s *Service) sendRefund(ctx context.Context, r storedRefund, providerID, refundKey string) (bool, error) {
	amount := types.Money{Amount: r.cents, Currency: currency}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundTimeout)
	defer cancel()
	full, err := s.payments.RefundInvoice(sendCtx, providerID, &amount, refundKey)
	if err != nil {
		if !refundRefused(err) {
			slog.ErrorContext(ctx, "billing: Stripe didn't confirm a refund; it stays recorded until the same key sends it again",
				"invoice", r.invoiceID, "org", r.orgID, "error", err)
			return false, fmt.Errorf("%w: invoice %s: %w", ErrRefundUnconfirmed, r.invoiceID, err)
		}
		if _, delErr := s.pg.Exec(context.WithoutCancel(ctx), `DELETE FROM loomplane_billing_refunds WHERE refund_key = $1 AND sent_at IS NULL`,
			refundKey); delErr != nil {
			slog.ErrorContext(ctx, "billing: a refund Stripe refused stays recorded; it holds its amount until it is deleted",
				"invoice", r.invoiceID, "error", delErr)
		}
		if errors.Is(err, stripe.ErrNotPaid) {
			return false, fmt.Errorf("%w: Stripe has no paid payment on invoice %s", ErrNotRefundable, r.invoiceID)
		}
		return false, fmt.Errorf("billing: Stripe refused the refund of invoice %s: %w", r.invoiceID, err)
	}
	if _, err = s.pg.Exec(context.WithoutCancel(ctx), `UPDATE loomplane_billing_refunds SET full_refund = $2, sent_at = $3 WHERE refund_key = $1`,
		refundKey, full, s.now().UTC()); err != nil {
		// Stripe refunded and the record doesn't say so yet: it stays
		// unsent, and a repeat of the key sends it again, which Stripe's
		// idempotency answers with this refund.
		slog.ErrorContext(ctx, "billing: Stripe made a refund that couldn't be recorded as made; the same key records it",
			"invoice", r.invoiceID, "org", r.orgID, "error", err)
		return false, fmt.Errorf("%w: Stripe refunded invoice %s and recording it failed: %w", ErrRefundUnconfirmed, r.invoiceID, err)
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
	err := s.pg.QueryRow(ctx, `SELECT invoice_id, org_id, cents, full_refund, sent_at, created_at FROM loomplane_billing_refunds
		WHERE refund_key = $1`, key).Scan(&r.invoiceID, &r.orgID, &cents, &r.full, &sent, &r.createdAt)
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
	r.createdAt = r.createdAt.UTC()
	return r, true, nil
}

// refundableInvoice is the workspace, the Stripe invoice and the total of
// an invoice that can be refunded: recorded by Loomplane, paid in ledger
// and sent to Stripe. The Stripe invoice is the one Loomplane recorded
// when the sync sent it, the same source PlatformInvoices reads. An
// invoice Loomplane never recorded is ErrInvoiceNotFound.
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
	if inv.Status != invoice.StatusPaid {
		return "", "", 0, fmt.Errorf("%w: invoice %s is %s", ErrNotRefundable, invoiceID, inv.Status)
	}
	if providerID == "" {
		return "", "", 0, fmt.Errorf("%w: invoice %s was never sent to Stripe", ErrNotRefundable, invoiceID)
	}
	return orgID, providerID, inv.Total.Amount, nil
}

// ledgerPreflightFailures are the errors ledger's Stripe provider (v1.9.1,
// provider/stripe/invoices.go, RefundInvoice and paidPayment) returns
// before it asks Stripe for a refund: its own checks and the listing of
// the invoice's payments. None of them made a refund, so the record can
// go. The wording is ledger's; TestRefund_DefinitiveFailuresDeleteAmbiguousOnesStay
// pins it.
var ledgerPreflightFailures = []string{
	"stripe: list payments for invoice ",
	"it has more than one paid payment",
	"has no charge to refund",
	": amount is in ",
	"stripe invoice id is required",
	": refund key is required",
	"characters, at most",
	": amount must be positive",
}

// refundRefused says whether a refund that failed certainly wasn't made:
// ledger refused it before asking Stripe (no paid payment, its own checks,
// listing the payments failed), or Stripe answered 400, 401, 402, 403 or
// 404. Anything else (no answer, a cancellation or timeout, 409, 429, a
// 5xx, or a failure after Stripe made it) leaves the outcome unknown. So
// does an idempotency_error, whatever its status: Stripe saw the key
// before with another request body (a ledger or stripe-go upgrade can
// change it), and that first request may have made the refund.
func refundRefused(err error) bool {
	if errors.Is(err, stripe.ErrNotPaid) {
		return true
	}
	msg := err.Error()
	for _, f := range ledgerPreflightFailures {
		if strings.Contains(msg, f) {
			return true
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *stripego.Error
	if errors.As(err, &se) {
		if se.Type == stripego.ErrorTypeIdempotency {
			return false
		}
		switch se.HTTPStatusCode {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
			return true
		}
	}
	return false
}

// SettleRefund settles, by hand, a refund Stripe never confirmed, once
// someone checked the invoice in Stripe: made says Stripe has the refund
// (stripeRefundID names it), so it counts as refunded from now on; dropped
// says Stripe has none, so it is deleted and its amount can be refunded
// again. It is for a refund past refundResendWindow, which Refund no
// longer sends, though any unconfirmed refund can be settled. A refund
// Stripe confirmed is ErrRefundSettled; an unknown key is
// ErrRefundNotFound. It runs under the invoice's refund lock. by names who
// settled it.
func (s *Service) SettleRefund(ctx context.Context, invoiceID, refundKey string, made bool, stripeRefundID, by string) error {
	if err := s.ready(); err != nil {
		return err
	}
	by = strings.TrimSpace(by)
	switch {
	case invoiceID == "" || !RefundKeyPattern.MatchString(refundKey):
		return ErrRefundNotFound
	case by == "":
		return errors.New("billing: settling a refund needs to say who did it")
	case made && !StripeRefundIDPattern.MatchString(stripeRefundID):
		return errors.New("billing: a refund settled as made needs the id Stripe gave it")
	}
	return pglock.Do(ctx, s.pg, refundLock+invoiceID, func(ctx context.Context) error {
		prev, found, err := s.storedRefund(ctx, refundKey)
		switch {
		case err != nil:
			return err
		case !found || prev.invoiceID != invoiceID:
			return ErrRefundNotFound
		case prev.sent:
			return ErrRefundSettled
		}
		if !made {
			if _, err = s.pg.Exec(ctx, `DELETE FROM loomplane_billing_refunds WHERE refund_key = $1 AND sent_at IS NULL`, refundKey); err != nil {
				return fmt.Errorf("billing: drop refund %s of invoice %s: %w", refundKey, invoiceID, err)
			}
			slog.WarnContext(ctx, "billing: an unconfirmed refund was settled as never made", "invoice", invoiceID, "org", prev.orgID, "by", by)
			return nil
		}
		iid, err := id.ParseInvoiceID(invoiceID)
		if err != nil {
			return ErrRefundNotFound
		}
		inv, err := s.l.Store().GetInvoice(ctx, iid)
		if err != nil {
			return fmt.Errorf("billing: read invoice %s: %w", invoiceID, err)
		}
		var confirmed int64
		if err = s.pg.QueryRow(ctx, `SELECT COALESCE(sum(cents), 0)::bigint FROM loomplane_billing_refunds
			WHERE invoice_id = $1 AND sent_at IS NOT NULL`, invoiceID).Scan(&confirmed); err != nil {
			return fmt.Errorf("billing: read the refunds of invoice %s: %w", invoiceID, err)
		}
		full := confirmed+prev.cents >= inv.Total.Amount
		if _, err = s.pg.Exec(ctx, `UPDATE loomplane_billing_refunds SET sent_at = $2, full_refund = $3 WHERE refund_key = $1 AND sent_at IS NULL`,
			refundKey, s.now().UTC(), full); err != nil {
			return fmt.Errorf("billing: settle refund %s of invoice %s: %w", refundKey, invoiceID, err)
		}
		slog.WarnContext(ctx, "billing: an unconfirmed refund was settled as made", "invoice", invoiceID, "org", prev.orgID,
			"stripe_refund", stripeRefundID, "by", by)
		return nil
	})
}
