//go:build cloud

package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ledger/coupon"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/types"

	"github.com/xraph/loomplane/internal/pglock"
)

func totalsByMonth(t *testing.T, s *Service) map[string]types.Money {
	t.Helper()
	totals := map[string]types.Money{}
	for _, inv := range invoicesFor(t, s, "org_1") {
		totals[inv.PeriodStart.UTC().Format("2006-01")] = inv.Total
	}
	return totals
}

func TestCredit_DiscountsOneInvoiceThenGoes(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if err := s.GrantCredit(ctx, "org_1", 300, "sorry about the outage", "rex"); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	clock = at("2026-12-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	totals := totalsByMonth(t, s)
	if totals["2026-10"] != types.USD(200) || totals["2026-11"] != types.USD(500) {
		t.Fatalf("totals %v: October $5 - $3, November $5", totals)
	}

	// The row says who granted it, why, and which invoice used it; the
	// coupon's code is the credit's own id.
	var (
		creditID, couponID, note, by string
		invID                        *string
		deleted                      bool
	)
	if err := pg.QueryRow(ctx, `SELECT id, coupon_id, note, granted_by, invoice_id, deleted_at IS NOT NULL FROM loomplane_billing_credits`).
		Scan(&creditID, &couponID, &note, &by, &invID, &deleted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(creditID, "lpc_") || note != "sorry about the outage" || by != "rex" || invID == nil || !deleted {
		t.Fatalf("credit row %s %q %q %v %v", creditID, note, by, invID, deleted)
	}
	cid, _ := id.ParseCouponID(couponID)
	if _, err := s.l.Store().GetCouponByID(ctx, cid); err == nil {
		t.Fatal("the used credit's coupon is still there")
	}
}

// A $20 credit on a $5 month: total 0, marked paid by the sync with no
// Stripe call.
func TestCredit_LargerThanTheBillIsAFreeInvoice(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if err := s.GrantCredit(ctx, "org_1", 2000, "", "rex"); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 || !invs[0].Total.IsZero() || invs[0].Status != "paid" || len(fs.sent()) != 0 {
		t.Fatalf("invoices %+v sent %v, want one free invoice, paid, never sent", invs, fs.sent())
	}
	var paid bool
	_ = pg.QueryRow(ctx, `SELECT paid_at IS NOT NULL FROM loomplane_billing_invoices`).Scan(&paid)
	if !paid {
		t.Fatal("Loomplane's row isn't paid")
	}
	// What the credit didn't use is gone with it: November pays $5.
	clock = at("2026-12-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	if got := totalsByMonth(t, s)["2026-11"]; got != types.USD(500) {
		t.Fatalf("November %v, want $5: an unused credit doesn't roll over", got)
	}
}

func TestCredit_NeedsACloudSubscription(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	if err := s.GrantCredit(ctx, "org_1", 300, "", "rex"); !errors.Is(err, ErrNeedsPayment) {
		t.Fatalf("no card: %v", err)
	}
	_ = s.MarkCardReady(ctx, "org_1")
	a, _ := s.Account(ctx, "org_1")
	sid, _ := id.ParseSubscriptionID(a.CloudSubscription)
	if err := s.l.CancelSubscription(ctx, sid, true); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantCredit(ctx, "org_1", 300, "", "rex"); !errors.Is(err, ErrNeedsPayment) {
		t.Fatalf("canceled subscription: %v", err)
	}
	var n int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_credits`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d credits recorded", n)
	}
}

func TestCredit_RefusesBadAmounts(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	for _, c := range []struct {
		cents    int64
		note, by string
	}{{0, "", "rex"}, {-1, "", "rex"}, {MaxCreditCents + 1, "", "rex"}, {100, "", " "}, {100, strings.Repeat("x", MaxCreditNote+1), "rex"}} {
		if err := s.GrantCredit(ctx, "org_1", c.cents, c.note, c.by); err == nil {
			t.Errorf("%d cents by %q granted", c.cents, c.by)
		}
	}
	if err := s.GrantCredit(ctx, "org_1", MaxCreditCents, "", "rex"); err != nil {
		t.Fatalf("$1,000.00: %v", err)
	}
}

// A grant waits while the month close holds the workspace's credits lock,
// so it lands wholly after the invoice the close is making.
func TestCredit_WaitsForTheCreditsLock(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")

	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo) // a failure below never leaves the lock held
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- pglock.Do(ctx, pg, creditsLock+"org_1", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	granted := make(chan error, 1)
	go func() { granted <- s.GrantCredit(ctx, "org_1", 300, "", "rex") }()
	select {
	case err := <-granted:
		t.Fatalf("the grant finished while the close held the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	var n int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_credits`).Scan(&n)
	a, _ := s.Account(ctx, "org_1")
	sid, _ := id.ParseSubscriptionID(a.CloudSubscription)
	applied, _ := s.l.ListAppliedCoupons(ctx, sid)
	if n != 0 || len(applied) != 0 {
		t.Fatalf("under the lock: %d rows, %d coupons applied", n, len(applied))
	}
	letGo()
	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}
	if err := <-granted; err != nil {
		t.Fatal(err)
	}
}

