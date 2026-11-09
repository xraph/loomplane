//go:build cloud

package billing

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/subscription"
	"github.com/xraph/ledger/types"

	"github.com/xraph/loomplane/internal/metering"
)

func TestSetAI_OffOnSameMonthNoSecondCharge(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if _, err := s.SetAI(ctx, "org_1", true); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-10-10T00:00:00Z")
	_, _ = s.SetAI(ctx, "org_1", false)
	clock = at("2026-10-12T00:00:00Z")
	a, err := s.SetAI(ctx, "org_1", true)
	if err != nil || !a.AIEnabled || a.AIEndsAt != nil {
		t.Fatalf("back on: %+v %v", a, err)
	}
	plans := map[string]int{}
	subs, _ := s.l.Store().ListSubscriptions(ctx, "org_1", testAppID, subscription.ListOpts{})
	for _, sub := range subs {
		p, _ := s.l.GetPlan(ctx, sub.PlanID)
		plans[p.Slug]++
	}
	if plans[PlanAI] != 1 {
		t.Fatalf("ai subscriptions %d, want 1", plans[PlanAI])
	}
	clock = at("2026-11-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	var aiBase, aiCredit int
	for _, inv := range invoicesFor(t, s, "org_1") {
		for _, li := range inv.LineItems {
			switch {
			case li.Type == invoice.LineItemBase && li.Amount == types.USD(1000):
				aiBase++
			case li.Type == invoice.LineItemCredit && li.Amount == types.USD(-1000):
				aiCredit++
			}
		}
	}
	if aiBase != 1 || aiCredit > 1 {
		t.Fatalf("$10 lines %d, AI credit lines %d", aiBase, aiCredit)
	}
}

func TestSetAI_OffKeepsUntilMonthEnd(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_, _ = s.SetAI(ctx, "org_1", true)
	a, _ := s.SetAI(ctx, "org_1", false)
	if !a.AIEnabled || a.AIEndsAt == nil || !a.AIEndsAt.Equal(at("2026-11-01T00:00:00Z")) {
		t.Fatalf("%+v", a)
	}
	if err := s.Gate().CanRun(ctx, "org_1", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("AI stopped before the month ended: %v", err)
	}
}

func TestSetAI_RefusedBeforeACard(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	if _, err := s.SetAI(t.Context(), "org_1", true); !errors.Is(err, ErrNeedsPayment) {
		t.Fatalf("%v", err)
	}
}

// aiSubscriptions are org_1's ai subscriptions in ledger.
func aiSubscriptions(t *testing.T, s *Service) []*subscription.Subscription {
	t.Helper()
	p, err := s.l.GetPlanBySlug(t.Context(), PlanAI, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	subs, err := s.l.Store().ListSubscriptions(t.Context(), "org_1", testAppID, subscription.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var out []*subscription.Subscription
	for _, sub := range subs {
		if sub.PlanID == p.ID {
			out = append(out, sub)
		}
	}
	return out
}

// aiInvoices are org_1's ai invoices, by the month they bill.
func aiInvoices(t *testing.T, s *Service) map[string][]*invoice.Invoice {
	t.Helper()
	ai := map[string]bool{}
	for _, sub := range aiSubscriptions(t, s) {
		ai[sub.ID.String()] = true
	}
	out := map[string][]*invoice.Invoice{}
	for _, inv := range invoicesFor(t, s, "org_1") {
		if ai[inv.SubscriptionID.String()] {
			k := inv.PeriodStart.UTC().Format("2006-01")
			out[k] = append(out[k], inv)
		}
	}
	return out
}

// lines sums an invoice's lines of one type.
func lines(inv *invoice.Invoice, typ invoice.LineItemType) (n int, sum types.Money) {
	sum = types.USD(0)
	for _, li := range inv.LineItems {
		if li.Type == typ {
			n++
			sum = sum.Add(li.Amount)
		}
	}
	return n, sum
}

// aiUsage records dollars of AI on the server's key, at cost.
func aiUsage(t *testing.T, s *Service, usageID string, dollars float64, when time.Time) {
	t.Helper()
	if err := s.Record(t.Context(), metering.Event{ID: usageID, OrgID: "org_1", Feature: FeatureAIUSD, ResourceRef: "req",
		Quantity: dollars, OccurredAt: when, PlatformOwned: true}); err != nil {
		t.Fatal(err)
	}
}

// Off then on in one month with AI used: one subscription, one $10 line,
// and the $10 credit taken off once.
func TestSetAI_OffOnSameMonthCreditOnce(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_, _ = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex") // at cost
	for _, on := range []bool{true, false, true, false, true} {
		if _, err := s.SetAI(ctx, "org_1", on); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(24 * time.Hour)
	}
	aiUsage(t, s, "ai:a", 12, at("2026-10-15T00:00:00Z"))
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(aiSubscriptions(t, s)); n != 1 {
		t.Fatalf("ai subscriptions %d, want 1", n)
	}
	oct := aiInvoices(t, s)["2026-10"]
	if len(oct) != 1 {
		t.Fatalf("October ai invoices %d, want 1", len(oct))
	}
	nBase, base := lines(oct[0], invoice.LineItemBase)
	nCredit, credit := lines(oct[0], invoice.LineItemCredit)
	// $10 base + $12 of AI - $10 credit.
	if nBase != 1 || base != types.USD(1000) || nCredit != 1 || credit != types.USD(-1000) || oct[0].Total != types.USD(1200) {
		t.Fatalf("base %d %v, credit %d %v, total %v", nBase, base, nCredit, credit, oct[0].Total)
	}
	// Left on, it isn't rolled over.
	if a, _ := s.Account(ctx, "org_1"); !a.AIEnabled || a.AIEndsAt != nil {
		t.Fatalf("after the close: %+v", a)
	}
}

// Off in October, rolled over at the close, on again in November: a new
// subscription, and each month pays $10 once with its own credit.
func TestSetAI_OffOnAcrossAMonthClose(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_, _ = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex")
	_, _ = s.SetAI(ctx, "org_1", true)
	aiUsage(t, s, "ai:oct", 12, at("2026-10-15T00:00:00Z"))
	clock = at("2026-10-20T00:00:00Z")
	_, _ = s.SetAI(ctx, "org_1", false)
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Account(ctx, "org_1")
	if first.AISubscription != "" || first.AIEnabled {
		t.Fatalf("not rolled over: %+v", first)
	}
	if err := s.Gate().CanRun(ctx, "org_1", metering.KindAIPlatform, false); err == nil {
		t.Fatal("AI ran in November with the add-on off")
	}
	clock = at("2026-11-05T00:00:00Z")
	a, err := s.SetAI(ctx, "org_1", true)
	if err != nil || !a.AIEnabled || a.AISubscription == "" || a.AIEndsAt != nil {
		t.Fatalf("on in November: %+v %v", a, err)
	}
	if err = s.Gate().CanRun(ctx, "org_1", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("AI refused once the add-on was back on: %v", err)
	}
	aiUsage(t, s, "ai:nov", 3, at("2026-11-10T00:00:00Z"))
	// Off and on again in November changes nothing more.
	_, _ = s.SetAI(ctx, "org_1", false)
	_, _ = s.SetAI(ctx, "org_1", true)
	clock = at("2026-12-01T01:15:00Z")
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(aiSubscriptions(t, s)); n != 2 {
		t.Fatalf("ai subscriptions %d, want October's and November's", n)
	}
	invs := aiInvoices(t, s)
	for month, want := range map[string]types.Money{"2026-10": types.USD(1200), "2026-11": types.USD(1000)} {
		if len(invs[month]) != 1 {
			t.Fatalf("%s ai invoices %d, want 1", month, len(invs[month]))
		}
		inv := invs[month][0]
		nBase, base := lines(inv, invoice.LineItemBase)
		nCredit, _ := lines(inv, invoice.LineItemCredit)
		if nBase != 1 || base != types.USD(1000) || nCredit != 1 || inv.Total != want {
			t.Fatalf("%s: base %d %v, credit lines %d, total %v, want %v", month, nBase, base, nCredit, inv.Total, want)
		}
	}
}

// On again after the month ended and before its close: the same
// subscription carries on, so October is invoiced once, November is billed
// on it, and nothing is rolled over.
func TestSetAI_OnAgainBeforeTheCloseKeepsTheSubscription(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	on, _ := s.SetAI(ctx, "org_1", true)
	clock = at("2026-10-20T00:00:00Z")
	_, _ = s.SetAI(ctx, "org_1", false)
	clock = at("2026-11-01T00:30:00Z")
	if a, _ := s.Account(ctx, "org_1"); a.AIEnabled {
		t.Fatalf("still on after its month: %+v", a)
	}
	// Off again here keeps the end it has, so it isn't carried into November.
	if a, _ := s.SetAI(ctx, "org_1", false); a.AIEndsAt == nil || !a.AIEndsAt.Equal(at("2026-11-01T00:00:00Z")) {
		t.Fatalf("off again moved the end: %+v", a)
	}
	a, err := s.SetAI(ctx, "org_1", true)
	if err != nil || !a.AIEnabled || a.AIEndsAt != nil || a.AISubscription != on.AISubscription {
		t.Fatalf("on again: %+v %v", a, err)
	}
	clock = at("2026-11-01T01:15:00Z")
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if a, _ = s.Account(ctx, "org_1"); a.AISubscription != on.AISubscription || !a.AIEnabled {
		t.Fatalf("rolled over though it was turned back on: %+v", a)
	}
	clock = at("2026-12-01T01:15:00Z")
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(aiSubscriptions(t, s)); n != 1 {
		t.Fatalf("ai subscriptions %d, want 1", n)
	}
	invs := aiInvoices(t, s)
	if len(invs["2026-10"]) != 1 || len(invs["2026-11"]) != 1 {
		t.Fatalf("ai invoices by month %v", invs)
	}
}

// The rollover reads the account again under its lock: an add-on turned
// back on after the rollover listed it keeps running.
func TestEndAI_LeavesAnAddOnTurnedBackOn(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	on, _ := s.SetAI(ctx, "org_1", true)
	_, _ = s.SetAI(ctx, "org_1", false)
	_, _ = s.SetAI(ctx, "org_1", true) // between the rollover's list and its cancel
	clock = at("2026-11-01T01:15:00Z")
	if err := s.endAI(ctx, "org_1", on.AISubscription, at("2026-11-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	sid, _ := id.ParseSubscriptionID(on.AISubscription)
	if sub, _ := s.l.GetSubscription(ctx, sid); sub == nil || sub.Status == subscription.StatusCanceled {
		t.Fatalf("canceled an add-on turned back on: %+v", sub)
	}
	if a, _ := s.Account(ctx, "org_1"); a.AISubscription != on.AISubscription || !a.AIEnabled {
		t.Fatalf("%+v", a)
	}
}

// A rollover that canceled in ledger and stopped before clearing the
// account: turning on again starts a new subscription instead of keeping a
// canceled one.
func TestSetAI_OnAfterACanceledSubscriptionStartsANewOne(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	on, _ := s.SetAI(ctx, "org_1", true)
	_, _ = s.SetAI(ctx, "org_1", false)
	clock = at("2026-11-01T01:15:00Z")
	sid, _ := id.ParseSubscriptionID(on.AISubscription)
	if err := s.l.CancelSubscription(ctx, sid, true); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-05T00:00:00Z")
	a, err := s.SetAI(ctx, "org_1", true)
	if err != nil || !a.AIEnabled || a.AISubscription == "" || a.AISubscription == on.AISubscription {
		t.Fatalf("%+v %v", a, err)
	}
}

// Four owners turning it on at once make one subscription.
func TestSetAI_ConcurrentOnMakesOneSubscription(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.SetAI(ctx, "org_1", true) }()
	}
	wg.Wait()
	if n := len(aiSubscriptions(t, s)); n != 1 {
		t.Fatalf("ai subscriptions %d, want 1", n)
	}
}

// Past due can't turn it on, but can turn it off, so next month's $10
// isn't owed too.
func TestSetAI_PastDueTurnsOffNotOn(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_, _ = s.SetAI(ctx, "org_1", true)
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices(invoice_id, org_id, subscription_id, plan, period_start, payment_failed_at)
		VALUES ('inv_1','org_1','sub_1','cloud','2026-09-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	a, err := s.SetAI(ctx, "org_1", false)
	if err != nil || a.Status != StatusPastDue || a.AIEndsAt == nil {
		t.Fatalf("off while past due: %+v %v", a, err)
	}
	if _, err = s.SetAI(ctx, "org_1", true); !errors.Is(err, ErrNeedsPayment) {
		t.Fatalf("on while past due: %v", err)
	}
	if a, _ = s.Account(ctx, "org_1"); a.AIEndsAt == nil {
		t.Fatalf("the refused on cleared the end: %+v", a)
	}
}

// Every change drops the gate's cached answer, so the next AI call sees it
// at once, not up to 15 seconds later.
func TestSetAI_ForgetsTheGatesAnswer(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	g := s.Gate()
	if err := g.CanRun(ctx, "org_1", metering.KindAIPlatform, false); err == nil {
		t.Fatal("AI ran with no add-on")
	}
	_, _ = s.SetAI(ctx, "org_1", true)
	if err := g.CanRun(ctx, "org_1", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("the gate kept its answer from before the add-on: %v", err)
	}
	// A write the gate didn't hear about would stay cached; SetAI's does not.
	s.gate.mu.Lock()
	_, cached := s.gate.cache["org_1"]
	s.gate.mu.Unlock()
	if !cached {
		t.Fatal("the gate cached nothing")
	}
	_, _ = s.SetAI(ctx, "org_1", false)
	s.gate.mu.Lock()
	_, cached = s.gate.cache["org_1"]
	s.gate.mu.Unlock()
	if cached {
		t.Fatal("turning it off left the gate's answer cached")
	}
}
