//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/platformcloud"
)

type suspenderSpy struct {
	mu       sync.Mutex
	calls    []string // "suspend spending_limit", "restore past_due", ...
	noTenant bool
	fail     error // returned by every call when set
}

func (f *suspenderSpy) Suspend(_ context.Context, _ string, r platformcloud.SuspendReason) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "suspend "+string(r))
	if f.fail != nil {
		return f.fail
	}
	if f.noTenant {
		return platformcloud.ErrNotEnabled
	}
	return nil
}

func (f *suspenderSpy) Restore(_ context.Context, _ string, r platformcloud.SuspendReason) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "restore "+string(r))
	if f.fail != nil {
		return f.fail
	}
	if f.noTenant {
		return platformcloud.ErrNotEnabled
	}
	return nil
}

// since is the calls made after the first n.
func (f *suspenderSpy) since(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls[n:])
}

func (f *suspenderSpy) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// spent puts priced base usage straight into the usage table.
func spent(t *testing.T, pg *pgdriver.PgDB, org string, cents int64, when time.Time) {
	t.Helper()
	_, err := pg.Exec(t.Context(), `INSERT INTO loomplane_billing_usage(id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ($1, $2, 'compute_vcpu_hours', 1, $3, $3, $4, 'pl_1')`, fmt.Sprintf("u%d-%d", cents, when.UnixNano()), org, cents*10_000, when)
	if err != nil {
		t.Fatal(err)
	}
}

// spentMicro puts one priced row of any feature into the usage table.
func spentMicro(t *testing.T, pg *pgdriver.PgDB, org, feature string, micro int64, when time.Time) {
	t.Helper()
	_, err := pg.Exec(t.Context(), `INSERT INTO loomplane_billing_usage(id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ($1, $2, $3, 1, $4, $4, $5, 'pl_1')`, fmt.Sprintf("%s-%s-%d-%d", org, feature, micro, when.UnixNano()), org, feature, micro, when)
	if err != nil {
		t.Fatal(err)
	}
}

func limitedWorld(t *testing.T, now time.Time) (*Service, *pgdriver.PgDB, *suspenderSpy, *[]notifications.Notice) {
	s, pg := newTestService(t, now)
	spy := &suspenderSpy{}
	var (
		mu   sync.Mutex
		sent []notifications.Notice
	)
	s.suspender = spy
	s.notify = func(_ context.Context, n notifications.Notice) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, n)
		return nil
	}
	if err := s.MarkCardReady(t.Context(), "org_1"); err != nil {
		t.Fatal(err)
	}
	return s, pg, spy, &sent
}

func ptr(n int64) *int64 { return &n }

func TestEvaluate_LimitReachedSuspends(t *testing.T) {
	s, pg, spy, sent := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(300))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z")) // 400 overage
	if err := s.Evaluate(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusLimited {
		t.Fatalf("status %s", a.Status)
	}
	if !slices.Contains(spy.calls, "suspend spending_limit") || !hasKind(*sent, "billing.limit_100") {
		t.Fatalf("calls %v notices %v", spy.calls, *sent)
	}
}

func TestEvaluate_LimitLoweredBelowSpend(t *testing.T) {
	s, pg, _, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	spent(t, pg, "org_1", 1400, at("2026-11-14T10:00:00Z")) // 900 overage, no limit yet
	_ = s.Evaluate(ctx, "org_1")
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusActive {
		t.Fatalf("no limit: %s", a.Status)
	}
	// No new usage: lowering the limit alone must limit.
	a, err := s.SetSpendingLimit(ctx, "org_1", ptr(500))
	if err != nil || a.Status != StatusLimited {
		t.Fatalf("after lowering: %+v %v", a, err)
	}
}

