//go:build cloud

package billing

import (
	"math"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger/meter"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/platformcloud"
)

func newTestService(t *testing.T, now time.Time) (*Service, *pgdriver.PgDB) {
	t.Helper()
	db, pg := scratchDB(t)
	l := newLedgerWith(db, nil)
	if err := SeedCatalog(t.Context(), l, testAppID); err != nil {
		t.Fatal(err)
	}
	clock := now
	return New(Deps{DB: db, Ledger: l, AppID: testAppID, Now: func() time.Time { return clock }}), pg
}

func compute(id string, q float64, when time.Time) metering.Event {
	return metering.Event{ID: id, OrgID: "org_1", Feature: platformcloud.FeatureComputeVCPUHours, ResourceRef: "pl_1", Quantity: q, OccurredAt: when, PlatformOwned: true}
}

func TestRecord_DuplicateIDCountsOnce(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 20_000, "rex") // 2 cents per vCPU-hour
	e := compute("platform:pl_1:1:2026-11-09T10:00:00Z:compute_vcpu_hours", 2, at("2026-11-09T10:00:00Z"))
	for range 3 {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var rows, micro int64
	if err := pg.QueryRow(ctx, `SELECT count(*), coalesce(sum(micro_usd),0) FROM loomplane_billing_usage`).Scan(&rows, &micro); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || micro != 40_000 {
		t.Fatalf("rows %d micro %d, want 1 and 40000", rows, micro)
	}
}

func TestRecord_NonPlatformIsFree(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	e := compute("e1", 5, at("2026-11-09T10:00:00Z"))
	e.PlatformOwned = false
	if err := s.Record(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	var rows int
	_ = pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_billing_usage`).Scan(&rows)
	if rows != 0 {
		t.Fatal("a customer's own work was recorded")
	}
}

func TestRecord_UnknownFeatureFails(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-10T00:00:00Z"))
	e := compute("e1", 1, at("2026-11-09T10:00:00Z"))
	e.Feature = "compute_vcpu_hour"
	if err := s.Record(t.Context(), e); err == nil {
		t.Fatal("a misspelt feature was accepted")
	}
}

func TestRecord_RateChangeNotRetroactive(t *testing.T) {
	s, _ := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	_, _ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex") // 1 cent
	_ = s.Record(ctx, compute("a", 10, at("2026-11-09T10:00:00Z")))             // 10 cents
	_, _ = s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 30_000, "rex") // 3 cents
	_ = s.Record(ctx, compute("b", 10, at("2026-11-09T11:00:00Z")))             // 30 cents
	if _, err := s.PushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledgerCents(t, s, "org_1", platformcloud.FeatureComputeVCPUHours, at("2026-11-01T00:00:00Z"), at("2026-12-01T00:00:00Z")); got != 40 {
		t.Fatalf("ledger has %d cents, want 40", got)
	}
}

// ledgerCents sums what ledger holds for a feature in a window.
func ledgerCents(t *testing.T, s *Service, org, feature string, from, to time.Time) int64 {
	t.Helper()
	events, err := s.l.Store().QueryUsage(t.Context(), org, testAppID, meter.QueryOpts{FeatureKey: feature, Start: from, End: to})
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range events {
		n += e.Quantity
	}
	return n
}

func TestRecord_RateChangeKeepsTheRecordedPrice(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	if _, err := s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex"); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, compute("a", 10, at("2026-11-09T10:00:00Z"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 30_000, "rex"); err != nil {
		t.Fatal(err)
	}
	// A replay of the same event after the change keeps the first price.
	if err := s.Record(ctx, compute("a", 10, at("2026-11-09T10:00:00Z"))); err != nil {
		t.Fatal(err)
	}
	var rate, micro int64
	if err := pg.QueryRow(ctx, `SELECT rate_micro_usd, micro_usd FROM loomplane_billing_usage WHERE id='a'`).Scan(&rate, &micro); err != nil {
		t.Fatal(err)
	}
	if rate != 10_000 || micro != 100_000 {
		t.Fatalf("rate %d micro %d, want the old 10000 and 100000", rate, micro)
	}
}

func TestRecord_RoundsToTheNearestMicroUSD(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	if _, err := s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRate(ctx, platformcloud.FeatureEgressGB, 4_000, "rex"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRate(ctx, platformcloud.FeatureBuildMinutes, 3, "rex"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id      string
		feature string
		q       float64
		want    int64
	}{
		{"one-minute", platformcloud.FeatureComputeVCPUHours, 1.0 / 60, 167},  // 166.67
		{"two-minutes", platformcloud.FeatureComputeVCPUHours, 2.0 / 60, 333}, // 333.33
		{"half", platformcloud.FeatureBuildMinutes, 0.5, 2},                   // 1.5 rounds away from zero
		{"tiny", platformcloud.FeatureEgressGB, 0.0001, 0},                    // 0.4 rounds down
		{"zero", platformcloud.FeatureEgressGB, 0, 0},
	} {
		e := compute(c.id, c.q, at("2026-11-09T10:00:00Z"))
		e.Feature = c.feature
		if err := s.Record(ctx, e); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		var micro int64
		if err := pg.QueryRow(ctx, `SELECT micro_usd FROM loomplane_billing_usage WHERE id=$1`, c.id).Scan(&micro); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if micro != c.want {
			t.Errorf("%s: %d micro-USD, want %d", c.id, micro, c.want)
		}
	}
}

func TestRecord_RefusesWhatCantBeUsage(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	if _, err := s.SetRate(ctx, platformcloud.FeatureComputeVCPUHours, 10_000, "rex"); err != nil {
		t.Fatal(err)
	}
	base := compute("bad", 1, at("2026-11-09T10:00:00Z"))
	for name, mutate := range map[string]func(*metering.Event){
		"negative":   func(e *metering.Event) { e.Quantity = -1 },
		"NaN":        func(e *metering.Event) { e.Quantity = math.NaN() },
		"infinite":   func(e *metering.Event) { e.Quantity = math.Inf(1) },
		"huge":       func(e *metering.Event) { e.Quantity = 1e18 },
		"no id":      func(e *metering.Event) { e.ID = "" },
		"no org":     func(e *metering.Event) { e.OrgID = "" },
		"no time":    func(e *metering.Event) { e.OccurredAt = time.Time{} },
		"no feature": func(e *metering.Event) { e.Feature = "" },
	} {
		e := base
		mutate(&e)
		if err := s.Record(ctx, e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var rows int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_usage`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("%d bad events were recorded", rows)
	}
}

