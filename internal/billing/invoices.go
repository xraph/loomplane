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
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/subscription"

	"github.com/xraph/loomplane/internal/pglock"
)

// The advisory locks the month close and the invoice sync take.
const (
	// closeLock is held by the one replica closing a month.
	closeLock = "loomplane:billing:close"
	// creditsLock, per workspace, is held while the workspace is invoiced
	// and its used credits are deleted. Granting a credit takes it too, so
	// a credit lands wholly before an invoice or wholly after it.
	creditsLock = "loomplane:billing:credits:"
	// syncLock, per invoice, is held while the invoice is sent to Stripe.
	syncLock = "loomplane:billing:sync:"
)

// closeAfter is how long after a month ends its close runs: 01:15 UTC on
// the 1st, when the close cron fires. Platform Cloud emits a month's last
// hour about two minutes into the next, so by then all of it has arrived.
const closeAfter = 75 * time.Minute

// billedAccount is a workspace with a subscription to invoice.
type billedAccount struct {
	org, cloudSub, aiSub string
}

// CloseMonth bills the month that ended before now, in this order:
//  1. PushUsage, waiting for a push already running elsewhere, so every
//     event of the month is in ledger at its own timestamp;
//  2. ledger.Advance(now), so every subscription's period has rolled;
//  3. for each account, for its cloud subscription and then its ai
//     subscription: GenerateInvoice(sub, ForPeriod(prevStart, prevEnd)),
//     record it, FinalizeInvoice; for the cloud subscription, delete the
//     coupons of credits granted before generation;
//  4. roll the AI add-on over: cancel each ai subscription whose ai_ends_at
//     has passed, once its month is invoiced;
//  5. SyncInvoices.
//
// Every step is safe to run again: ErrAlreadyExists and ErrInvoiceFinalized
// are success, and the sync skips what has an id. A workspace whose usage
// for the month hasn't all reached ledger is left for the next run, so it
// is never invoiced short. Once every workspace is invoiced and rolled
// over, the month is recorded as closed; until then CloseMonthIfOpen runs
// the close again every hour. A replica that finds another closing returns
// at once.
func (s *Service) CloseMonth(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	ran, err := pglock.Try(ctx, s.pg, closeLock, s.closeMonth)
	if err == nil && !ran {
		slog.InfoContext(ctx, "billing: another replica is closing the month")
	}
	return err
}

