//go:build cloud

package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/provider"

	"github.com/xraph/loomplane/internal/platformcloud"
)

// providerStripe is the name ledger knows the Stripe provider by.
const providerStripe = "stripe"

// What HandleStripeWebhook refuses before anything is applied. Both are a
// 400: Stripe resending the same bytes can't make them pass.
var (
	// ErrBadSignature is an event whose Stripe-Signature doesn't match the
	// webhook secret, is missing or is too old.
	ErrBadSignature = errors.New("billing: the Stripe signature doesn't match")
	// ErrBadEvent is a signed event Loomplane can't read.
	ErrBadEvent = errors.New("billing: the Stripe event can't be read")
)

// HandleStripeWebhook verifies the signature, lets ledger map the event,
// and applies what it means for the workspace. A bad signature is
// ErrBadSignature (400). An event for a workspace or invoice Loomplane
// doesn't know is logged and applied as nothing (200).
//
// Any other error means the event wasn't applied, and the route answers
// 500 so Stripe sends it again. Every event is safe to apply twice.
func (s *Service) HandleStripeWebhook(ctx context.Context, payload []byte, signature string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if s.payments == nil {
		return errors.New("billing: nothing to verify Stripe events with")
	}
	if err := s.payments.Verify(payload, signature); err != nil {
		return fmt.Errorf("%w: %w", ErrBadSignature, err)
	}
	res, err := s.l.HandleWebhook(ctx, providerStripe, payload)
	if err != nil {
		if unreadable(err) {
			return fmt.Errorf("%w: %w", ErrBadEvent, err)
		}
		return fmt.Errorf("billing: map a Stripe event: %w", err)
	}
	if res == nil {
		return nil
	}
	return s.applyWebhook(ctx, res)
}

// unreadable says whether the provider failed because the event itself is
// malformed, not because Stripe or the database didn't answer.
func unreadable(err error) bool {
	var (
		syntax *json.SyntaxError
		typ    *json.UnmarshalTypeError
	)
	return errors.As(err, &syntax) || errors.As(err, &typ) || strings.Contains(err.Error(), "has no data")
}

// applyWebhook applies one event ledger mapped. Only what the event says
// is written, and each write is safe to repeat: a card already ready, an
// invoice already paid and a failure already recorded change nothing.
func (s *Service) applyWebhook(ctx context.Context, res *provider.WebhookResult) error {
	if res == nil || !res.Handled {
		return nil
	}
	at := res.Timestamp.UTC()
	if res.Timestamp.IsZero() {
		at = s.now().UTC()
	}
	switch {
	case res.EntityType == "tenant":
		return s.cardSaved(ctx, res.EntityID, res.EventType)
	case res.EventType == "invoice.paid":
		return s.invoicePaid(ctx, res.EntityID, res.ProviderID, at)
	case res.EventType == "invoice.payment_failed":
		return s.paymentFailed(ctx, res.EntityID, at)
	}
	slog.InfoContext(ctx, "billing: a Stripe event needs nothing from billing", "event", res.EventType, "entity", res.EntityType)
	return nil
}