func TestEvaluate_RaisedLimitRestores(t *testing.T) {
	s, pg, spy, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	a, _ := s.SetSpendingLimit(ctx, "org_1", ptr(1000))
	if a.Status != StatusActive || !slices.Contains(spy.calls, "restore spending_limit") {
		t.Fatalf("%s %v", a.Status, spy.calls)
	}
}

func TestEvaluate_NewMonthClears(t *testing.T) {
	clock := at("2026-11-30T23:00:00Z")
	s, pg, spy, _ := limitedWorld(t, clock)
	s.now = func() time.Time { return clock }
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(0))
	spent(t, pg, "org_1", 600, at("2026-11-30T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	clock = at("2026-12-01T00:15:00Z")
	_ = s.Evaluate(ctx, "org_1")
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusActive || !slices.Contains(spy.calls, "restore spending_limit") {
		t.Fatalf("%s %v", a.Status, spy.calls)
	}
}

func TestEvaluate_ZeroLimitStopsAtCredit(t *testing.T) {
	s, pg, _, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(0))
	spent(t, pg, "org_1", 100, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusActive {
		t.Fatalf("$1 of $5 included with a $0 limit: %s", a.Status)
	}
	spent(t, pg, "org_1", 400, at("2026-11-14T11:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusLimited {
		t.Fatalf("credit used up with a $0 limit: %s", a.Status)
	}
}

func TestEvaluate_NoticesOncePerPeriodButAgainAfterARestore(t *testing.T) {
	s, pg, _, sent := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	_ = s.Evaluate(ctx, "org_1")
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(1000)) // restored
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))  // limited again, same month
	subjects := map[string]bool{}
	for _, n := range *sent {
		if n.Kind == "billing.limit_100" {
			subjects[n.SubjectID] = true
		}
	}
	if len(subjects) != 2 {
		t.Fatalf("limit_100 subjects %v, want two (one per limited transition)", subjects)
	}
	if !subjects["limit_100:2026-11:1"] || !subjects["limit_100:2026-11:2"] {
		t.Fatalf("limit_100 subjects %v", subjects)
	}
}

