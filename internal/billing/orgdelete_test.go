//go:build cloud

package billing

import (
	"testing"
	"time"

	authsomeid "github.com/xraph/authsome/id"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/types"

	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// mustOrgID parses a workspace id the way authsome's id.ParseOrgID does.
func mustOrgID(t *testing.T, s string) authsomeid.ID {
	t.Helper()
	oid, err := authsomeid.ParseOrgID(s)
	if err != nil {
		t.Fatal(err)
	}
	return oid
}

// usageOf is compute usage for one workspace.
func usageOf(org, eventID string, q float64, when time.Time) metering.Event {
	e := compute(eventID, q, when)
	e.OrgID = org
	return e
}

// cancelLikeAuthsome is what authsome's subscription plugin does after the
// hook: cancel every subscription of the tenant now.
func cancelLikeAuthsome(t *testing.T, s *Service, org string) {
	t.Helper()
	a, _ := s.Account(t.Context(), org)
	for _, sub := range []string{a.CloudSubscription, a.AISubscription} {
		if sub == "" {
			continue
		}
		sid, _ := id.ParseSubscriptionID(sub)
		if err := s.l.CancelSubscription(t.Context(), sid, true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrgDelete_InvoicesBeforeCancel(t *testing.T) {
	clock := at("2026-11-15T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	org := authsomeid.NewOrgID().String() // "org_1" isn't an authsome workspace id
	_ = s.MarkCardReady(ctx, org)
	_, _ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, usageOf(org, "a", 900, at("2026-11-14T10:00:00Z")))
	hook := newOrgDeleteHook(s) // what NewOrgDeleteHook wraps
	if err := hook.OnAfterOrgDelete(ctx, mustOrgID(t, org)); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, org)
	if len(invs) != 1 || invs[0].Total != types.USD(900) || invs[0].Status != invoice.StatusPending {
		t.Fatalf("on delete: %+v", invs)
	}
}

// After the hook and authsome's cancel, the month close neither invoices
// the deleted workspace again nor fails over it, then or a month later.
func TestOrgDelete_TheCloseBillsNothingTwice(t *testing.T) {
	clock := at("2026-11-15T00:00:00Z")
	s, pg, fs := billingWorld(t, &clock)
	ctx := t.Context()
	org := authsomeid.NewOrgID().String()
	_ = s.MarkCardReady(ctx, org)
	if err := newOrgDeleteHook(s).OnAfterOrgDelete(ctx, mustOrgID(t, org)); err != nil {
		t.Fatal(err)
	}
	cancelLikeAuthsome(t, s, org)
	for _, when := range []string{"2026-12-01T01:15:00Z", "2027-01-01T01:15:00Z"} {
		clock = at(when)
		if err := s.CloseMonth(ctx); err != nil {
			t.Fatalf("close at %s: %v", when, err)
		}
	}
	if n := len(invoicesFor(t, s, org)); n != 1 || len(fs.sent()) != 1 {
		t.Fatalf("invoices %d sent %d, want the one made on delete", n, len(fs.sent()))
	}
	if !closed(t, pg, "2026-11-01") || !closed(t, pg, "2026-12-01") {
		t.Fatal("a deleted workspace kept a month open")
	}
}

// Deleted in the first minutes of a month, before the clock rolled: the
// hook bills the new month, and the close bills the month before in full.
func TestOrgDelete_RightAfterTheMonthEnds(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	org := authsomeid.NewOrgID().String()
	_ = s.MarkCardReady(ctx, org)
	_, _ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, usageOf(org, "october", 800, at("2026-10-10T10:00:00Z")))
	clock = at("2026-11-01T00:10:00Z")
	_ = s.Record(ctx, usageOf(org, "november", 700, at("2026-11-01T00:05:00Z")))
	if err := newOrgDeleteHook(s).OnAfterOrgDelete(ctx, mustOrgID(t, org)); err != nil {
		t.Fatal(err)
	}
	cancelLikeAuthsome(t, s, org)
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	totals := map[string]types.Money{}
	for _, inv := range invoicesFor(t, s, org) {
		totals[inv.PeriodStart.UTC().Format("2006-01")] = inv.Total
	}
	// October: $5 + $8 - $5. November to the delete: $5 + $7 - $5.
	if len(totals) != 2 || totals["2026-10"] != types.USD(800) || totals["2026-11"] != types.USD(700) {
		t.Fatalf("totals %v", totals)
	}
	clock = at("2026-12-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatalf("December's close: %v", err)
	}
	if n := len(invoicesFor(t, s, org)); n != 2 {
		t.Fatalf("invoices %d after December, want 2", n)
	}
}

// A workspace whose usage can't reach ledger isn't invoiced short on
// delete: the hook says so, and the month close bills the canceled
// subscription's month once the usage goes through.
func TestOrgDelete_StuckUsageLeavesItToTheClose(t *testing.T) {
	clock := at("2026-11-15T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	org := authsomeid.NewOrgID().String()
	_ = s.MarkCardReady(ctx, org)
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_usage (id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ('bad', $1, 'compute_vcpu_hours', 1, 10000, -1, '2026-11-14T10:00:00Z', 'pl_1')`, org); err != nil {
		t.Fatal(err)
	}
	if err := newOrgDeleteHook(s).OnAfterOrgDelete(ctx, mustOrgID(t, org)); err == nil {
		t.Fatal("the hook invoiced with usage missing")
	}
	if n := len(invoicesFor(t, s, org)); n != 0 {
		t.Fatalf("invoiced short on delete: %d", n)
	}
	cancelLikeAuthsome(t, s, org)
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_usage SET micro_usd = 6000000 WHERE id = 'bad'`) // $6
	clock = at("2026-12-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	if invs := invoicesFor(t, s, org); len(invs) != 1 || invs[0].Total != types.USD(600) {
		t.Fatalf("November after the close: %+v", invs)
	}
}

// A workspace that never had a subscription has nothing to invoice.
func TestOrgDelete_NoAccountIsNothing(t *testing.T) {
	clock := at("2026-11-15T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	org := authsomeid.NewOrgID().String()
	if err := newOrgDeleteHook(s).OnAfterOrgDelete(t.Context(), mustOrgID(t, org)); err != nil {
		t.Fatal(err)
	}
	if n := len(invoicesFor(t, s, org)); n != 0 {
		t.Fatalf("invoices %d", n)
	}
}

// An add-on turned off in October and deleted with the workspace before
// the close: the hook bills no November add-on, and the close bills
// October's once.
func TestOrgDelete_AnEndedAddOnIsNotBilledForTheNewMonth(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	org := authsomeid.NewOrgID().String()
	_ = s.MarkCardReady(ctx, org)
	p, err := s.l.GetPlanBySlug(ctx, PlanAI, testAppID)
	if err != nil {
		t.Fatal(err)
	}
	aiSub, err := s.subscribe(ctx, org, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id = $2, ai_ends_at = '2026-11-01T00:00:00Z' WHERE org_id = $1`,
		org, aiSub); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T00:30:00Z")
	if err = newOrgDeleteHook(s).OnAfterOrgDelete(ctx, mustOrgID(t, org)); err != nil {
		t.Fatal(err)
	}
	cancelLikeAuthsome(t, s, org)
	clock = at("2026-11-01T01:15:00Z")
	if err = s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	aiLines := map[string]int{}
	for _, inv := range invoicesFor(t, s, org) {
		for _, li := range inv.LineItems {
			if li.Type == invoice.LineItemBase && li.Amount == types.USD(1000) {
				aiLines[inv.PeriodStart.UTC().Format("2006-01")]++
			}
		}
	}
	if len(aiLines) != 1 || aiLines["2026-10"] != 1 {
		t.Fatalf("$10 lines by month %v, want October's only", aiLines)
	}
	if a, _ := s.Account(ctx, org); a.AISubscription != "" {
		t.Fatalf("the ended add-on wasn't rolled over: %+v", a)
	}
}
