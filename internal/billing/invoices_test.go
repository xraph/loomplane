//go:build cloud

package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/coupon"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/provider"
	pgstore "github.com/xraph/ledger/store/postgres"
	"github.com/xraph/ledger/types"

	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// fakeStripe is a ledger payment provider that only syncs invoices. Any
// other provider call panics through the nil embedded interface.
type fakeStripe struct {
	provider.Provider
	mu     sync.Mutex
	synced []string
	fail   error // returned by SyncInvoice when set
}

func (f *fakeStripe) Name() string { return "stripe" }
func (f *fakeStripe) SyncInvoice(_ context.Context, inv *invoice.Invoice) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	f.synced = append(f.synced, inv.ID.String())
	return "in_" + inv.ID.String(), nil
}

func (f *fakeStripe) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.synced...)
}

func (f *fakeStripe) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

type fakePlugin struct{ p *fakeStripe }

func (f fakePlugin) Name() string                { return "stripe" }
func (f fakePlugin) Provider() provider.Provider { return f.p }

func billingWorld(t *testing.T, clock *time.Time) (*Service, *pgdriver.PgDB, *fakeStripe) {
	t.Helper()
	db, pg := scratchDB(t)
	fs := &fakeStripe{}
	l := ledger.New(pgstore.New(db), ledger.WithPlugin(fakePlugin{p: fs}), ledger.WithoutMigrate(),
		ledger.WithLifecycleInterval(0), ledger.WithClock(func() time.Time { return *clock }))
	if err := SeedCatalog(t.Context(), l, testAppID); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{DB: db, Ledger: l, AppID: testAppID, Now: func() time.Time { return *clock }})
	s.suspender = &suspenderSpy{}
	s.notify = func(context.Context, notifications.Notice) error { return nil }
	return s, pg, fs
}

func invoicesFor(t *testing.T, s *Service, org string) []*invoice.Invoice {
	t.Helper()
	out, err := s.l.Store().ListInvoices(t.Context(), org, testAppID, invoice.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCloseMonth_BillsBasePlusOverageWithCredit(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("a", 800, at("2026-10-10T10:00:00Z"))) // $8.00 of usage
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 {
		t.Fatalf("invoices %d", len(invs))
	}
	inv := invs[0]
	// $5 base + $8 usage - $5 credit = $8: "$8 of usage pays $5 + $3".
	if inv.Total != types.USD(800) || inv.Status != invoice.StatusPending || len(fs.synced) != 1 {
		t.Fatalf("total %v status %s synced %v", inv.Total, inv.Status, fs.synced)
	}
}

func TestCloseMonth_UsageUnderCreditPaysBase(t *testing.T) {
	// $3 of usage pays $5.
	clock := at("2026-10-05T00:00:00Z")
	s, _, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("a", 300, at("2026-10-10T10:00:00Z"))) // $3.00 of usage
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 {
		t.Fatalf("invoices %d", len(invs))
	}
	inv := invs[0]
	if inv.Total != types.USD(500) || inv.Status != invoice.StatusPending || len(fs.synced) != 1 {
		t.Fatalf("total %v status %s synced %v, want $5.00 pending and sent", inv.Total, inv.Status, fs.synced)
	}
	if !inv.PeriodStart.Equal(at("2026-10-01T00:00:00Z")) || !inv.PeriodEnd.Equal(at("2026-11-01T00:00:00Z")) {
		t.Fatalf("period %v to %v, want October", inv.PeriodStart, inv.PeriodEnd)
	}
}

func TestCloseMonth_LastHourOnTheInvoice(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("big", 500, at("2026-10-10T10:00:00Z")))
	clock = at("2026-11-01T00:02:30Z") // the last hour arrives
	_ = s.Record(ctx, compute("platform:pl_1:1:2026-10-31T23:00:00Z:compute_vcpu_hours", 100, at("2026-10-31T23:00:00Z")))
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if inv := invoicesFor(t, s, "org_1")[0]; inv.Total != types.USD(600) {
		t.Fatalf("total %v, want $6.00 (the last hour is October's)", inv.Total)
	}
}