func TestRecord_ClaimsAPlatformAIRequestOnce(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	e := metering.Event{ID: "ai-request:req_1", OrgID: "org_1", Feature: agentsettings.FeatureAIPlatformRequests,
		ResourceRef: "nt_1:req_1", Quantity: 1, OccurredAt: at("2026-11-09T10:00:00Z"), PlatformOwned: true}
	for range 2 {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var (
		claims  int
		org     string
		tenant  string
		claimed time.Time
	)
	if err := pg.QueryRow(ctx, `SELECT count(*), min(org_id), min(nexus_tenant), min(claimed_at) FROM loomplane_billing_ai_claims WHERE request_id='req_1'`).
		Scan(&claims, &org, &tenant, &claimed); err != nil {
		t.Fatal(err)
	}
	if claims != 1 || org != "org_1" || tenant != "nt_1" || !claimed.Equal(e.OccurredAt) {
		t.Fatalf("claims %d org %q tenant %q at %v", claims, org, tenant, claimed)
	}
	var usage int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_usage`).Scan(&usage)
	if usage != 0 {
		t.Fatal("a claim was priced as usage")
	}
	e.ID, e.ResourceRef = "req_2", "nt_1:req_2"
	if err := s.Record(ctx, e); err == nil {
		t.Fatal("a claim without the ai-request prefix was accepted")
	}
}

func TestRates_SeededAtZeroAndSetByRex(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	rates, err := s.Rates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rates) != len(Features) {
		t.Fatalf("rates %v", rates)
	}
	for _, f := range Features {
		if r, ok := rates[f]; !ok || r != 0 {
			t.Fatalf("%s rate %d (%v), want 0", f, r, ok)
		}
	}
	if _, err = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex"); err != nil {
		t.Fatal(err)
	}
	var by string
	_ = pg.QueryRow(ctx, `SELECT updated_by FROM loomplane_billing_rates WHERE feature='ai_usd'`).Scan(&by)
	if rates, _ = s.Rates(ctx); rates[FeatureAIUSD] != 1_000_000 || by != "rex" {
		t.Fatalf("after SetRate: %v by %q", rates, by)
	}
	setErr := func(feature string, micro int64, by string) error {
		_, err := s.SetRate(ctx, feature, micro, by)
		return err
	}
	for name, err := range map[string]error{
		"unknown feature": setErr("gpu_hours", 1, "rex"),
		"negative":        setErr(FeatureAIUSD, -1, "rex"),
		"nobody":          setErr(FeatureAIUSD, 1, " "),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRecord_ZeroRateRecordsAtNoCost(t *testing.T) {
	s, pg := newTestService(t, at("2026-11-10T00:00:00Z"))
	ctx := t.Context()
	if err := s.Record(ctx, compute("a", 4, at("2026-11-09T10:00:00Z"))); err != nil {
		t.Fatal(err)
	}
	var micro int64
	if err := pg.QueryRow(ctx, `SELECT micro_usd FROM loomplane_billing_usage WHERE id='a'`).Scan(&micro); err != nil || micro != 0 {
		t.Fatalf("micro %d %v, want 0 at the seeded rate", micro, err)
	}
}