func TestEvaluate_NoLimitNeverLimits(t *testing.T) {
	s, pg, spy, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	spent(t, pg, "org_1", 100_000, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(t.Context(), "org_1")
	if a, _ := s.Account(t.Context(), "org_1"); a.Status != StatusActive || slices.Contains(spy.calls, "suspend spending_limit") {
		t.Fatalf("%s %v", a.Status, spy.calls)
	}
}

func TestEvaluate_PastDueSuspendsAndPaymentRestores(t *testing.T) {
	now := at("2026-11-15T00:00:00Z")
	s, pg, spy, _ := limitedWorld(t, now)
	ctx := t.Context()
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices(invoice_id, org_id, subscription_id, plan, period_start, payment_failed_at)
		VALUES ('inv_1','org_1','sub_1','cloud','2026-10-01T00:00:00Z', $1)`, now.Add(-73*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusPastDue || !slices.Contains(spy.calls, "suspend past_due") {
		t.Fatalf("%s %v", a.Status, spy.calls)
	}
	if slices.Contains(spy.calls, "restore past_due") {
		t.Fatalf("restored while still owing: %v", spy.calls)
	}
	n := spy.count()
	if _, err := pg.Exec(ctx, `UPDATE loomplane_billing_invoices SET paid_at=$1 WHERE invoice_id='inv_1'`, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	after := spy.since(n)
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusActive || !slices.Contains(after, "restore past_due") || slices.Contains(after, "suspend past_due") {
		t.Fatalf("after paying: %s %v", a.Status, after)
	}
}

func TestEvaluate_PayingLeavesAReachedLimitSuspended(t *testing.T) {
	now := at("2026-11-15T00:00:00Z")
	s, pg, spy, _ := limitedWorld(t, now)
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices(invoice_id, org_id, subscription_id, plan, period_start, payment_failed_at)
		VALUES ('inv_1','org_1','sub_1','cloud','2026-10-01T00:00:00Z', $1)`, now.Add(-100*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = s.Evaluate(ctx, "org_1")
	if !slices.Contains(spy.calls, "suspend past_due") || !slices.Contains(spy.calls, "suspend spending_limit") {
		t.Fatalf("both reasons: %v", spy.calls)
	}
	n := spy.count()
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_invoices SET paid_at=$1 WHERE invoice_id='inv_1'`, now)
	_ = s.Evaluate(ctx, "org_1")
	after := spy.since(n)
	if slices.Contains(after, "restore spending_limit") || !slices.Contains(after, "restore past_due") {
		t.Fatalf("paying lifted the limit too: %v", after)
	}
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusLimited {
		t.Fatalf("after paying with the limit reached: %s", a.Status)
	}
}

func TestEvaluate_AIOnlyWorkspaceGetsRestoredNotice(t *testing.T) {
	s, pg, spy, sent := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	spy.noTenant = true
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	if err := s.Evaluate(ctx, "org_1"); err != nil {
		t.Fatalf("no tenant is nothing to stop, not an error: %v", err)
	}
	_, _ = s.SetSpendingLimit(ctx, "org_1", nil)
	if !hasKind(*sent, "billing.restored") {
		t.Fatal("a workspace with no platform tenant heard nothing on restore")
	}
	i := slices.IndexFunc(*sent, func(n notifications.Notice) bool { return n.Kind == NoticeRestored })
	if n := (*sent)[i]; n.SubjectID != "restored:1" || n.Title != "Spending limit cleared" || n.Body != "AI on Loomplane's model keys is back on." || n.Path != "/settings/billing" {
		t.Fatalf("%+v", n)
	}
}

func TestEvaluate_TenantRestoreSendsNoBillingRestored(t *testing.T) {
	s, pg, _, sent := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	_, _ = s.SetSpendingLimit(ctx, "org_1", nil)
	if hasKind(*sent, NoticeRestored) {
		t.Fatal("platform Cloud tells a workspace with a tenant itself")
	}
}

func TestEvaluate_ReadErrorChangesNothing(t *testing.T) {
	s, pg, spy, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	// Raise the limit, then lose the usage table: nothing may be lifted
	// or stopped on a read that failed.
	if _, err := pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET spending_limit_cents=NULL WHERE org_id='org_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(ctx, `ALTER TABLE loomplane_billing_usage RENAME TO loomplane_billing_usage_gone`); err != nil {
		t.Fatal(err)
	}
	n := spy.count()
	if err := s.Evaluate(ctx, "org_1"); err == nil {
		t.Fatal("a failed usage read evaluated")
	}
	if calls := spy.since(n); len(calls) != 0 {
		t.Fatalf("platform calls on a failed read: %v", calls)
	}
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusLimited {
		t.Fatalf("status %s after a failed read, want limited kept", a.Status)
	}
}

func TestEvaluate_FailedSuspendKeepsTheStateAndRetries(t *testing.T) {
	s, pg, spy, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spy.fail = errors.New("host unreachable")
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	if err := s.Evaluate(ctx, "org_1"); err == nil {
		t.Fatal("a failed suspend came back nil")
	}
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusLimited {
		t.Fatalf("status %s: the limit holds even when the work couldn't be stopped yet", a.Status)
	}
	spy.fail = nil
	n := spy.count()
	if err := s.Evaluate(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spy.since(n), "suspend spending_limit") {
		t.Fatalf("no retry: %v", spy.since(n))
	}
	var epoch int64
	_ = pg.QueryRow(ctx, `SELECT limit_epoch FROM loomplane_billing_accounts WHERE org_id='org_1'`).Scan(&epoch)
	if epoch != 1 {
		t.Fatalf("epoch %d, want 1: a retry is not a second transition", epoch)
	}
}

func TestEvaluate_ConcurrentEvaluationsLimitOnce(t *testing.T) {
	s, pg, _, sent := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _ = s.Evaluate(ctx, "org_1") })
	}
	wg.Wait()
	var epoch int64
	_ = pg.QueryRow(ctx, `SELECT limit_epoch FROM loomplane_billing_accounts WHERE org_id='org_1'`).Scan(&epoch)
	limits := 0
	for _, n := range *sent {
		if n.Kind == NoticeLimit100 {
			limits++
		}
	}
	if epoch != 1 || limits != 1 {
		t.Fatalf("epoch %d, limit_100 sent %d times; want 1 and 1", epoch, limits)
	}
}

func TestEvaluate_NoCardDoesNothing(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-15T00:00:00Z"))
	spy := &suspenderSpy{}
	s.suspender = spy
	ctx := t.Context()
	if _, err := s.SetSpendingLimit(ctx, "org_2", ptr(0)); err != nil {
		t.Fatal(err)
	}
	spent(t, pg, "org_2", 900, at("2026-11-14T10:00:00Z"))
	if err := s.Evaluate(ctx, "org_2"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, "org_2"); a.Status != StatusPendingPayment || len(spy.calls) != 0 || a.SpendingLimitCents == nil || *a.SpendingLimitCents != 0 {
		t.Fatalf("%+v %v", a, spy.calls)
	}
}

func TestSetSpendingLimit_RefusesOutOfRange(t *testing.T) {
	s, _, _, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	for _, c := range []int64{-1, maxSpendingLimitCents + 1} {
		if _, err := s.SetSpendingLimit(t.Context(), "org_1", ptr(c)); err == nil {
			t.Fatalf("limit %d saved", c)
		}
	}
	if a, err := s.SetSpendingLimit(t.Context(), "org_1", ptr(maxSpendingLimitCents)); err != nil || *a.SpendingLimitCents != maxSpendingLimitCents {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestSpend_TotalsTheMonth(t *testing.T) {
	s, pg, _, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	// 700.5 cents of base over two features, each rounded down on its own:
	// 300 + 400 = 700. AI 1500. Usage outside November doesn't count.
	spentMicro(t, pg, "org_1", "compute_vcpu_hours", 3_005_000, at("2026-11-02T00:00:00Z"))
	spentMicro(t, pg, "org_1", "egress_gb", 4_000_000, at("2026-11-30T23:59:59Z"))
	spentMicro(t, pg, "org_1", FeatureAIUSD, 15_000_000, at("2026-11-03T00:00:00Z"))
	spentMicro(t, pg, "org_1", "egress_gb", 9_000_000, at("2026-10-31T23:59:59Z"))
	spentMicro(t, pg, "org_1", "egress_gb", 9_000_000, at("2026-12-01T00:00:00Z"))
	spentMicro(t, pg, "org_9", "egress_gb", 9_000_000, at("2026-11-05T00:00:00Z"))

	sp, err := s.Spend(ctx, "org_1", at("2026-11-20T00:00:00Z"))
	want := PeriodSpend{BaseUsageCents: 700, AIUsageCents: 1500, OverageCents: 200 + 1500}
	if err != nil || sp != want {
		t.Fatalf("no add-on: %+v %v, want %+v", sp, err, want)
	}
	if _, err = pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id='sub_ai' WHERE org_id='org_1'`); err != nil {
		t.Fatal(err)
	}
	sp, err = s.Spend(ctx, "org_1", at("2026-11-01T00:00:00Z"))
	want.OverageCents = 200 + 500
	if err != nil || sp != want {
		t.Fatalf("with the add-on: %+v %v, want %+v", sp, err, want)
	}
	// Turned off this month: on until December, so November keeps its credit.
	if _, err = pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_ends_at='2026-12-01T00:00:00Z' WHERE org_id='org_1'`); err != nil {
		t.Fatal(err)
	}
	if sp, err = s.Spend(ctx, "org_1", at("2026-11-15T00:00:00Z")); err != nil || sp != want {
		t.Fatalf("add-on ending at the month's end: %+v %v, want %+v", sp, err, want)
	}
}

func TestEvaluate_AICreditCountsTowardTheZeroLimit(t *testing.T) {
	s, pg, _, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id='sub_ai' WHERE org_id='org_1'`)
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(0))
	spentMicro(t, pg, "org_1", FeatureAIUSD, 9_990_000, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusActive {
		t.Fatalf("$9.99 of $10 AI credit with a $0 limit: %s", a.Status)
	}
	spentMicro(t, pg, "org_1", FeatureAIUSD, 10_000, at("2026-11-14T11:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	if a, _ := s.Account(ctx, "org_1"); a.Status != StatusLimited {
		t.Fatalf("AI credit used up with a $0 limit: %s", a.Status)
	}
}

func TestEvaluate_CreditAndLimitNotices(t *testing.T) {
	s, pg, _, sent := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(1000))
	find := func(kind string) *notifications.Notice {
		for i := range *sent {
			if (*sent)[i].Kind == kind {
				return &(*sent)[i]
			}
		}
		return nil
	}

	spent(t, pg, "org_1", 399, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	if len(*sent) != 0 {
		t.Fatalf("under 80%%: %v", *sent)
	}
	spent(t, pg, "org_1", 1, at("2026-11-14T10:01:00Z")) // 400
	_ = s.Evaluate(ctx, "org_1")
	n := find(NoticeCredit80)
	if n == nil || n.SubjectID != "credit_80:2026-11" || n.OrgID != "org_1" || n.Path != "/settings/billing" ||
		n.Title != "80% of this month's included usage is used" ||
		n.Body != "This workspace has used $4.00 of the $5.00 of usage included this month. Usage past $5.00 is added to next month's invoice." {
		t.Fatalf("credit_80: %+v", n)
	}
	spent(t, pg, "org_1", 100, at("2026-11-14T10:02:00Z")) // 500
	_ = s.Evaluate(ctx, "org_1")
	n = find(NoticeCredit100)
	if n == nil || n.SubjectID != "credit_100:2026-11" || n.Title != "This month's included usage is used" ||
		n.Body != "Usage from now until the month ends is added to next month's invoice." {
		t.Fatalf("credit_100: %+v", n)
	}
	if find(NoticeLimit80) != nil {
		t.Fatal("limit_80 at no overage")
	}
	spent(t, pg, "org_1", 850, at("2026-11-14T10:03:00Z")) // 850 overage of a 1000 limit
	_ = s.Evaluate(ctx, "org_1")
	n = find(NoticeLimit80)
	if n == nil || n.SubjectID != "limit_80:2026-11:1000" || n.Title != "80% of the spending limit is used" ||
		n.Body != "This workspace has spent $8.50 of its $10.00 monthly spending limit on usage past what's included." {
		t.Fatalf("limit_80: %+v", n)
	}
	if find(NoticeLimit100) != nil {
		t.Fatal("limit_100 under the limit")
	}
	spent(t, pg, "org_1", 150, at("2026-11-14T10:04:00Z")) // 1000 overage
	_ = s.Evaluate(ctx, "org_1")
	n = find(NoticeLimit100)
	if n == nil || n.SubjectID != "limit_100:2026-11:1" || n.Title != "Spending limit reached" ||
		n.Body != "Loomplane Cloud work in this workspace is stopped and AI on Loomplane's model keys is paused until the month ends or an owner raises the limit in Settings > Billing. Nothing on your own providers is touched." {
		t.Fatalf("limit_100: %+v", n)
	}
	for _, n := range *sent {
		if len([]rune(n.Title)) > 200 || len([]rune(n.Body)) > 2000 {
			t.Fatalf("too long: %+v", n)
		}
	}
}

func TestEvaluate_ZeroLimitSendsNoLimit80(t *testing.T) {
	s, pg, _, sent := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(0))
	spent(t, pg, "org_1", 450, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	if hasKind(*sent, NoticeLimit80) || hasKind(*sent, NoticeLimit100) || !hasKind(*sent, NoticeCredit80) {
		t.Fatalf("%v", *sent)
	}
}

func TestEvaluate_LimitedDropsTheCachedAIAnswer(t *testing.T) {
	s, pg, _, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET ai_subscription_id='sub_ai' WHERE org_id='org_1'`)
	_, _ = s.SetSpendingLimit(ctx, "org_1", ptr(100))
	if err := s.Gate().CanRun(ctx, "org_1", metering.KindAIPlatform, false); err != nil {
		t.Fatalf("before the limit: %v", err)
	}
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	_ = s.Evaluate(ctx, "org_1")
	var blocked *metering.BlockedError
	if err := s.Gate().CanRun(ctx, "org_1", metering.KindAIPlatform, false); !errors.As(err, &blocked) {
		t.Fatalf("right after the limit: %v, want the cache dropped", err)
	}
}

func TestEvaluateAll_EveryCardedAccount(t *testing.T) {
	s, pg, spy, _ := limitedWorld(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	if err := s.MarkCardReady(ctx, "org_2"); err != nil {
		t.Fatal(err)
	}
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_accounts SET spending_limit_cents=100`)
	_, _ = s.SetSpendingLimit(ctx, "org_3", ptr(0)) // no card
	spent(t, pg, "org_1", 900, at("2026-11-14T10:00:00Z"))
	spent(t, pg, "org_2", 100, at("2026-11-14T10:00:00Z"))
	spent(t, pg, "org_3", 900, at("2026-11-14T10:00:01Z")) // the helper's ids are cents and time
	n := spy.count()
	if err := s.EvaluateAll(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]Status{"org_1": StatusLimited, "org_2": StatusActive, "org_3": StatusPendingPayment}
	for org, st := range want {
		if a, _ := s.Account(ctx, org); a.Status != st {
			t.Fatalf("%s is %s, want %s", org, a.Status, st)
		}
	}
	// Two carded workspaces, two reasons each.
	if calls := spy.since(n); len(calls) != 4 {
		t.Fatalf("calls %v", calls)
	}
}