func TestCloseMonth_RunTwiceBillsOnce(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	clock = at("2026-11-01T01:15:00Z")
	for range 2 {
		if err := s.CloseMonth(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(invoicesFor(t, s, "org_1")); n != 1 || len(fs.synced) != 1 {
		t.Fatalf("invoices %d synced %d", n, len(fs.synced))
	}
}

func TestCloseMonth_InvoicesBeforeRollover(t *testing.T) {
	t.Skip("Task 9") // Task 9 adds SetAI
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	// Task 9: if _, err := s.SetAI(ctx, "org_1", true); err != nil { t.Fatal(err) }
	clock = at("2026-10-20T00:00:00Z")
	// Task 9: _, _ = s.SetAI(ctx, "org_1", false) // on until 1 November
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	var aiLines int
	for _, inv := range invoicesFor(t, s, "org_1") {
		for _, li := range inv.LineItems {
			if li.Type == invoice.LineItemBase && li.Amount == types.USD(1000) {
				aiLines++
			}
		}
	}
	if aiLines != 1 {
		t.Fatalf("October's AI add-on lines %d, want 1", aiLines)
	}
	if a, _ := s.Account(ctx, "org_1"); a.AIEnabled || a.AISubscription != "" {
		t.Fatalf("AI still on after rollover: %+v", a)
	}
}

func TestSyncInvoices_SkipsOneWithAProviderID(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	clock = at("2026-11-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.SyncInvoices(ctx) }()
	}
	wg.Wait()
	if len(fs.synced) != 1 {
		t.Fatalf("one invoice was sent %d times", len(fs.synced))
	}
}

func TestCloseMonth_CardAddedTodayIsNotBilledForLastMonth(t *testing.T) {
	clock := at("2026-11-01T00:30:00Z")
	s, _, _ := billingWorld(t, &clock)
	_ = s.MarkCardReady(t.Context(), "org_1")
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(invoicesFor(t, s, "org_1")); n != 0 {
		t.Fatalf("a workspace that started today got %d invoices for October", n)
	}
}

// A sync that fails leaves the invoice for the hourly sync, and four
// replicas retrying it at once still send it once.
func TestSyncInvoices_RetriesAFailedSyncOnce(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	fs.failWith(errors.New("stripe is down"))
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err == nil {
		t.Fatal("the close hid a failed sync")
	}
	if n := len(fs.sent()); n != 0 {
		t.Fatalf("sent %d while Stripe was down", n)
	}
	// A failed sync doesn't hold the month open: its invoices are made.
	if open := closed(t, pg, "2026-10-01"); !open {
		t.Fatal("a failed sync kept October from closing")
	}
	fs.failWith(nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.SyncInvoices(ctx) }()
	}
	wg.Wait()
	if n := len(fs.sent()); n != 1 {
		t.Fatalf("one invoice was sent %d times", n)
	}
	var providerID string
	_ = pg.QueryRow(ctx, `SELECT provider_id FROM loomplane_billing_invoices`).Scan(&providerID)
	if providerID != "in_"+fs.sent()[0] {
		t.Fatalf("recorded provider id %q", providerID)
	}
	if err := s.SyncInvoices(ctx); err != nil || len(fs.sent()) != 1 {
		t.Fatalf("a later sync sent again: %v %d", err, len(fs.sent()))
	}
}

// closed says whether the month starting on day is recorded as closed.
func closed(t *testing.T, pg *pgdriver.PgDB, day string) bool {
	t.Helper()
	var ok bool
	if err := pg.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM loomplane_billing_closes WHERE period = $1::date)`, day).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// A crash between ledger generating the invoice and Loomplane recording it
// leaves an invoice only ledger knows of; the next close finds it, records
// and finalizes it, and makes no second one.
func TestCloseMonth_CrashAfterGenerateBillsOnce(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	clock = at("2026-11-01T01:15:00Z")
	a, _ := s.Account(ctx, "org_1")
	sid, _ := id.ParseSubscriptionID(a.CloudSubscription)
	if _, err := s.l.Advance(ctx, clock); err != nil {
		t.Fatal(err)
	}
	orphan, err := s.l.GenerateInvoice(ctx, sid, ledger.ForPeriod(at("2026-10-01T00:00:00Z"), at("2026-11-01T00:00:00Z")))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 || invs[0].ID != orphan.ID || invs[0].Status != invoice.StatusPending {
		t.Fatalf("invoices %+v, want the orphan finalized", invs)
	}
	var recorded int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_invoices WHERE invoice_id = $1 AND finalized_at IS NOT NULL`,
		orphan.ID.String()).Scan(&recorded)
	if recorded != 1 || len(fs.sent()) != 1 {
		t.Fatalf("recorded %d, sent %v", recorded, fs.sent())
	}
}