// cardSaved records a card saved through the setup page, for a workspace
// that still exists. A deleted workspace's event changes nothing.
func (s *Service) cardSaved(ctx context.Context, orgID, event string) error {
	if orgID == "" {
		slog.WarnContext(ctx, "billing: a Stripe card event names no workspace", "event", event)
		return nil
	}
	var exists bool
	if err := s.pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM authsome_organizations WHERE id = $1 AND app_id = $2)`,
		orgID, s.appID).Scan(&exists); err != nil {
		return fmt.Errorf("billing: read whether workspace %s exists: %w", orgID, err)
	}
	if !exists {
		slog.WarnContext(ctx, "billing: a card was saved for a workspace that no longer exists; nothing changes",
			"org", orgID, "event", event)
		return nil
	}
	return s.MarkCardReady(ctx, orgID)
}

// invoicePaid records a paid invoice in ledger and here, then evaluates
// the workspace, which lifts past_due once nothing else holds it.
func (s *Service) invoicePaid(ctx context.Context, invID, stripeID string, at time.Time) error {
	iid, err := id.ParseInvoiceID(invID)
	if err != nil {
		slog.WarnContext(ctx, "billing: Stripe paid an invoice whose id isn't ledger's; nothing changes", "invoice", invID)
		return nil
	}
	err = s.l.MarkInvoicePaid(ctx, iid, at, stripeID)
	switch {
	case err == nil, errors.Is(err, ledger.ErrInvoicePaid):
		// Paid now, or by a delivery before this one.
	case errors.Is(err, ledger.ErrInvoiceNotFound):
		slog.WarnContext(ctx, "billing: Stripe paid an invoice ledger doesn't have; nothing changes", "invoice", invID)
		return nil
	case errors.Is(err, ledger.ErrInvoiceVoided):
		slog.ErrorContext(ctx, "billing: Stripe paid an invoice that is voided; refund it in Stripe if that's wrong",
			"invoice", invID, "stripe_invoice", stripeID)
		return nil
	default:
		return fmt.Errorf("billing: mark invoice %s paid: %w", invID, err)
	}

	// The first time recorded wins, so a replay keeps it.
	var (
		orgID    string
		failedAt *time.Time
		paidAt   time.Time
	)
	err = s.pg.QueryRow(ctx, `UPDATE loomplane_billing_invoices SET paid_at = COALESCE(paid_at, $2)
		WHERE invoice_id = $1 RETURNING org_id, payment_failed_at, paid_at`, invID, at).Scan(&orgID, &failedAt, &paidAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.WarnContext(ctx, "billing: Stripe paid an invoice Loomplane never recorded; only ledger changes", "invoice", invID)
			return nil
		}
		return fmt.Errorf("billing: record that invoice %s is paid: %w", invID, err)
	}
	s.forget(orgID)
	// A state that can't be settled fails the event, so Stripe sends it
	// again. A platform Cloud restore that didn't finish doesn't: the
	// state is right, and the next evaluation, within 15 minutes, retries it.
	effects, err := s.evaluate(ctx, orgID)
	if err != nil {
		return fmt.Errorf("billing: evaluate %s after invoice %s was paid: %w", orgID, invID, err)
	}
	if effects != nil {
		slog.WarnContext(ctx, "billing: platform Cloud didn't finish restoring a paid workspace; the next evaluation retries",
			"org", orgID, "invoice", invID, "error", effects)
	}
	// This invoice held the workspace past due when it was paid.
	if failedAt != nil && !failedAt.After(paidAt.Add(-pastDueAfter)) {
		return s.pastDueCleared(ctx, orgID, invID)
	}
	return nil
}

// pastDueCleared tells the admins of a workspace with no platform tenant
// that AI on Loomplane's keys is back once a paid invoice lifted past_due.
// Platform Cloud tells everyone else when their work restarts. The notice
// is made once per invoice, so a replay says nothing twice.
func (s *Service) pastDueCleared(ctx context.Context, orgID, invID string) error {
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return err
	}
	if a.Status != StatusActive || !a.AIEnabled {
		return nil // still held, or there is no AI to bring back
	}
	// Restoring a reason that isn't there changes nothing, and is how a
	// workspace with no tenant is told apart: platform Cloud says so.
	if !errors.Is(s.platform(ctx, orgID, platformcloud.ReasonPastDue, false), platformcloud.ErrNotEnabled) {
		return nil
	}
	s.sendNotice(ctx, orgID, NoticeRestored, "paid:"+invID,
		"Payment received",
		"The unpaid invoice is paid. AI on Loomplane's model keys is back on.")
	return nil
}

// paymentFailed records the first time Stripe couldn't charge an invoice
// still unpaid, and tells the admins once. A failure after the invoice was
// paid, or for an invoice Loomplane doesn't know, changes nothing.
func (s *Service) paymentFailed(ctx context.Context, invID string, at time.Time) error {
	iid, err := id.ParseInvoiceID(invID)
	if err != nil {
		slog.WarnContext(ctx, "billing: Stripe failed to charge an invoice whose id isn't ledger's; nothing changes", "invoice", invID)
		return nil
	}
	inv, err := s.l.Store().GetInvoice(ctx, iid)
	if errors.Is(err, ledger.ErrInvoiceNotFound) {
		slog.WarnContext(ctx, "billing: Stripe failed to charge an invoice ledger doesn't have; nothing changes", "invoice", invID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("billing: read invoice %s: %w", invID, err)
	}
	if inv.Status == invoice.StatusPaid || inv.Status == invoice.StatusVoided {
		return nil // paid or voided already; this failure came late
	}
	var orgID string
	err = s.pg.QueryRow(ctx, `UPDATE loomplane_billing_invoices SET payment_failed_at = COALESCE(payment_failed_at, $2)
		WHERE invoice_id = $1 AND paid_at IS NULL AND voided_at IS NULL RETURNING org_id`, invID, at).Scan(&orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.WarnContext(ctx, "billing: Stripe failed to charge an invoice Loomplane didn't record, or settled since; nothing changes",
				"invoice", invID)
			return nil
		}
		return fmt.Errorf("billing: record that invoice %s failed to charge: %w", invID, err)
	}
	s.forget(orgID)
	s.sendNotice(ctx, orgID, NoticePaymentFailed, "payment_failed:"+invID,
		"A payment for Loomplane Cloud failed",
		"Stripe couldn't charge the card for this workspace's invoice. Update the card in Settings > Billing. If the invoice is still unpaid in 3 days, Loomplane Cloud work and AI on Loomplane's model keys stop.")
	return nil
}
