//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/subscription"
)

// ErrNeedsPayment is SetAI's refusal to turn the add-on on for a workspace
// with no card yet, or with a failed payment still unpaid.
var ErrNeedsPayment = errors.New("billing: the AI add-on needs a card on file and no unpaid invoice")

// SetAI turns the AI add-on on or off and returns the account as it is
// after.
//
// On: an ai subscription the account still holds, including one turned off
// and running to the month's end, keeps going: ai_ends_at is cleared and no
// second subscription is made, so the month is paid for once and its credit
// granted once. With none, one is made on the current calendar month,
// active, with no trial, and the full $10 is due (no proration). Turning it
// on is refused with ErrNeedsPayment while the workspace is pending_payment
// or past_due.
//
// Off: ai_ends_at is set to 00:00 UTC on the 1st of next month. AI stays on
// until then, and the month close cancels the subscription once that month
// is invoiced. Turning off is never refused, so a workspace that owes money
// can still stop next month's $10. Off on an add-on already turned off, or
// on a workspace without one, changes nothing.
//
// Every call drops the gate's cached answer, so the next AI call on this
// replica reads the account again.
func (s *Service) SetAI(ctx context.Context, orgID string, enabled bool) (Account, error) {
	if err := s.ready(); err != nil {
		return Account{}, err
	}
	if orgID == "" {
		return Account{}, errors.New("billing: the AI add-on needs a workspace")
	}
	defer s.forget(orgID)
	if enabled {
		a, err := s.Account(ctx, orgID)
		if err != nil {
			return Account{}, err
		}
		if a.Status == StatusPendingPayment || a.Status == StatusPastDue {
			return Account{}, fmt.Errorf("%w: %s is %s", ErrNeedsPayment, orgID, a.Status)
		}
		if onErr := s.aiOn(ctx, orgID); onErr != nil {
			return Account{}, onErr
		}
	} else if err := s.aiOff(ctx, orgID); err != nil {
		return Account{}, err
	}
	return s.Account(ctx, orgID)
}