// A crash after recording and before finalizing leaves a draft; the next
// close finalizes it and sends it once.
func TestCloseMonth_CrashBeforeFinalizeFinishes(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	clock = at("2026-11-01T01:15:00Z")
	a, _ := s.Account(ctx, "org_1")
	sid, _ := id.ParseSubscriptionID(a.CloudSubscription)
	_, _ = s.l.Advance(ctx, clock)
	draft, err := s.l.GenerateInvoice(ctx, sid, ledger.ForPeriod(at("2026-10-01T00:00:00Z"), at("2026-11-01T00:00:00Z")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices (invoice_id, org_id, subscription_id, plan, period_start)
		VALUES ($1, 'org_1', $2, 'cloud', $3)`, draft.ID.String(), a.CloudSubscription, draft.PeriodStart); err != nil {
		t.Fatal(err)
	}
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 || invs[0].Status != invoice.StatusPending || len(fs.sent()) != 1 {
		t.Fatalf("invoices %+v sent %v", invs, fs.sent())
	}
}

// A close that stops part way (here: one workspace's usage can't reach
// ledger) invoices everyone else, keeps the month open, and the hourly
// catch-up finishes it once the usage goes through.
func TestCloseMonth_OneWorkspacesStuckUsageHoldsOnlyIt(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	for _, org := range []string{"org_1", "org_2"} {
		_ = s.MarkCardReady(ctx, org)
	}
	_ = s.Record(ctx, compute("good", 800, at("2026-10-10T10:00:00Z")))
	// org_2's row can't be batched: a negative price never adds up.
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_usage (id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ('bad', 'org_2', 'compute_vcpu_hours', 1, 10000, -1, '2026-10-10T10:00:00Z', 'pl_2')`); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err == nil {
		t.Fatal("the close hid org_2's stuck usage")
	}
	if invs := invoicesFor(t, s, "org_1"); len(invs) != 1 || invs[0].Total != types.USD(800) {
		t.Fatalf("org_1 waited on org_2: %+v", invs)
	}
	if n := len(invoicesFor(t, s, "org_2")); n != 0 {
		t.Fatalf("org_2 was invoiced short (%d invoices)", n)
	}
	if closed(t, pg, "2026-10-01") {
		t.Fatal("October is recorded closed with org_2 not invoiced")
	}

	// The row is put right; the next hour's catch-up finishes October.
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_usage SET micro_usd = 30000 WHERE id = 'bad'`)
	clock = at("2026-11-01T02:00:00Z")
	if err := s.CloseMonthIfOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if invs := invoicesFor(t, s, "org_2"); len(invs) != 1 || invs[0].Total != types.USD(500) {
		t.Fatalf("org_2 after the catch-up: %+v", invs)
	}
	if n := len(invoicesFor(t, s, "org_1")); n != 1 {
		t.Fatalf("org_1 invoiced %d times", n)
	}
	if !closed(t, pg, "2026-10-01") {
		t.Fatal("October still open")
	}
}

// The catch-up never runs before the close's own time, and never once the
// month is closed.
func TestCloseMonthIfOpen_OnlyAfterTheCloseAndUntilItFinishes(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	clock = at("2026-11-01T01:00:00Z")
	if err := s.CloseMonthIfOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(invoicesFor(t, s, "org_1")); n != 0 {
		t.Fatalf("closed before 01:15 (%d invoices)", n)
	}
	// The cron never fired; the hourly run closes October.
	clock = at("2026-11-01T03:00:00Z")
	if err := s.CloseMonthIfOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(invoicesFor(t, s, "org_1")); n != 1 || !closed(t, pg, "2026-10-01") {
		t.Fatalf("catch-up: %d invoices, closed %v", n, closed(t, pg, "2026-10-01"))
	}
	// Closed: the next runs touch nothing, even an invoice voided by hand.
	inv := invoicesFor(t, s, "org_1")[0]
	_, _ = pg.Exec(ctx, `UPDATE ledger_invoices SET status = 'voided' WHERE id = $1`, inv.ID.String())
	clock = at("2026-11-01T04:00:00Z")
	if err := s.CloseMonthIfOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(invoicesFor(t, s, "org_1")); n != 1 || len(fs.sent()) != 1 {
		t.Fatalf("a closed month ran again: %d invoices, %d sent", n, len(fs.sent()))
	}
}

// A credit granted before the invoice discounts it and goes; one granted
// after a crash, before the close ran again, waits for the next invoice.
func TestCloseMonth_DeletesOnlyTheCreditsItUsed(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	a, _ := s.Account(ctx, "org_1")
	used := grantCredit(t, s, pg, a.CloudSubscription, "lpc_used", 300)

	// A crash after generating, then a credit, then the close again.
	clock = at("2026-11-01T01:15:00Z")
	sid, _ := id.ParseSubscriptionID(a.CloudSubscription)
	_, _ = s.l.Advance(ctx, clock)
	if _, err := s.l.GenerateInvoice(ctx, sid, ledger.ForPeriod(at("2026-10-01T00:00:00Z"), at("2026-11-01T00:00:00Z"))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // the second credit is granted after the invoice
	later := grantCredit(t, s, pg, a.CloudSubscription, "lpc_later", 100)
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 || invs[0].Total != types.USD(200) {
		t.Fatalf("October: %+v, want $5 - $3", invs)
	}
	if _, err := s.l.Store().GetCoupon(ctx, used, testAppID); !errors.Is(err, ledger.ErrCouponNotFound) {
		t.Fatalf("the used credit's coupon: %v", err)
	}
	if _, err := s.l.Store().GetCoupon(ctx, later, testAppID); err != nil {
		t.Fatalf("the later credit's coupon went: %v", err)
	}
	var usedInv, laterInv *string
	_ = pg.QueryRow(ctx, `SELECT invoice_id FROM loomplane_billing_credits WHERE id='lpc_used' AND deleted_at IS NOT NULL`).Scan(&usedInv)
	_ = pg.QueryRow(ctx, `SELECT invoice_id FROM loomplane_billing_credits WHERE id='lpc_later' AND deleted_at IS NULL`).Scan(&laterInv)
	if usedInv == nil || *usedInv != invs[0].ID.String() || laterInv != nil {
		t.Fatalf("credit rows: used %v later %v", usedInv, laterInv)
	}

	// November's invoice takes the later credit.
	clock = at("2026-12-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	totals := map[string]types.Money{}
	for _, inv := range invoicesFor(t, s, "org_1") {
		totals[inv.PeriodStart.UTC().Format("2006-01")] = inv.Total
	}
	if totals["2026-11"] != types.USD(400) {
		t.Fatalf("totals %v, want November $5 - $1", totals)
	}
}

// grantCredit makes a credit the way GrantCredit does: a one-use amount
// coupon applied to the cloud subscription, and its row.
// It returns the coupon's code.
func grantCredit(t *testing.T, s *Service, pg *pgdriver.PgDB, subID, code string, cents int64) string {
	t.Helper()
	ctx := t.Context()
	c := &coupon.Coupon{Code: code, Name: "Credit", Type: coupon.CouponTypeAmount, Amount: types.USD(cents),
		Currency: "usd", AppID: testAppID, MaxRedemptions: 1}
	if err := s.l.CreateCoupon(ctx, c); err != nil {
		t.Fatal(err)
	}
	sid, _ := id.ParseSubscriptionID(subID)
	if _, err := s.l.ApplyCoupon(ctx, sid, code); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_credits (id, org_id, subscription_id, coupon_id, cents, granted_by, created_at)
		VALUES ($1, 'org_1', $2, $3, $4, 'rex', now())`, code, subID, c.ID.String(), cents); err != nil {
		t.Fatal(err)
	}
	return code
}

