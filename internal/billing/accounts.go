//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ledger/plan"
	"github.com/xraph/ledger/subscription"
)

// Status is where a workspace's billing stands. It is computed each time
// it is read, never stored.
type Status string

const (
	// StatusPendingPayment is a workspace with no card yet: nothing runs.
	StatusPendingPayment Status = "pending_payment"
	// StatusActive is a workspace with a card and nothing owing.
	StatusActive Status = "active"
	// StatusPastDue is a workspace whose payment failed more than 72 hours
	// ago and is still unpaid: nothing on the platform's own capacity runs.
	StatusPastDue Status = "past_due"
	// StatusLimited is a workspace that reached its spending limit this
	// month: nothing on the platform's own capacity runs.
	StatusLimited Status = "limited"
)

// pastDueAfter is how long a failed payment stays unpaid before the
// workspace is past due.
const pastDueAfter = 72 * time.Hour

// Account is a workspace's billing as of one moment.
type Account struct {
	OrgID              string
	Status             Status
	CardReady          bool
	CustomerID         string
	CloudSubscription  string
	AISubscription     string
	AIEnabled          bool       // an ai subscription is live this month
	AIEndsAt           *time.Time // set when turned off; on until then
	SpendingLimitCents *int64
	LimitedPeriod      *time.Time
}

// Account reads the workspace's account as of now. No row is pending_payment.
//
// The status follows the Global Constraints' precedence: no row or no card
// is pending_payment; else an invoice whose payment failed at least 72
// hours ago and is still unpaid is past_due; else a limit reached this
// calendar month is limited; else active.
func (s *Service) Account(ctx context.Context, orgID string) (Account, error) {
	if err := s.ready(); err != nil {
		return Account{}, err
	}
	if orgID == "" {
		return Account{}, errors.New("billing: an account needs a workspace")
	}
	now := s.now().UTC()
	a := Account{OrgID: orgID, Status: StatusPendingPayment}
	var pastDue bool
	// The past-due check spells out the partial index's predicate
	// (loomplane_billing_invoices_unpaid_failed), so the planner uses it.
	err := s.pg.QueryRow(ctx, `SELECT a.card_ready, a.stripe_customer_id, a.cloud_subscription_id, a.ai_subscription_id,
			a.ai_ends_at, a.spending_limit_cents, a.limited_period,
			EXISTS (SELECT 1 FROM loomplane_billing_invoices i
				WHERE i.org_id = a.org_id AND i.paid_at IS NULL AND i.payment_failed_at IS NOT NULL
					AND i.voided_at IS NULL AND i.payment_failed_at <= $2)
		FROM loomplane_billing_accounts a WHERE a.org_id = $1`,
		orgID, now.Add(-pastDueAfter),
	).Scan(&a.CardReady, &a.CustomerID, &a.CloudSubscription, &a.AISubscription,
		&a.AIEndsAt, &a.SpendingLimitCents, &a.LimitedPeriod, &pastDue)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return Account{}, fmt.Errorf("billing: read the account of %s: %w", orgID, err)
	}
	a.derive(pastDue, now)
	return a, nil
}

// derive works out, from an account row read at now, what Account reports
// beyond the row: the times in UTC, whether the add-on is live, and the
// status. pastDue says an invoice's payment failed at least 72 hours
// before now and it is still unpaid.
func (a *Account) derive(pastDue bool, now time.Time) {
	if a.AIEndsAt != nil {
		t := a.AIEndsAt.UTC()
		a.AIEndsAt = &t
	}
	if a.LimitedPeriod != nil {
		t := monthOf(*a.LimitedPeriod)
		a.LimitedPeriod = &t
	}
	a.AIEnabled = a.AISubscription != "" && (a.AIEndsAt == nil || a.AIEndsAt.After(now))
	switch {
	case !a.CardReady:
		a.Status = StatusPendingPayment
	case pastDue:
		a.Status = StatusPastDue
	case a.LimitedPeriod != nil && a.LimitedPeriod.Equal(monthStart(now)):
		a.Status = StatusLimited
	default:
		a.Status = StatusActive
	}
}

