//go:build cloud

package billing

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ledger/subscription"
)

func TestAccount_NoRowIsPending(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-10T00:00:00Z"))
	a, err := s.Account(t.Context(), "org_new")
	if err != nil || a.Status != StatusPendingPayment {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestMarkCardReady_SubscribesOnceOnTheCalendarMonth(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-20T15:04:05Z"))
	ctx := t.Context()
	for range 2 {
		if err := s.MarkCardReady(ctx, "org_1"); err != nil {
			t.Fatal(err)
		}
	}
	subs, err := s.l.Store().ListSubscriptions(ctx, "org_1", testAppID, subscription.ListOpts{})
	if err != nil || len(subs) != 1 {
		t.Fatalf("subscriptions %d %v, want 1", len(subs), err)
	}
	sub := subs[0]
	if !sub.CurrentPeriodStart.Equal(at("2026-11-01T00:00:00Z")) || !sub.CurrentPeriodEnd.Equal(at("2026-12-01T00:00:00Z")) {
		t.Fatalf("period %s to %s", sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	}
	if sub.Status != subscription.StatusActive || sub.TrialEnd != nil {
		t.Fatalf("status %s trial %v: there is no trial", sub.Status, sub.TrialEnd)
	}
	a, _ := s.Account(ctx, "org_1")
	if a.Status != StatusActive || a.CloudSubscription != sub.ID.String() {
		t.Fatalf("%+v", a)
	}
}

func TestAccount_PastDueAfter72h(t *testing.T) {
	clock := at("2026-11-02T10:00:00Z")
	s, pg := newTestService(t, clock)
	s.now = func() time.Time { return clock }
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices(invoice_id, org_id, subscription_id, plan, period_start, payment_failed_at)
		VALUES ('inv_1','org_1','sub_1','cloud','2026-10-01T00:00:00Z', $1)`, clock); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(71 * time.Hour)
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusActive {
		t.Fatalf("at 71h: %s", a.Status)
	}
	clock = clock.Add(2 * time.Hour)
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusPastDue {
		t.Fatalf("at 73h: %s", a.Status)
	}
}

func TestAccount_StatusPrecedence(t *testing.T) {
	now := at("2026-11-20T00:00:00Z")
	failed := now.Add(-100 * time.Hour)
	cases := []struct {
		name    string
		card    bool
		failed  bool // an unpaid invoice failed 100h ago
		paid    bool // ... and has since been paid
		limited string
		want    Status
	}{
		{"no card, everything else wrong", false, true, false, "2026-11-01", StatusPendingPayment},
		{"past due and limited", true, true, false, "2026-11-01", StatusPastDue},
		{"failed then paid", true, true, true, "", StatusActive},
		{"limited this month", true, false, false, "2026-11-01", StatusLimited},
		{"limited last month", true, false, false, "2026-10-01", StatusActive},
		{"card and nothing owing", true, false, false, "", StatusActive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, pg := newTestService(t, now)
			ctx := t.Context()
			var limited *time.Time
			if c.limited != "" {
				l := at(c.limited + "T00:00:00Z")
				limited = &l
			}
			if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_accounts (org_id, card_ready, limited_period) VALUES ('org_1', $1, $2)`,
				c.card, limited); err != nil {
				t.Fatal(err)
			}
			if c.failed {
				var paid *time.Time
				if c.paid {
					paid = &now
				}
				if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices(invoice_id, org_id, subscription_id, plan, period_start, payment_failed_at, paid_at)
					VALUES ('inv_1','org_1','sub_1','cloud','2026-10-01T00:00:00Z', $1, $2)`, failed, paid); err != nil {
					t.Fatal(err)
				}
			}
			a, err := s.Account(ctx, "org_1")
			if err != nil || a.Status != c.want {
				t.Fatalf("%s %v, want %s", a.Status, err, c.want)
			}
		})
	}
}

func TestAccount_AnotherWorkspacesFailedInvoiceIsNotYours(t *testing.T) {
	now := at("2026-11-20T00:00:00Z")
	s, pg := newTestService(t, now)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices(invoice_id, org_id, subscription_id, plan, period_start, payment_failed_at)
		VALUES ('inv_9','org_9','sub_9','cloud','2026-10-01T00:00:00Z', $1)`, now.Add(-100*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusActive {
		t.Fatalf("org_1 is %s for org_9's invoice", a.Status)
	}
}

func TestAccount_AIEnabledUntilItEnds(t *testing.T) {
	now := at("2026-11-20T00:00:00Z")
	s, pg := newTestService(t, now)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if a, _ := s.Account(ctx, "org_1"); a.AIEnabled || a.AIEndsAt != nil {
		t.Fatalf("no add-on: %+v", a)
	}
	if _, err := pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id='sub_ai' WHERE org_id='org_1'`); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); !a.AIEnabled || a.AISubscription != "sub_ai" {
		t.Fatalf("add-on on: %+v", a)
	}
	ends := at("2026-12-01T00:00:00Z")
	if _, err := pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_ends_at=$1 WHERE org_id='org_1'`, ends); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); !a.AIEnabled || a.AIEndsAt == nil || !a.AIEndsAt.Equal(ends) {
		t.Fatalf("turned off, on until the 1st: %+v", a)
	}
	s.now = func() time.Time { return ends }
	if a, _ := s.Account(ctx, "org_1"); a.AIEnabled {
		t.Fatalf("after it ended: %+v", a)
	}
}

func TestAccount_ReadsTheRestOfTheRow(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-20T00:00:00Z"))
	ctx := t.Context()
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_accounts (org_id, stripe_customer_id, card_ready, spending_limit_cents, limited_period)
		VALUES ('org_1', 'cus_1', true, 2500, '2026-10-01')`); err != nil {
		t.Fatal(err)
	}
	a, err := s.Account(ctx, "org_1")
	if err != nil || a.CustomerID != "cus_1" || !a.CardReady || a.SpendingLimitCents == nil || *a.SpendingLimitCents != 2500 ||
		a.LimitedPeriod == nil || !a.LimitedPeriod.Equal(at("2026-10-01T00:00:00Z")) {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestAccount_RefusesAnEmptyWorkspace(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-20T00:00:00Z"))
	if _, err := s.Account(t.Context(), ""); err == nil {
		t.Fatal("read an account with no workspace")
	}
	if err := s.MarkCardReady(t.Context(), ""); err == nil {
		t.Fatal("saved a card with no workspace")
	}
}

func TestMarkCardReady_KeepsTheCustomer(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-20T00:00:00Z"))
	ctx := t.Context()
	if err := (pgCustomers{pg: pg}).SetCustomerID(ctx, "org_1", "cus_1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCardReady(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); a.CustomerID != "cus_1" || !a.CardReady {
		t.Fatalf("%+v", a)
	}
}

func TestMarkCardReady_ConcurrentCallsSubscribeOnce(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-20T00:00:00Z"))
	ctx := t.Context()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- s.MarkCardReady(ctx, "org_1") })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	subs, err := s.l.Store().ListSubscriptions(ctx, "org_1", testAppID, subscription.ListOpts{})
	if err != nil || len(subs) != 1 {
		t.Fatalf("subscriptions %d %v, want 1", len(subs), err)
	}
}

// A crash after ledger made the subscription and before the account row
// recorded it leaves a subscription with no row pointing at it. The next
// call records that one instead of making a second.
func TestMarkCardReady_RecordsASubscriptionACrashLeftBehind(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-20T00:00:00Z"))
	ctx := t.Context()
	p, err := s.l.GetPlanBySlug(ctx, PlanCloud, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.subscribe(ctx, "org_1", p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkCardReady(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	subs, _ := s.l.Store().ListSubscriptions(ctx, "org_1", testAppID, subscription.ListOpts{})
	if len(subs) != 1 {
		t.Fatalf("subscriptions %d, want 1", len(subs))
	}
	if a, _ := s.Account(ctx, "org_1"); a.CloudSubscription != orphan {
		t.Fatalf("recorded %q, want %q", a.CloudSubscription, orphan)
	}
}

// A subscription with a trial is never the workspace's cloud subscription:
// billing gives no trial, so one with a trial status or a trial end is left
// alone and a new one is made.
func TestMarkCardReady_NeverReusesATrialSubscription(t *testing.T) {
	for _, c := range []struct {
		name   string
		status subscription.Status
	}{
		{"trialing", subscription.StatusTrialing},
		{"active with a trial end", subscription.StatusActive},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := at("2026-11-20T00:00:00Z")
			s, _ := newTestService(t, now)
			ctx := t.Context()
			p, err := s.l.GetPlanBySlug(ctx, PlanCloud, testAppID)
			if err != nil {
				t.Fatal(err)
			}
			start, end := monthStart(now), monthStart(now).AddDate(0, 0, 14)
			trial := &subscription.Subscription{TenantID: "org_1", PlanID: p.ID, AppID: testAppID, Status: c.status,
				CurrentPeriodStart: start, TrialStart: &start, TrialEnd: &end}
			if err = s.l.CreateSubscription(ctx, trial); err != nil {
				t.Fatal(err)
			}
			if err = s.MarkCardReady(ctx, "org_1"); err != nil {
				t.Fatal(err)
			}
			a, _ := s.Account(ctx, "org_1")
			if a.CloudSubscription == "" || a.CloudSubscription == trial.ID.String() {
				t.Fatalf("recorded %q, the trial subscription is %q", a.CloudSubscription, trial.ID)
			}
			subs, _ := s.l.Store().ListSubscriptions(ctx, "org_1", testAppID, subscription.ListOpts{})
			if len(subs) != 2 {
				t.Fatalf("subscriptions %d, want the trial one and a new one", len(subs))
			}
			for _, sub := range subs {
				if sub.ID.String() == a.CloudSubscription && (sub.Status != subscription.StatusActive || sub.TrialEnd != nil) {
					t.Fatalf("the new subscription is %s with trial end %v", sub.Status, sub.TrialEnd)
				}
			}
		})
	}
}

// A live subscription left from an earlier month is still recorded (it is
// the workspace's), with a warning that its period isn't this month's.
func TestMarkCardReady_RecordsAnOlderSubscriptionWithAWarning(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-20T00:00:00Z"))
	ctx := t.Context()
	p, err := s.l.GetPlanBySlug(ctx, PlanCloud, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	old := &subscription.Subscription{TenantID: "org_1", PlanID: p.ID, AppID: testAppID,
		Status: subscription.StatusActive, CurrentPeriodStart: at("2026-10-01T00:00:00Z")}
	if err = s.l.CreateSubscription(ctx, old); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	if err = s.MarkCardReady(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); a.CloudSubscription != old.ID.String() {
		t.Fatalf("recorded %q, want %q", a.CloudSubscription, old.ID)
	}
	if !strings.Contains(logged.String(), "doesn't start this month") || !strings.Contains(logged.String(), old.ID.String()) {
		t.Fatalf("no warning: %q", logged.String())
	}
}

func TestMarkCardReady_OnTheFirstAtMidnightAndTheLastSecond(t *testing.T) {
	for _, c := range []struct{ now, start, end string }{
		{"2026-12-01T00:00:00Z", "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"2026-12-31T23:59:59Z", "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"2027-01-31T12:00:00Z", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z"},
	} {
		s, _ := newTestService(t, at(c.now))
		ctx := t.Context()
		if err := s.MarkCardReady(ctx, "org_1"); err != nil {
			t.Fatal(err)
		}
		subs, _ := s.l.Store().ListSubscriptions(ctx, "org_1", testAppID, subscription.ListOpts{})
		if len(subs) != 1 || !subs[0].CurrentPeriodStart.Equal(at(c.start)) || !subs[0].CurrentPeriodEnd.Equal(at(c.end)) {
			t.Fatalf("card at %s: %+v", c.now, subs)
		}
	}
}

func TestMonthStart(t *testing.T) {
	lagos := time.FixedZone("WAT", 3600)
	for _, c := range []struct {
		in   time.Time
		want string
	}{
		{at("2026-11-20T15:04:05Z"), "2026-11-01T00:00:00Z"},
		{at("2026-11-01T00:00:00Z"), "2026-11-01T00:00:00Z"},
		// 00:30 on 1 December in Lagos is still November in UTC.
		{time.Date(2026, 12, 1, 0, 30, 0, 0, lagos), "2026-11-01T00:00:00Z"},
	} {
		got := monthStart(c.in)
		if !got.Equal(at(c.want)) || got.Location() != time.UTC {
			t.Errorf("monthStart(%s) = %s", c.in, got)
		}
	}
}