// A canceled subscription whose cancel came before the month began has no
// invoice for it and doesn't fail the close.
func TestCloseMonth_SkipsASubscriptionCanceledBeforeTheMonth(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	a, _ := s.Account(ctx, "org_1")
	sid, _ := id.ParseSubscriptionID(a.CloudSubscription)
	clock = at("2026-10-20T00:00:00Z")
	if err := s.l.CancelSubscription(ctx, sid, true); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-12-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatalf("December's close: %v", err)
	}
	if n := len(invoicesFor(t, s, "org_1")); n != 1 || !closed(t, pg, "2026-11-01") {
		t.Fatalf("invoices %d, November closed %v; want October's only", n, closed(t, pg, "2026-11-01"))
	}
}

// The AI add-on's rollover cancels an ended add-on only after its month is
// invoiced, and a workspace whose invoice failed keeps its add-on for the
// next run.
func TestRollOverAI_OnlyOnceInvoiced(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	p, err := s.l.GetPlanBySlug(ctx, PlanAI, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	aiSub, err := s.subscribe(ctx, "org_1", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id = $1, ai_ends_at = '2026-11-01T00:00:00Z' WHERE org_id = 'org_1'`,
		aiSub); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	if err = s.rollOverAI(ctx, clock, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); a.AISubscription != aiSub {
		t.Fatalf("rolled over before it was invoiced: %+v", a)
	}
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	var aiBase int
	for _, inv := range invoicesFor(t, s, "org_1") {
		for _, li := range inv.LineItems {
			if li.Type == invoice.LineItemBase && li.Amount == types.USD(1000) {
				aiBase++
			}
		}
	}
	if aiBase != 1 {
		t.Fatalf("October's $10 lines %d, want 1", aiBase)
	}
	if a, _ := s.Account(ctx, "org_1"); a.AIEnabled || a.AISubscription != "" || a.AIEndsAt != nil {
		t.Fatalf("AI still on after rollover: %+v", a)
	}
	sid, _ := id.ParseSubscriptionID(aiSub)
	if sub, _ := s.l.GetSubscription(ctx, sid); sub == nil || sub.CanceledAt == nil {
		t.Fatalf("the ai subscription isn't canceled in ledger: %+v", sub)
	}
	// December's close doesn't bill the ended add-on again.
	clock = at("2026-12-01T01:15:00Z")
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	aiBase = 0
	for _, inv := range invoicesFor(t, s, "org_1") {
		for _, li := range inv.LineItems {
			if li.Type == invoice.LineItemBase && li.Amount == types.USD(1000) {
				aiBase++
			}
		}
	}
	if aiBase != 1 {
		t.Fatalf("$10 lines after December %d, want 1", aiBase)
	}
}

// An invoice of zero is never sent: ledger marks it paid and so does
// Loomplane's row, so the hourly sync doesn't pick it up again.
func TestSyncInvoices_ZeroTotalIsPaidNotSent(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	a, _ := s.Account(ctx, "org_1")
	grantCredit(t, s, pg, a.CloudSubscription, "lpc_big", 2000)
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 || !invs[0].Total.IsZero() || invs[0].Status != invoice.StatusPaid || len(fs.sent()) != 0 {
		t.Fatalf("invoices %+v sent %v", invs, fs.sent())
	}
	var paid bool
	_ = pg.QueryRow(ctx, `SELECT paid_at IS NOT NULL FROM loomplane_billing_invoices`).Scan(&paid)
	if !paid {
		t.Fatal("Loomplane's row isn't paid")
	}
}

// Which credits an invoice used never depends on clocks: a credit whose
// row says it was granted after the invoice (another replica's clock ahead)
// still discounts it once and goes.
func TestCloseMonth_CreditWithAClockAheadAppliesOnce(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	a, _ := s.Account(ctx, "org_1")
	grantCredit(t, s, pg, a.CloudSubscription, "lpc_ahead", 300)
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_credits SET created_at = now() + interval '1 day' WHERE id = 'lpc_ahead'`)
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-12-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	totals := map[string]types.Money{}
	for _, inv := range invoicesFor(t, s, "org_1") {
		totals[inv.PeriodStart.UTC().Format("2006-01")] = inv.Total
	}
	if totals["2026-10"] != types.USD(200) || totals["2026-11"] != types.USD(500) {
		t.Fatalf("totals %v: October $5 - $3, November $5", totals)
	}
	var deleted bool
	_ = pg.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM loomplane_billing_credits WHERE id = 'lpc_ahead'`).Scan(&deleted)
	if !deleted {
		t.Fatal("the credit wasn't deleted after the invoice it discounted")
	}
}

// A recorded invoice voided in ledger is left alone by the next close: no
// second invoice, no error, and it is marked voided so the sync never
// lists it.
func TestCloseMonth_LeavesAVoidedInvoice(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	fs.failWith(errors.New("stripe is down"))
	clock = at("2026-11-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	inv := invoicesFor(t, s, "org_1")[0]
	if err := s.l.MarkInvoiceVoided(ctx, inv.ID, "by hand"); err != nil {
		t.Fatal(err)
	}
	_, _ = pg.Exec(ctx, `DELETE FROM loomplane_billing_closes`)
	fs.failWith(nil)
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if invs := invoicesFor(t, s, "org_1"); len(invs) != 1 || invs[0].ID != inv.ID || len(fs.sent()) != 0 {
		t.Fatalf("invoices %+v sent %v", invs, fs.sent())
	}
	var voided bool
	_ = pg.QueryRow(ctx, `SELECT voided_at IS NOT NULL FROM loomplane_billing_invoices WHERE invoice_id = $1`, inv.ID.String()).Scan(&voided)
	if !voided || !closed(t, pg, "2026-10-01") {
		t.Fatalf("voided recorded %v, October closed %v", voided, closed(t, pg, "2026-10-01"))
	}
}

// A finalized invoice voided before it was sent is never sent, and once
// the sync has seen it, it is off the sync's list.
func TestSyncInvoices_DropsAVoidedInvoice(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	fs.failWith(errors.New("stripe is down"))
	clock = at("2026-11-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	inv := invoicesFor(t, s, "org_1")[0]
	if err := s.l.MarkInvoiceVoided(ctx, inv.ID, "by hand"); err != nil {
		t.Fatal(err)
	}
	fs.failWith(nil)
	if err := s.SyncInvoices(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(fs.sent()); n != 0 {
		t.Fatalf("a voided invoice was sent %d times", n)
	}
	var due int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_invoices
		WHERE finalized_at IS NOT NULL AND provider_id = '' AND paid_at IS NULL AND voided_at IS NULL`).Scan(&due)
	if due != 0 {
		t.Fatalf("%d invoices still due after the sync saw the voided one", due)
	}
}

