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

// closeLookBack is how many months before the current one the hourly
// catch-up closes when their close didn't finish.
const closeLookBack = 3

// closeAfter is how long after a month ends its close runs: 01:15 UTC on
// the 1st, when the close cron fires. Platform Cloud emits a month's last
// hour about two minutes into the next, so by then all of it has arrived.
const closeAfter = 75 * time.Minute

// billedAccount is a workspace with a subscription to invoice.
type billedAccount struct {
	org, cloudSub, aiSub string
	aiEndsAt             *time.Time // the add-on was turned off and runs until then
}

// CloseMonth bills the month that ended before now, in this order:
//  1. PriceAIClaims, then PushUsage, waiting for a push already running
//     elsewhere, so every event of the month is in ledger at its own
//     timestamp;
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
	now := s.now().UTC()
	month := subscription.Period{Start: monthStart(now).AddDate(0, -1, 0), End: monthStart(now)}
	ran, err := pglock.Try(ctx, s.pg, closeLock, func(ctx context.Context) error {
		return s.closeMonth(ctx, now, month)
	})
	if err == nil && !ran {
		slog.InfoContext(ctx, "billing: another replica is closing the month")
	}
	return err
}

// CloseMonthIfOpen closes, oldest first, each of the last three months
// that has no record of its close, once this month's close time has
// passed. The hourly sync job calls it, so a close that crashed or failed
// part way, or whose cron never fired, is finished within the hour, and a
// workspace held past the next 1st still gets its month invoiced. An open
// month older than that is logged as an error: it needs a person.
func (s *Service) CloseMonthIfOpen(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	now := s.now().UTC()
	current := monthStart(now)
	if now.Before(current.Add(closeAfter)) {
		return nil // this month's close cron hasn't fired yet
	}
	oldest := current.AddDate(0, -closeLookBack, 0)
	_, err := pglock.Try(ctx, s.pg, closeLock, func(ctx context.Context) error {
		var stale bool
		if err := s.pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM generate_series(
				(SELECT min(period) FROM loomplane_billing_closes)::timestamp, ($1::date - interval '1 month')::timestamp, interval '1 month') AS m
			WHERE NOT EXISTS (SELECT 1 FROM loomplane_billing_closes c WHERE c.period = m::date))`, oldest).Scan(&stale); err != nil {
			return fmt.Errorf("billing: look for months left open: %w", err)
		}
		if stale {
			slog.ErrorContext(ctx, "billing: a month older than the catch-up looks back is still open; close it by hand",
				"before", periodKey(oldest))
		}
		var errs []error
		for start := oldest; start.Before(current); start = start.AddDate(0, 1, 0) {
			var closed bool
			if err := s.pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_billing_closes WHERE period = $1)`, start).
				Scan(&closed); err != nil {
				errs = append(errs, fmt.Errorf("billing: read whether %s is closed: %w", periodKey(start), err))
				continue
			}
			if closed {
				continue
			}
			slog.WarnContext(ctx, "billing: closing a month whose close hasn't finished", "month", periodKey(start))
			// Each month on its own: one that stays open doesn't hold the
			// later ones back.
			errs = append(errs, s.closeMonth(ctx, now, subscription.Period{Start: start, End: start.AddDate(0, 1, 0)}))
		}
		return errors.Join(errs...)
	})
	return err
}