func TestEnqueue_EachWorkspaceTellsASharedAdmin(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-15T00:00:00Z"))
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO authsome_apps(id,name,slug) VALUES('app','App','app')`,
		`INSERT INTO authsome_environments(id,app_id,name,slug,type,is_default) VALUES('env','app','Production','production','production',true)`,
		`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES ('usr_ada','app','env','ada@example.com','','')`,
		`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by) VALUES ('org_a','app','env','A','a','usr_ada'),('org_b','app','env','B','b','usr_ada')`,
		`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES ('amem_a','org_a','usr_ada','owner'),('amem_b','org_b','usr_ada','admin')`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, org := range []string{"org_a", "org_b"} {
		for range 2 {
			s.sendNotice(ctx, org, NoticeCredit80, "credit_80:2026-11", "t", "b")
		}
	}
	rows, err := pg.Query(ctx, `SELECT org_id, user_id, subject_id FROM loomplane_notification_outbox WHERE kind=$1 ORDER BY org_id`, NoticeCredit80)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var org, user, subject string
		if err = rows.Scan(&org, &user, &subject); err != nil {
			t.Fatal(err)
		}
		got = append(got, org+" "+user+" "+subject)
	}
	want := []string{"org_a usr_ada org_a:credit_80:2026-11", "org_b usr_ada org_b:credit_80:2026-11"}
	if !slices.Equal(got, want) {
		t.Fatalf("outbox %v, want %v", got, want)
	}
}

func hasKind(ns []notifications.Notice, kind string) bool {
	return slices.ContainsFunc(ns, func(n notifications.Notice) bool { return n.Kind == kind })
}