// A grant racing the month close discounts exactly one invoice by exactly
// its amount, October's or November's, whichever way the race goes.
func TestCredit_RacingACloseDiscountsOneInvoice(t *testing.T) {
	for run := range 3 {
		clock := at("2026-10-05T00:00:00Z")
		s, pg, _ := billingWorld(t, &clock)
		ctx := t.Context()
		_ = s.MarkCardReady(ctx, "org_1")
		// The clock moves only while nothing runs.
		clock = at("2026-11-01T01:15:00Z")

		var wg sync.WaitGroup
		var grantErr, closeErr error
		wg.Add(2)
		go func() { defer wg.Done(); grantErr = s.GrantCredit(ctx, "org_1", 300, "", "rex") }()
		go func() { defer wg.Done(); closeErr = s.CloseMonth(ctx) }()
		wg.Wait()
		if grantErr != nil || closeErr != nil {
			t.Fatalf("run %d: grant %v close %v", run, grantErr, closeErr)
		}
		clock = at("2026-12-01T01:15:00Z")
		if err := s.CloseMonth(ctx); err != nil {
			t.Fatal(err)
		}
		totals := totalsByMonth(t, s)
		oct, nov := totals["2026-10"].Amount, totals["2026-11"].Amount
		if oct+nov != 700 || (oct != 200 && nov != 200) {
			t.Fatalf("run %d: October %d November %d, want $3 off one of them", run, oct, nov)
		}
		var used int
		_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_credits WHERE invoice_id IS NOT NULL AND deleted_at IS NOT NULL`).Scan(&used)
		if used != 1 {
			t.Fatalf("run %d: %d credits used", run, used)
		}
	}
}

// A coupon a failed grant left applied with no credit row discounts
// nothing: the close deletes it before it makes the invoice.
func TestCredit_AnUnrecordedCouponDiscountsNothing(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	a, _ := s.Account(ctx, "org_1")
	sid, _ := id.ParseSubscriptionID(a.CloudSubscription)
	c := &coupon.Coupon{Code: "lpc_orphan", Name: "Credit", Type: coupon.CouponTypeAmount, Amount: types.USD(300),
		Currency: "usd", AppID: testAppID, MaxRedemptions: 1}
	if err := s.l.CreateCoupon(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.l.ApplyCoupon(ctx, sid, c.Code); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if got := totalsByMonth(t, s)["2026-10"]; got != types.USD(500) {
		t.Fatalf("October %v, want $5: an unrecorded coupon discounted it", got)
	}
	if _, err := s.l.Store().GetCouponByID(ctx, c.ID); err == nil {
		t.Fatal("the unrecorded coupon is still there")
	}
}