// CloseMonthIfOpen runs CloseMonth when the month that ended before now
// has no record of its close and the close's own time has passed. The
// hourly sync job calls it, so a close that crashed or failed part way, or
// whose cron never fired, is finished within the hour.
func (s *Service) CloseMonthIfOpen(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	now := s.now().UTC()
	end := monthStart(now)
	if now.Before(end.Add(closeAfter)) {
		return nil // the close cron hasn't fired yet
	}
	start := end.AddDate(0, -1, 0)
	var closed bool
	if err := s.pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_billing_closes WHERE period = $1)`, start).
		Scan(&closed); err != nil {
		return fmt.Errorf("billing: read whether %s is closed: %w", periodKey(start), err)
	}
	if closed {
		return nil
	}
	slog.WarnContext(ctx, "billing: closing a month whose close hasn't finished", "month", periodKey(start))
	return s.CloseMonth(ctx)
}

// closeMonth is CloseMonth under the close lock.
func (s *Service) closeMonth(ctx context.Context) error {
	now := s.now().UTC()
	month := subscription.Period{Start: monthStart(now).AddDate(0, -1, 0), End: monthStart(now)}
	// errs fail the run; open also keep the month from being recorded as
	// closed, so the next hourly run tries again.
	var errs, open []error

	// 1. The month's usage into ledger.
	if _, err := s.push(ctx, "", true); err != nil {
		slog.ErrorContext(ctx, "billing: the month close's push didn't finish", "month", periodKey(month.Start), "error", err)
		errs = append(errs, err)
	}
	waiting, err := s.unpushed(ctx, month.End)
	if err != nil {
		return errors.Join(append(errs, err)...) // nobody can be invoiced without knowing
	}

	// 2. Every period rolled. One that didn't still bills the month: its
	// current period is the month, and GenerateInvoice bills that.
	if _, err = s.l.Advance(ctx, now); err != nil {
		slog.ErrorContext(ctx, "billing: ledger's clock didn't finish advancing", "error", err)
		errs = append(errs, fmt.Errorf("billing: advance ledger's clock: %w", err))
	}

	// 3. The invoices.
	accounts, err := s.billedAccounts(ctx, "")
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	invoiced := map[string]bool{}
	for _, a := range accounts {
		if ctxErr := ctx.Err(); ctxErr != nil {
			open = append(open, ctxErr)
			break
		}
		if waiting[a.org] {
			slog.WarnContext(ctx, "billing: a workspace waits for its usage to reach ledger before it is invoiced",
				"org", a.org, "month", periodKey(month.Start))
			open = append(open, fmt.Errorf("billing: %s isn't invoiced for %s: some of its usage hasn't reached ledger",
				a.org, periodKey(month.Start)))
			continue
		}
		if invErr := s.invoiceAccount(ctx, a, &month); invErr != nil {
			slog.ErrorContext(ctx, "billing: invoicing a workspace failed", "org", a.org, "month", periodKey(month.Start), "error", invErr)
			open = append(open, invErr)
			continue
		}
		invoiced[a.org] = true
	}

	// 4. The AI add-on rolls over, only where its month is invoiced.
	if rollErr := s.rollOverAI(ctx, now, invoiced); rollErr != nil {
		open = append(open, rollErr)
	}

	// 5. To Stripe. A sync that fails is retried by the hourly job, so it
	// doesn't hold the month open.
	if syncErr := s.SyncInvoices(ctx); syncErr != nil {
		errs = append(errs, syncErr)
	}

	if len(open) == 0 {
		if _, err = s.pg.Exec(ctx, `INSERT INTO loomplane_billing_closes (period, closed_at) VALUES ($1, $2)
			ON CONFLICT (period) DO NOTHING`, month.Start, now); err != nil {
			errs = append(errs, fmt.Errorf("billing: record the close of %s: %w", periodKey(month.Start), err))
		}
	}
	return errors.Join(append(errs, open...)...)
}

// unpushed lists the workspaces with usage from before end that isn't in
// ledger yet: rows not batched, or batched and not sent.
func (s *Service) unpushed(ctx context.Context, end time.Time) (map[string]bool, error) {
	rows, err := s.pg.Query(ctx, `SELECT org_id FROM loomplane_billing_usage WHERE push_id IS NULL AND occurred_at < $1
		UNION SELECT org_id FROM loomplane_billing_pushes WHERE sent_at IS NULL AND period < $2`, end, end)
	if err != nil {
		return nil, fmt.Errorf("billing: list the usage not in ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var org string
		if err = rows.Scan(&org); err != nil {
			return nil, fmt.Errorf("billing: list the usage not in ledger: %w", err)
		}
		out[org] = true
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: list the usage not in ledger: %w", err)
	}
	return out, nil
}

// billedAccounts lists the accounts holding a subscription, of one
// workspace (org) or of every one (""). A workspace deleted since keeps its
// row, so its last month is still invoiced.
func (s *Service) billedAccounts(ctx context.Context, org string) ([]billedAccount, error) {
	rows, err := s.pg.Query(ctx, `SELECT org_id, cloud_subscription_id, ai_subscription_id FROM loomplane_billing_accounts
		WHERE (cloud_subscription_id <> '' OR ai_subscription_id <> '') AND ($1 = '' OR org_id = $1) ORDER BY org_id`, org)
	if err != nil {
		return nil, fmt.Errorf("billing: list the accounts to invoice: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []billedAccount
	for rows.Next() {
		var a billedAccount
		if err = rows.Scan(&a.org, &a.cloudSub, &a.aiSub); err != nil {
			return nil, fmt.Errorf("billing: list the accounts to invoice: %w", err)
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: list the accounts to invoice: %w", err)
	}
	return out, nil
}

// invoiceAccount invoices the workspace's cloud subscription, then its ai
// subscription, for month, or for each one's current period when month is
// nil, under the workspace's credits lock. Both are tried; the errors come
// back together.
func (s *Service) invoiceAccount(ctx context.Context, a billedAccount, month *subscription.Period) error {
	return pglock.Do(ctx, s.pg, creditsLock+a.org, func(ctx context.Context) error {
		var errs []error
		for _, sub := range []struct{ id, plan string }{{a.cloudSub, PlanCloud}, {a.aiSub, PlanAI}} {
			if sub.id == "" {
				continue
			}
			if err := s.invoiceSubscription(ctx, a.org, sub.id, sub.plan, month); err != nil {
				errs = append(errs, fmt.Errorf("billing: invoice the %s subscription of %s: %w", sub.plan, a.org, err))
			}
		}
		return errors.Join(errs...)
	})
}

// invoiceSubscription makes, records and finalizes one subscription's
// invoice for month (nil: its current period, usage to now), and for the
// cloud subscription deletes the credits it used. Any step done before, by
// a run that crashed, is found and not done again.
func (s *Service) invoiceSubscription(ctx context.Context, org, subID, planSlug string, month *subscription.Period) error {
	sid, err := id.ParseSubscriptionID(subID)
	if err != nil {
		return fmt.Errorf("read subscription id %q: %w", subID, err)
	}
	sub, err := s.l.GetSubscription(ctx, sid)
	if err != nil {
		return fmt.Errorf("read subscription %s: %w", subID, err)
	}
	var (
		opts  []ledger.InvoiceOption
		start = sub.CurrentPeriodStart.UTC()
	)
	if month != nil {
		if !billsIn(sub, *month) {
			return nil
		}
		opts, start = []ledger.InvoiceOption{ledger.ForPeriod(month.Start, month.End)}, month.Start
	}

	inv, err := s.l.GenerateInvoice(ctx, sid, opts...)
	if errors.Is(err, ledger.ErrAlreadyExists) {
		inv, err = s.findInvoice(ctx, org, sid, start)
	}
	if err != nil {
		return fmt.Errorf("generate the invoice from %s: %w", start.Format(time.RFC3339), err)
	}
	if _, err = s.pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices (invoice_id, org_id, subscription_id, plan, period_start)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (invoice_id) DO NOTHING`,
		inv.ID.String(), org, subID, planSlug, inv.PeriodStart.UTC()); err != nil {
		return fmt.Errorf("record invoice %s: %w", inv.ID, err)
	}
	if err = s.l.FinalizeInvoice(ctx, inv.ID); err != nil && !errors.Is(err, ledger.ErrInvoiceFinalized) {
		return fmt.Errorf("finalize invoice %s: %w", inv.ID, err)
	}
	if _, err = s.pg.Exec(ctx, `UPDATE loomplane_billing_invoices SET finalized_at = $2 WHERE invoice_id = $1 AND finalized_at IS NULL`,
		inv.ID.String(), s.now().UTC()); err != nil {
		return fmt.Errorf("record that invoice %s is final: %w", inv.ID, err)
	}
	if planSlug == PlanCloud {
		return s.deleteUsedCredits(ctx, org, subID, inv)
	}
	return nil
}