// closeMonth closes month as of now, under the close lock.
func (s *Service) closeMonth(ctx context.Context, now time.Time, month subscription.Period) error {
	// errs fail the run; open also keep the month from being recorded as
	// closed, so the next hourly run tries again.
	var errs, open []error

	// 1. The month's usage into ledger: AI on the server's key priced from
	// nexus's records first, so it is in the push. A pricing run that
	// fails fails the close, but doesn't hold the month open: a claim it
	// left is priced by the next run, as late usage.
	if err := s.PriceAIClaims(ctx); err != nil {
		slog.ErrorContext(ctx, "billing: the month close's AI pricing didn't finish", "month", periodKey(month.Start), "error", err)
		errs = append(errs, err)
	}
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

	// 4. The AI add-on rolls over, only where its last month is the one
	// just invoiced.
	if rollErr := s.rollOverAI(ctx, month.End, invoiced); rollErr != nil {
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
	rows, err := s.pg.Query(ctx, `SELECT org_id, cloud_subscription_id, ai_subscription_id, ai_ends_at FROM loomplane_billing_accounts
		WHERE (cloud_subscription_id <> '' OR ai_subscription_id <> '') AND ($1 = '' OR org_id = $1) ORDER BY org_id`, org)
	if err != nil {
		return nil, fmt.Errorf("billing: list the accounts to invoice: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []billedAccount
	for rows.Next() {
		var a billedAccount
		if err = rows.Scan(&a.org, &a.cloudSub, &a.aiSub, &a.aiEndsAt); err != nil {
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

	// A recorded invoice is resumed; a voided one is left alone, never made
	// again.
	inv, voided, err := s.recordedInvoice(ctx, sid, start)
	if err != nil || voided {
		return err // a voided one was said when it was found
	}
	if inv != nil && inv.Status == invoice.StatusVoided {
		slog.WarnContext(ctx, "billing: the invoice for this period was voided; it isn't made again",
			"org", org, "subscription", subID, "period_start", start.Format(time.RFC3339), "invoice", inv.ID.String())
		return s.recordVoided(ctx, inv.ID.String())
	}
	// The credits a new invoice uses are the coupons applied now, under
	// the credits lock, so no grant lands between this list and generation.
	var credits []credit
	if planSlug == PlanCloud {
		if credits, err = s.unusedCredits(ctx, org, subID); err != nil {
			return err
		}
	}
	created := false
	if inv == nil {
		inv, err = s.l.GenerateInvoice(ctx, sid, opts...)
		switch {
		case errors.Is(err, ledger.ErrAlreadyExists):
			// Generated by a run that crashed before recording it.
			inv, err = s.ledgerInvoice(ctx, org, sid, start)
		case err == nil:
			created = true
		}
		if err != nil {
			return fmt.Errorf("generate the invoice from %s: %w", start.Format(time.RFC3339), err)
		}
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
		if !created {
			// An invoice an earlier run made used only the credits applied
			// then; one granted since stays for the next invoice.
			if credits, err = s.creditsOn(ctx, sid, inv, credits); err != nil {
				return err
			}
		}
		return s.deleteUsedCredits(ctx, org, subID, inv, credits)
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

// recordedInvoice is the invoice Loomplane recorded for the subscription's
// period from start, as ledger has it now, or nil when there is none.
// voided says Loomplane already recorded it as voided.
func (s *Service) recordedInvoice(ctx context.Context, sid id.SubscriptionID, start time.Time) (inv *invoice.Invoice, voided bool, err error) {
	var invID string
	err = s.pg.QueryRow(ctx, `SELECT invoice_id, voided_at IS NOT NULL FROM loomplane_billing_invoices
		WHERE subscription_id = $1 AND period_start = $2`, sid.String(), start).Scan(&invID, &voided)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("find the recorded invoice: %w", err)
	case voided:
		return nil, true, nil
	}
	iid, err := id.ParseInvoiceID(invID)
	if err != nil {
		return nil, false, fmt.Errorf("read invoice id %q: %w", invID, err)
	}
	if inv, err = s.l.Store().GetInvoice(ctx, iid); err != nil {
		return nil, false, fmt.Errorf("read invoice %s: %w", invID, err)
	}
	return inv, false, nil
}

// ledgerInvoice is the live invoice ledger holds for the subscription's
// period from start that Loomplane never recorded: a crash came between
// generating and recording it.
func (s *Service) ledgerInvoice(ctx context.Context, org string, sid id.SubscriptionID, start time.Time) (*invoice.Invoice, error) {
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

// credit is a granted credit and its ledger coupon.
type credit struct{ id, coupon string }

// unusedCredits lists the workspace's credits on the subscription that no
// invoice has used yet.
func (s *Service) unusedCredits(ctx context.Context, org, subID string) ([]credit, error) {
	return s.listCredits(ctx, `SELECT id, coupon_id FROM loomplane_billing_credits
		WHERE org_id = $1 AND subscription_id = $2 AND invoice_id IS NULL AND deleted_at IS NULL
		ORDER BY created_at, id`, org, subID)
}

func (s *Service) listCredits(ctx context.Context, query string, args ...any) ([]credit, error) {
	rows, err := s.pg.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list the credits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []credit
	for rows.Next() {
		var c credit
		if err = rows.Scan(&c.id, &c.coupon); err != nil {
			return nil, fmt.Errorf("list the credits: %w", err)
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list the credits: %w", err)
	}
	return out, nil
}

// creditsOn narrows credits to those that discounted inv, an invoice an
// earlier run generated: the ones whose coupon has a discount line on it.
// Ledger writes each coupon's line as "Discount <code>" (ledger.go,
// GenerateInvoice step 5), and a credit's coupon is still applied until it
// is deleted below.
func (s *Service) creditsOn(ctx context.Context, sid id.SubscriptionID, inv *invoice.Invoice, credits []credit) ([]credit, error) {
	if len(credits) == 0 {
		return nil, nil
	}
	applied, err := s.l.ListAppliedCoupons(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("list the coupons applied to %s: %w", sid, err)
	}
	codes := map[string]string{}
	for _, c := range applied {
		codes[c.ID.String()] = c.Code
	}
	lines := map[string]bool{}
	for _, li := range inv.LineItems {
		if li.Type == invoice.LineItemDiscount {
			lines[li.Description] = true
		}
	}
	var out []credit
	for _, c := range credits {
		if code, ok := codes[c.coupon]; ok && lines["Discount "+code] {
			out = append(out, c)
		}
	}
	return out, nil
}

// deleteUsedCredits deletes the coupon of each credit inv used and records
// it against inv, with any credit an earlier run marked for inv and didn't
// delete. The caller holds the workspace's credits lock.
func (s *Service) deleteUsedCredits(ctx context.Context, org, subID string, inv *invoice.Invoice, used []credit) error {
	resumed, err := s.listCredits(ctx, `SELECT id, coupon_id FROM loomplane_billing_credits
		WHERE org_id = $1 AND subscription_id = $2 AND invoice_id = $3 AND deleted_at IS NULL
		ORDER BY created_at, id`, org, subID, inv.ID.String())
	if err != nil {
		return fmt.Errorf("list the credits invoice %s used: %w", inv.ID, err)
	}
	var errs []error
	for _, c := range append(resumed, used...) {
		if err = s.deleteCredit(ctx, c.id, c.coupon, inv.ID.String()); err != nil {
			errs = append(errs, fmt.Errorf("delete credit %s: %w", c.id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) deleteCredit(ctx context.Context, creditID, couponID, invID string) error {
	var mine bool
	if err := s.pg.QueryRow(ctx, `UPDATE loomplane_billing_credits SET invoice_id = $2 WHERE id = $1 AND (invoice_id IS NULL OR invoice_id = $2)
		RETURNING true`, creditID, invID).Scan(&mine); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("another invoice used credit %s", creditID)
	} else if err != nil {
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
		WHERE finalized_at IS NOT NULL AND provider_id = '' AND paid_at IS NULL AND voided_at IS NULL
		ORDER BY period_start, invoice_id`)
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
		providerID       string
		paidAt, voidedAt *time.Time
	)
	if err := s.pg.QueryRow(ctx, `SELECT provider_id, paid_at, voided_at FROM loomplane_billing_invoices WHERE invoice_id = $1`, invID).
		Scan(&providerID, &paidAt, &voidedAt); err != nil {
		return err
	}
	if providerID != "" || paidAt != nil || voidedAt != nil {
		return nil // sent, settled or voided since the list was read
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
		// Recorded, so the next sync leaves it out and this is said once.
		slog.WarnContext(ctx, "billing: a voided invoice is never sent", "invoice", invID)
		return s.recordVoided(ctx, invID)
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

func (s *Service) recordVoided(ctx context.Context, invID string) error {
	_, err := s.pg.Exec(ctx, `UPDATE loomplane_billing_invoices SET voided_at = $2 WHERE invoice_id = $1 AND voided_at IS NULL`,
		invID, s.now().UTC())
	return err
}