// A workspace held open past the next 1st still gets its month closed by
// the catch-up, and the month after it is closed on its own.
func TestCloseMonthIfOpen_ClosesAMonthHeldPastTheNextFirst(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_usage (id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ('bad', 'org_1', 'compute_vcpu_hours', 1, 10000, -1, '2026-10-10T10:00:00Z', 'pl_1')`); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	clock = at("2026-12-01T01:15:00Z")
	_ = s.CloseMonth(ctx) // November waits too: October's row is still stuck
	if closed(t, pg, "2026-10-01") || len(invoicesFor(t, s, "org_1")) != 0 {
		t.Fatal("October closed with its usage stuck")
	}
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_usage SET micro_usd = 20000 WHERE id = 'bad'`)
	clock = at("2026-12-01T02:00:00Z")
	if err := s.CloseMonthIfOpen(ctx); err != nil {
		t.Fatal(err)
	}
	periods := map[string]bool{}
	for _, inv := range invoicesFor(t, s, "org_1") {
		periods[inv.PeriodStart.UTC().Format("2006-01")] = true
	}
	if len(periods) != 2 || !periods["2026-10"] || !periods["2026-11"] {
		t.Fatalf("invoiced %v, want October and November", periods)
	}
	if !closed(t, pg, "2026-10-01") || !closed(t, pg, "2026-11-01") {
		t.Fatal("the catch-up left a month open")
	}
}