// billsIn says whether the subscription had any of month: it was made
// before the month ended (a card added today had no period last month), and
// it wasn't canceled or ended before the month began. A canceled one whose
// period stopped at the month's start, before its clock rolled, has no
// period in the month either.
func billsIn(sub *subscription.Subscription, month subscription.Period) bool {
	if !sub.CreatedAt.Before(month.End) {
		return false
	}
	if sub.Status != subscription.StatusCanceled && sub.Status != subscription.StatusExpired {
		return true
	}
	ended := sub.CurrentPeriodEnd
	switch {
	case sub.CanceledAt != nil:
		ended = *sub.CanceledAt
	case sub.EndedAt != nil:
		ended = *sub.EndedAt
	}
	return ended.After(month.Start) && sub.CurrentPeriodEnd.After(month.Start)
}

// findInvoice is the live invoice ledger already holds for the subscription's
// period from start: the one recorded, or, after a crash between generating
// and recording it, the one in ledger's list.
func (s *Service) findInvoice(ctx context.Context, org string, sid id.SubscriptionID, start time.Time) (*invoice.Invoice, error) {
	var invID string
	err := s.pg.QueryRow(ctx, `SELECT invoice_id FROM loomplane_billing_invoices WHERE subscription_id = $1 AND period_start = $2`,
		sid.String(), start).Scan(&invID)
	switch {
	case err == nil:
		iid, parseErr := id.ParseInvoiceID(invID)
		if parseErr != nil {
			return nil, fmt.Errorf("read invoice id %q: %w", invID, parseErr)
		}
		return s.l.Store().GetInvoice(ctx, iid)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("find the recorded invoice: %w", err)
	}
	invs, err := s.l.Store().ListInvoices(ctx, org, s.appID, invoice.ListOpts{})
	if err != nil {
		return nil, fmt.Errorf("list the invoices of %s: %w", org, err)
	}
	for _, inv := range invs {
		if inv.SubscriptionID == sid && inv.PeriodStart.Equal(start) && inv.Status != invoice.StatusVoided {
			return inv, nil
		}
	}
	return nil, fmt.Errorf("ledger says an invoice covers the period from %s, and none is listed", start.Format(time.RFC3339))
}

