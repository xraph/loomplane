//go:build cloud

package billing

import (
	"fmt"
	"slices"
	"testing"

	"github.com/xraph/ledger/meter"

	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/platformcloud"
)

func TestPushUsage_CarriesFractionalCents(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureEgressGB, 4_000, "rex") // 0.4 cents per GB
	for i, h := range []string{"T01", "T02", "T03"} {
		_ = s.Record(ctx, metering.Event{ID: fmt.Sprintf("eg%d", i), OrgID: "org_1", Feature: platformcloud.FeatureEgressGB,
			Quantity: 1, OccurredAt: at("2026-11-09" + h + ":00:00Z"), PlatformOwned: true, ResourceRef: "pl_1"})
	}
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureEgressGB, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 1 {
		t.Fatalf("pushed %d cents, want 1", got)
	}
	var carry int64
	_ = pg.QueryRow(ctx, `SELECT micro_usd_remainder FROM loomplane_billing_carry WHERE org_id='org_1' AND feature='egress_gb'`).Scan(&carry)
	if carry != 2_000 {
		t.Fatalf("carry %d, want 2000 (0.2 cents)", carry)
	}
}

func TestPushUsage_LastHourBillsInItsMonth(t *testing.T) {
	// Platform Cloud emits October's last hour at about 00:02 on 1 November.
	s, _ := newTestService(t, at("2026-11-01T00:02:30Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("platform:pl_1:1:2026-10-31T23:00:00Z:compute_vcpu_hours", 3, at("2026-10-31T23:00:00Z")))
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureComputeVCPUHours, at("2026-10-01T00:00:00Z"), at("2026-11-01T00:00:00Z")); got != 3 {
		t.Fatalf("October holds %d cents, want 3", got)
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 0 {
		t.Fatalf("November holds %d cents, want 0", got)
	}
}

func TestPushUsage_MonthsNeverShareABatch(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-01T00:05:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("oct", 1, at("2026-10-31T23:00:00Z")))
	_ = s.Record(ctx, compute("nov", 1, at("2026-11-01T00:00:00Z")))
	_, _ = s.PushUsage(ctx)
	var pushes int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_pushes`).Scan(&pushes)
	if pushes != 2 {
		t.Fatalf("pushes %d, want one per month", pushes)
	}
}

func TestPushUsage_RetryAfterCrashCountsOnce(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("a", 5, at("2026-11-09T10:00:00Z")))
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	// The crash: ledger took the push, the sent mark never landed.
	if _, err := pg.Exec(ctx, `UPDATE loomplane_billing_pushes SET sent_at = NULL`); err != nil {
		t.Fatal(err)
	}
	_ = s.Record(ctx, compute("b", 7, at("2026-11-09T11:00:00Z"))) // arrives before the retry
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 12 {
		t.Fatalf("ledger has %d cents, want 12 (5 once, 7 once)", got)
	}
}

// The tests below go past the brief's: the money edge cases around them.

func TestPushUsage_MonthsKeepTheirOwnPriceAndCarry(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-01T00:05:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureEgressGB, 6_000, "rex") // 0.6 cents per GB
	_ = s.Record(ctx, metering.Event{ID: "oct", OrgID: "org_1", Feature: platformcloud.FeatureEgressGB, Quantity: 1,
		OccurredAt: at("2026-10-31T23:00:00Z"), PlatformOwned: true, ResourceRef: "pl_1"})
	_ = s.Record(ctx, metering.Event{ID: "nov", OrgID: "org_1", Feature: platformcloud.FeatureEgressGB, Quantity: 1,
		OccurredAt: at("2026-11-01T00:00:00Z"), PlatformOwned: true, ResourceRef: "pl_1"})
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	// 0.6 + 0.6 would be a cent in one batch; in two months it is none.
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureEgressGB, at("2026-10-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 0 {
		t.Fatalf("ledger has %d cents across the two months, want 0", got)
	}
	var oct, nov int64
	_ = pg.QueryRow(ctx, `SELECT micro_usd_remainder FROM loomplane_billing_carry WHERE period='2026-10-01'`).Scan(&oct)
	_ = pg.QueryRow(ctx, `SELECT micro_usd_remainder FROM loomplane_billing_carry WHERE period='2026-11-01'`).Scan(&nov)
	if oct != 6_000 || nov != 6_000 {
		t.Fatalf("carry October %d November %d, want 6000 each", oct, nov)
	}
}

func TestPushUsage_CarryAddsUpAcrossPushes(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureEgressGB, 4_000, "rex") // 0.4 cents per GB
	want := []int64{0, 0, 1, 1, 2}                                  // 0.4, 0.8, 1.2, 1.6, 2.0
	for i, w := range want {
		_ = s.Record(ctx, metering.Event{ID: fmt.Sprintf("eg%d", i), OrgID: "org_1", Feature: platformcloud.FeatureEgressGB, Quantity: 1,
			OccurredAt: at(fmt.Sprintf("2026-11-09T%02d:00:00Z", i)), PlatformOwned: true, ResourceRef: "pl_1"})
		if _, err := s.PushUsage(ctx); err != nil {
			t.Fatal(err)
		}
		if got := ledgerCents(t, s, "org_1", platformcloud.FeatureEgressGB, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != w {
			t.Fatalf("after push %d ledger has %d cents, want %d", i, got, w)
		}
	}
	var carry int64
	_ = pg.QueryRow(ctx, `SELECT micro_usd_remainder FROM loomplane_billing_carry`).Scan(&carry)
	if carry != 0 {
		t.Fatalf("carry %d, want 0", carry)
	}
}

func TestPushUsage_ReplayedEventAfterAPushCountsOnce(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	e := compute("platform:pl_1:1:2026-11-09T10:00:00Z:compute_vcpu_hours", 4, at("2026-11-09T10:00:00Z"))
	for range 3 {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PushUsage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 4 {
		t.Fatalf("ledger has %d cents, want 4", got)
	}
}

func TestPushUsage_AZeroCentPushIsMarkedSentAndSendsNothing(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.Record(ctx, compute("a", 5, at("2026-11-09T10:00:00Z"))) // rate 0
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	var pushes, unsent, unpushed int
	_ = pg.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE sent_at IS NULL) FROM loomplane_billing_pushes`).Scan(&pushes, &unsent)
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_usage WHERE push_id IS NULL`).Scan(&unpushed)
	if pushes != 1 || unsent != 0 || unpushed != 0 {
		t.Fatalf("pushes %d unsent %d unpushed %d", pushes, unsent, unpushed)
	}
	events, err := s.l.Store().QueryUsage(ctx, "org_1", testAppID, metermatch(platformcloud.FeatureComputeVCPUHours))
	if err != nil || len(events) != 0 {
		t.Fatalf("ledger got %d events, %v", len(events), err)
	}
}

func TestPushUsage_LedgerEventsCarryTheirMonthAndKey(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("a", 2, at("2026-11-09T10:00:00Z")))
	_ = s.Record(ctx, compute("b", 2, at("2026-11-09T14:00:00Z")))
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	var pushID string
	_ = pg.QueryRow(ctx, `SELECT id FROM loomplane_billing_pushes`).Scan(&pushID)
	events, err := s.l.Store().QueryUsage(ctx, "org_1", testAppID, metermatch(platformcloud.FeatureComputeVCPUHours))
	if err != nil || len(events) != 1 {
		t.Fatalf("%d events, %v", len(events), err)
	}
	e := events[0]
	if e.Quantity != 4 || !e.Timestamp.Equal(at("2026-11-09T14:00:00Z")) || e.IdempotencyKey != "loomplane:"+pushID || e.AppID != testAppID {
		t.Fatalf("ledger event %+v (push %s)", e, pushID)
	}
	if len(pushID) < len("lpush_") || pushID[:len("lpush_")] != "lpush_" {
		t.Fatalf("push id %q", pushID)
	}
}

func TestPushUsage_LateUsageIsStillSent(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-02T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_invoices (invoice_id, org_id, subscription_id, plan, period_start)
		VALUES ('inv_1', 'org_1', 'sub_1', 'cloud', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = s.Record(ctx, compute("late", 2, at("2026-10-31T23:00:00Z")))
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureComputeVCPUHours, at("2026-10-01T00:00:00Z"), at("2026-11-01T00:00:00Z")); got != 2 {
		t.Fatalf("October holds %d cents, want the late 2 recorded", got)
	}
}

func TestPushUsage_ReturnsTheWorkspacesItTouched(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	for i, org := range []string{"org_b", "org_a", "org_b"} {
		e := compute(fmt.Sprintf("e%d", i), 1, at("2026-11-09T10:00:00Z"))
		e.OrgID = org
		_ = s.Record(ctx, e)
	}
	touched, err := s.PushUsage(ctx)
	if err != nil || !slices.Equal(touched, []string{"org_a", "org_b"}) {
		t.Fatalf("touched %v, %v", touched, err)
	}
	if got := ledgerCents(t, s, "org_b", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 2 {
		t.Fatalf("org_b has %d cents, want 2", got)
	}
	if touched, err = s.PushUsage(ctx); err != nil || len(touched) != 0 {
		t.Fatalf("a push with nothing to do touched %v, %v", touched, err)
	}
}

func TestPushUsage_WaitsWhileAnotherReplicaPushes(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	_ = s.Record(ctx, compute("a", 1, at("2026-11-09T10:00:00Z")))
	tx, err := pg.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, pushLock); err != nil {
		t.Fatal(err)
	}
	touched, err := s.PushUsage(ctx)
	_ = tx.Rollback()
	if err != nil || len(touched) != 0 {
		t.Fatalf("pushed while another replica held the lock: %v %v", touched, err)
	}
	var pushes int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_pushes`).Scan(&pushes)
	if pushes != 0 {
		t.Fatalf("%d pushes made under another replica's lock", pushes)
	}
	if _, err = s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 1 {
		t.Fatalf("ledger has %d cents after the lock went, want 1", got)
	}
}