// MarkCardReady records the card and, the first time only, creates the
// workspace's cloud subscription with ledger.CreateSubscription on the
// current calendar month, active, no trial.
//
// The account row stays locked while the subscription is made, so two
// calls for one workspace make one subscription. Ledger writes outside
// this transaction, so a crash after ledger made the subscription and
// before its id was stored would leave it unrecorded; the next call finds
// that subscription in ledger and records it instead of making another.
func (s *Service) MarkCardReady(ctx context.Context, orgID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if orgID == "" {
		return errors.New("billing: a card needs a workspace")
	}
	// Whatever happens below, the next gate check reads the row again.
	defer s.forget(orgID)

	tx, err := s.pg.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billing: begin saving the card of %s: %w", orgID, err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a no-op after commit

	// The upsert locks the row until commit, so a second call waits here
	// and then reads the subscription the first one stored.
	var subID string
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_billing_accounts (org_id, card_ready, updated_at) VALUES ($1, true, $2)
		ON CONFLICT (org_id) DO UPDATE SET card_ready = true, updated_at = EXCLUDED.updated_at
		RETURNING cloud_subscription_id`, orgID, s.now().UTC()).Scan(&subID)
	if err != nil {
		return fmt.Errorf("billing: save the card of %s: %w", orgID, err)
	}
	if subID == "" {
		if subID, err = s.liveSubscription(ctx, orgID, PlanCloud); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_billing_accounts SET cloud_subscription_id = $2 WHERE org_id = $1`,
			orgID, subID); err != nil {
			return fmt.Errorf("billing: record the cloud subscription of %s: %w", orgID, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("billing: commit the card of %s: %w", orgID, err)
	}
	return nil
}

// liveSubscription returns the id of the workspace's live subscription to
// the plan called slug in ledger, making it when there is none: active,
// from 00:00 UTC on the 1st of this month, and with no trial. A live one
// ledger holds that the account never recorded (a crash came between
// making it and storing its id) is the one returned, so a retry never
// makes a second.
func (s *Service) liveSubscription(ctx context.Context, orgID, slug string) (string, error) {
	p, err := s.l.GetPlanBySlug(ctx, slug, s.appID)
	if err != nil {
		return "", fmt.Errorf("billing: read the %s plan: %w", slug, err)
	}
	subs, err := s.l.Store().ListSubscriptions(ctx, orgID, s.appID, subscription.ListOpts{})
	if err != nil {
		return "", fmt.Errorf("billing: list the subscriptions of %s: %w", orgID, err)
	}
	for _, sub := range subs {
		if !reusable(sub, p) {
			continue
		}
		if start := monthStart(s.now()); !sub.CurrentPeriodStart.Equal(start) {
			slog.WarnContext(ctx, "billing: recording a subscription whose period doesn't start this month",
				"org", orgID, "plan", slug, "subscription", sub.ID.String(),
				"period_start", sub.CurrentPeriodStart.UTC().Format(time.RFC3339), "month", start.Format("2006-01"))
		}
		return sub.ID.String(), nil
	}
	return s.subscribe(ctx, orgID, p)
}

// reusable says whether a subscription ledger already holds is the
// workspace's live subscription to plan p: on p, neither canceled nor
// expired, and never one with a trial, since billing gives no trial.
func reusable(sub *subscription.Subscription, p *plan.Plan) bool {
	switch {
	case sub.PlanID != p.ID:
		return false
	case sub.Status == subscription.StatusCanceled, sub.Status == subscription.StatusExpired,
		sub.Status == subscription.StatusTrialing:
		return false
	case sub.TrialEnd != nil:
		return false
	}
	return true
}

// subscribe puts the workspace on plan p for this calendar month. The
// status is set, so ledger never starts a trial whatever the plan says,
// and ledger ends the first period on the 1st of next month.
func (s *Service) subscribe(ctx context.Context, orgID string, p *plan.Plan) (string, error) {
	sub := &subscription.Subscription{TenantID: orgID, PlanID: p.ID, AppID: s.appID,
		Status: subscription.StatusActive, CurrentPeriodStart: monthStart(s.now())}
	if err := s.l.CreateSubscription(ctx, sub); err != nil { // ledger fills CurrentPeriodEnd: the 1st of next month
		return "", fmt.Errorf("billing: subscribe %s to %s: %w", orgID, p.Slug, err)
	}
	return sub.ID.String(), nil
}

// forget drops the workspace's cached gate answer, so the next check in
// this replica reads the account again. Every write to an account calls it.
func (s *Service) forget(orgID string) {
	if s != nil && s.gate != nil {
		s.gate.forget(orgID)
	}
}

// monthStart is 00:00 UTC on the 1st of t's month.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