// deleteUsedCredits deletes the coupon of each credit granted before inv
// was generated, which inv used, and records it against inv. The caller
// holds the workspace's credits lock, so no credit is granted meanwhile. A
// credit an earlier run marked and didn't delete is finished.
func (s *Service) deleteUsedCredits(ctx context.Context, org, subID string, inv *invoice.Invoice) error {
	rows, err := s.pg.Query(ctx, `SELECT id, coupon_id FROM loomplane_billing_credits
		WHERE org_id = $1 AND subscription_id = $2 AND deleted_at IS NULL
			AND ((invoice_id IS NULL AND created_at < $3) OR invoice_id = $4)
		ORDER BY created_at, id`, org, subID, inv.CreatedAt, inv.ID.String())
	if err != nil {
		return fmt.Errorf("list the credits invoice %s used: %w", inv.ID, err)
	}
	type credit struct{ id, coupon string }
	var used []credit
	for rows.Next() {
		var c credit
		if err = rows.Scan(&c.id, &c.coupon); err != nil {
			_ = rows.Close()
			return fmt.Errorf("list the credits invoice %s used: %w", inv.ID, err)
		}
		used = append(used, c)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("list the credits invoice %s used: %w", inv.ID, err)
	}

	var errs []error
	for _, c := range used {
		if err = s.deleteCredit(ctx, c.id, c.coupon, inv.ID.String()); err != nil {
			errs = append(errs, fmt.Errorf("delete credit %s: %w", c.id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) deleteCredit(ctx context.Context, creditID, couponID, invID string) error {
	if _, err := s.pg.Exec(ctx, `UPDATE loomplane_billing_credits SET invoice_id = $2 WHERE id = $1 AND invoice_id IS NULL`,
		creditID, invID); err != nil {
		return err
	}
	cid, err := id.ParseCouponID(couponID)
	if err != nil {
		return fmt.Errorf("read coupon id %q: %w", couponID, err)
	}
	// Not found is a delete an earlier run made before it crashed.
	if err = s.l.DeleteCoupon(ctx, cid); err != nil && !errors.Is(err, ledger.ErrCouponNotFound) {
		return err
	}
	_, err = s.pg.Exec(ctx, `UPDATE loomplane_billing_credits SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`,
		creditID, s.now().UTC())
	return err
}

// rollOverAI cancels, in ledger, each ai subscription whose add-on was
// turned off and whose last month has ended, then clears it from the
// account. Only workspaces in invoiced are rolled over: an add-on whose
// month isn't invoiced yet stays, so the next close still invoices it.
func (s *Service) rollOverAI(ctx context.Context, now time.Time, invoiced map[string]bool) error {
	rows, err := s.pg.Query(ctx, `SELECT org_id, ai_subscription_id FROM loomplane_billing_accounts
		WHERE ai_subscription_id <> '' AND ai_ends_at IS NOT NULL AND ai_ends_at <= $1 ORDER BY org_id`, now)
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
		if !invoiced[e.org] {
			continue
		}
		if rollErr := s.endAI(ctx, e.org, e.sub, now); rollErr != nil {
			slog.ErrorContext(ctx, "billing: rolling the AI add-on over failed", "org", e.org, "error", rollErr)
			errs = append(errs, fmt.Errorf("billing: roll over the AI add-on of %s: %w", e.org, rollErr))
		}
	}
	return errors.Join(errs...)
}

// endAI cancels one ai subscription now and clears it from the account,
// unless the account moved on to another meanwhile.
func (s *Service) endAI(ctx context.Context, org, subID string, now time.Time) error {
	sid, err := id.ParseSubscriptionID(subID)
	if err != nil {
		return fmt.Errorf("read subscription id %q: %w", subID, err)
	}
	err = s.l.CancelSubscription(ctx, sid, true)
	if err != nil && !errors.Is(err, ledger.ErrSubscriptionCanceled) && !errors.Is(err, ledger.ErrSubscriptionExpired) {
		return err
	}
	defer s.forget(org)
	_, err = s.pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id = '', ai_ends_at = NULL, updated_at = $3
		WHERE org_id = $1 AND ai_subscription_id = $2 AND ai_ends_at IS NOT NULL AND ai_ends_at <= $3`, org, subID, now)
	return err
}

// SyncInvoices sends each finalized invoice with no provider id to Stripe,
// one at a time under a per-invoice advisory lock, rereading the invoice
// inside the lock and skipping one that has a provider id by then. An
// invoice of zero is marked paid by ledger with nothing sent. One invoice
// failing doesn't hold the others back; the errors come back together.
func (s *Service) SyncInvoices(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	rows, err := s.pg.Query(ctx, `SELECT invoice_id FROM loomplane_billing_invoices
		WHERE finalized_at IS NOT NULL AND provider_id = '' AND paid_at IS NULL ORDER BY period_start, invoice_id`)
	if err != nil {
		return fmt.Errorf("billing: list the invoices to send: %w", err)
	}
	var due []string
	for rows.Next() {
		var invID string
		if err = rows.Scan(&invID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("billing: list the invoices to send: %w", err)
		}
		due = append(due, invID)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("billing: list the invoices to send: %w", err)
	}

	var errs []error
	for _, invID := range due {
		if ctxErr := ctx.Err(); ctxErr != nil {
			errs = append(errs, ctxErr)
			break
		}
		// Another replica holding the lock is sending it.
		if _, syncErr := pglock.Try(ctx, s.pg, syncLock+invID, func(ctx context.Context) error {
			return s.syncInvoice(ctx, invID)
		}); syncErr != nil {
			slog.ErrorContext(ctx, "billing: sending an invoice to Stripe failed; the hourly sync retries", "invoice", invID, "error", syncErr)
			errs = append(errs, fmt.Errorf("billing: send invoice %s: %w", invID, syncErr))
		}
	}
	return errors.Join(errs...)
}

// syncInvoice sends one invoice, under its sync lock.
func (s *Service) syncInvoice(ctx context.Context, invID string) error {
	var (
		providerID string
		paidAt     *time.Time
	)
	if err := s.pg.QueryRow(ctx, `SELECT provider_id, paid_at FROM loomplane_billing_invoices WHERE invoice_id = $1`, invID).
		Scan(&providerID, &paidAt); err != nil {
		return err
	}
	if providerID != "" || paidAt != nil {
		return nil // sent since the list was read
	}
	iid, err := id.ParseInvoiceID(invID)
	if err != nil {
		return fmt.Errorf("read invoice id %q: %w", invID, err)
	}
	inv, err := s.l.Store().GetInvoice(ctx, iid)
	if err != nil {
		return err
	}
	switch {
	case inv.ProviderID != "":
		// Sent by a run that stopped before recording it here.
		return s.recordProvider(ctx, invID, inv.ProviderID)
	case inv.Status == invoice.StatusPaid:
		// A zero total a run settled before it stopped.
		at := s.now().UTC()
		if inv.PaidAt != nil {
			at = inv.PaidAt.UTC()
		}
		return s.recordPaid(ctx, invID, at)
	case inv.Status == invoice.StatusVoided:
		slog.WarnContext(ctx, "billing: a voided invoice is never sent", "invoice", invID)
		return nil
	}
	res, err := s.l.SyncInvoiceToProvider(ctx, iid)
	if err != nil {
		return err
	}
	if res != nil && res.ProviderID != "" {
		if err := s.recordProvider(ctx, invID, res.ProviderID); err != nil {
			return err
		}
	}
	if inv.Total.IsZero() {
		// Ledger marked it paid without sending it (ledger.go:1834).
		return s.recordPaid(ctx, invID, s.now().UTC())
	}
	return nil
}

func (s *Service) recordProvider(ctx context.Context, invID, providerID string) error {
	_, err := s.pg.Exec(ctx, `UPDATE loomplane_billing_invoices SET provider_id = $2 WHERE invoice_id = $1 AND provider_id = ''`,
		invID, providerID)
	return err
}

func (s *Service) recordPaid(ctx context.Context, invID string, at time.Time) error {
	_, err := s.pg.Exec(ctx, `UPDATE loomplane_billing_invoices SET paid_at = $2 WHERE invoice_id = $1 AND paid_at IS NULL`,
		invID, at)
	return err
}