// aiOn keeps or makes the workspace's ai subscription. The account row stays
// locked while ledger is read and written, so two calls make one
// subscription, and the month close's rollover (endAI, which locks the row
// too) either ends the subscription before this call reads it or sees
// ai_ends_at cleared and leaves it.
func (s *Service) aiOn(ctx context.Context, orgID string) error {
	tx, err := s.pg.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billing: begin turning on the AI add-on of %s: %w", orgID, err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a no-op after commit

	var (
		subID  string
		endsAt *time.Time
	)
	err = tx.QueryRow(ctx, `SELECT ai_subscription_id, ai_ends_at FROM loomplane_billing_accounts WHERE org_id = $1 FOR UPDATE`,
		orgID).Scan(&subID, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s has no billing account", ErrNeedsPayment, orgID)
	}
	if err != nil {
		return fmt.Errorf("billing: read the AI add-on of %s: %w", orgID, err)
	}
	now := s.now().UTC()
	if subID != "" {
		live, liveErr := s.subscriptionLive(ctx, subID)
		if liveErr != nil {
			return liveErr
		}
		if live {
			if endsAt != nil {
				if _, err = tx.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_ends_at = NULL, updated_at = $2 WHERE org_id = $1`,
					orgID, now); err != nil {
					return fmt.Errorf("billing: turn the AI add-on of %s back on: %w", orgID, err)
				}
			}
			if err = tx.Commit(); err != nil {
				return fmt.Errorf("billing: commit the AI add-on of %s: %w", orgID, err)
			}
			return nil
		}
		// Ended in ledger by a rollover that stopped before it cleared the
		// account. That month was invoiced before it was ended, so a new
		// subscription bills this month and nothing twice.
		slog.WarnContext(ctx, "billing: the account's ai subscription has ended in ledger; starting a new one",
			"org", orgID, "subscription", subID)
	}
	newID, err := s.liveSubscription(ctx, orgID, PlanAI)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id = $2, ai_ends_at = NULL, updated_at = $3
		WHERE org_id = $1`, orgID, newID, now); err != nil {
		return fmt.Errorf("billing: record the ai subscription of %s: %w", orgID, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("billing: commit the AI add-on of %s: %w", orgID, err)
	}
	return nil
}

// subscriptionLive says whether ledger's subscription is still running:
// neither canceled nor expired.
func (s *Service) subscriptionLive(ctx context.Context, subID string) (bool, error) {
	sid, err := id.ParseSubscriptionID(subID)
	if err != nil {
		return false, fmt.Errorf("billing: read subscription id %q: %w", subID, err)
	}
	sub, err := s.l.GetSubscription(ctx, sid)
	if err != nil {
		return false, fmt.Errorf("billing: read subscription %s: %w", subID, err)
	}
	return sub.Status != subscription.StatusCanceled && sub.Status != subscription.StatusExpired, nil
}

// aiOff dates the end of a running add-on to the start of next month. An
// end already set is kept, so an add-on whose month is over and waits for
// its rollover isn't carried into another month.
func (s *Service) aiOff(ctx context.Context, orgID string) error {
	now := s.now().UTC()
	if _, err := s.pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_ends_at = $2, updated_at = $3
		WHERE org_id = $1 AND ai_subscription_id <> '' AND ai_ends_at IS NULL`,
		orgID, monthStart(now).AddDate(0, 1, 0), now); err != nil {
		return fmt.Errorf("billing: turn off the AI add-on of %s: %w", orgID, err)
	}
	return nil
}

// rollOverAI cancels, in ledger, each ai subscription whose add-on was
// turned off and ended by cutoff, the end of the month just closed, then
// clears it from the account. An add-on is rolled over only once its last
// month (the one ending at ai_ends_at) has a recorded invoice that is final
// or voided, whichever close made it: one whose last month isn't invoiced
// yet stays, so the close of that month still invoices it, however late.
func (s *Service) rollOverAI(ctx context.Context, cutoff time.Time) error {
	rows, err := s.pg.Query(ctx, `SELECT org_id, ai_subscription_id FROM loomplane_billing_accounts
		WHERE ai_subscription_id <> '' AND ai_ends_at IS NOT NULL AND ai_ends_at <= $1 ORDER BY org_id`, cutoff)
	if err != nil {
		return fmt.Errorf("billing: list the AI add-ons to roll over: %w", err)
	}
	type ending struct{ org, sub string }
	var due []ending
	for rows.Next() {
		var e ending
		if err = rows.Scan(&e.org, &e.sub); err != nil {
			_ = rows.Close()
			return fmt.Errorf("billing: list the AI add-ons to roll over: %w", err)
		}
		due = append(due, e)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("billing: list the AI add-ons to roll over: %w", err)
	}

	var errs []error
	for _, e := range due {
		if rollErr := s.endAI(ctx, e.org, e.sub, cutoff); rollErr != nil {
			slog.ErrorContext(ctx, "billing: rolling the AI add-on over failed", "org", e.org, "error", rollErr)
			errs = append(errs, fmt.Errorf("billing: roll over the AI add-on of %s: %w", e.org, rollErr))
		}
	}
	return errors.Join(errs...)
}

// endAI cancels one ai subscription now and clears it from the account. The
// account row is locked first and read again, so an add-on turned back on
// since the list was read (ai_ends_at cleared) or moved to another
// subscription is left running. So is one whose last month has no final
// invoice yet.
func (s *Service) endAI(ctx context.Context, org, subID string, cutoff time.Time) error {
	sid, err := id.ParseSubscriptionID(subID)
	if err != nil {
		return fmt.Errorf("read subscription id %q: %w", subID, err)
	}
	defer s.forget(org)
	tx, err := s.pg.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a no-op after commit
	var (
		due    bool
		endsAt *time.Time
	)
	err = tx.QueryRow(ctx, `SELECT ai_subscription_id = $2 AND ai_ends_at IS NOT NULL AND ai_ends_at <= $3, ai_ends_at
		FROM loomplane_billing_accounts WHERE org_id = $1 FOR UPDATE`, org, subID, cutoff).Scan(&due, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !due) {
		return nil
	}
	if err != nil {
		return err
	}
	last := monthStart(endsAt.UTC().Add(-time.Nanosecond))
	var invoiced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_billing_invoices
		WHERE subscription_id = $1 AND period_start = $2 AND (finalized_at IS NOT NULL OR voided_at IS NOT NULL))`,
		subID, last).Scan(&invoiced); err != nil {
		return fmt.Errorf("find the invoice for the add-on's last month: %w", err)
	}
	if !invoiced {
		slog.InfoContext(ctx, "billing: the AI add-on's last month isn't invoiced yet; it rolls over after",
			"org", org, "subscription", subID, "month", periodKey(last))
		return nil
	}
	err = s.l.CancelSubscription(ctx, sid, true)
	if err != nil && !errors.Is(err, ledger.ErrSubscriptionCanceled) && !errors.Is(err, ledger.ErrSubscriptionExpired) {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id = '', ai_ends_at = NULL, updated_at = $2
		WHERE org_id = $1`, org, s.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}