// metermatch is every ledger event of one feature.
func metermatch(feature string) meter.QueryOpts { return meter.QueryOpts{FeatureKey: feature} }

// One workspace's group that can't be batched is logged and left for the
// next push; every other workspace's usage still reaches ledger.
func TestPushUsage_OneBadGroupDoesntBlockAnotherWorkspace(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex")
	// org_a sorts first, so its group is tried before org_b's.
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_usage (id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ('bad', 'org_a', 'compute_vcpu_hours', 1, 10000, -1, '2026-11-09T10:00:00Z', 'pl_a')`); err != nil {
		t.Fatal(err)
	}
	b := compute("good", 3, at("2026-11-09T10:00:00Z"))
	b.OrgID = "org_b"
	_ = s.Record(ctx, b)
	touched, err := s.PushUsage(ctx)
	if err == nil {
		t.Fatal("the bad group's error was dropped")
	}
	if len(touched) != 1 || touched[0] != "org_b" {
		t.Fatalf("touched %v, want org_b", touched)
	}
	if got := ledgerCents(t, s, "org_b", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 3 {
		t.Fatalf("org_b has %d cents, want 3", got)
	}
	var waiting int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_usage WHERE id = 'bad' AND push_id IS NULL`).Scan(&waiting)
	if waiting != 1 {
		t.Fatal("the bad row was marked pushed")
	}
	// Put right, it goes on the next push.
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_usage SET micro_usd = 20000 WHERE id = 'bad'`)
	if _, err = s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledgerCents(t, s, "org_a", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 2 {
		t.Fatalf("org_a has %d cents, want 2", got)
	}
}
