//go:build cloud

package billing

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	nexusid "github.com/xraph/nexus/id"
	"github.com/xraph/nexus/money"
	nexuspg "github.com/xraph/nexus/store/postgres"
	"github.com/xraph/nexus/tenant"
	"github.com/xraph/nexus/usage"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/metering"
)

// nexusRecord writes a usage record the way nexus's usage middleware does.
func nexusRecord(t *testing.T, s *Service, tenant, request, cost string, when time.Time) string {
	t.Helper()
	usd, err := money.Parse(cost)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := nexusid.ParseRequestID(request)
	tid, _ := nexusid.ParseTenantID(tenant)
	rec := &usage.Record{ID: nexusid.NewUsageID(), TenantID: tid, RequestID: rid, Provider: "anthropic", Model: "claude", CostUSD: &usd, CreatedAt: when}
	if err := nexuspg.New(s.db).Usage().Insert(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	return rec.ID.String()
}

// nexusTenantID makes a nexus tenant, as the AI extension does for each
// workspace, and returns its id. Nexus's usage records need one.
func nexusTenantID(t *testing.T, s *Service) string {
	t.Helper()
	tid := nexusid.NewTenantID()
	now := time.Now().UTC()
	if err := nexuspg.New(s.db).Tenants().Insert(t.Context(), &tenant.Tenant{ID: tid, Name: tid.String(), Slug: tid.String(),
		Status: tenant.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return tid.String()
}

func nexusRequestID(t *testing.T) string {
	t.Helper()
	return nexusid.NewRequestID().String()
}

// claimAI claims a request on the server's key, as the credential resolver
// does.
func claimAI(t *testing.T, s *Service, tenant, request string, when time.Time) {
	t.Helper()
	if err := s.Record(t.Context(), metering.Event{ID: "ai-request:" + request, OrgID: "org_1", Feature: agentsettings.FeatureAIPlatformRequests,
		ResourceRef: tenant + ":" + request, Quantity: 1, OccurredAt: when, PlatformOwned: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPriceAI_OnlyClaimedRequestsBill(t *testing.T) {
	clock := at("2026-11-10T12:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.SetRate(ctx, FeatureAIUSD, 1_200_000, "rex") // cost plus 20%
	tenant, platformReq, ownKeyReq := nexusTenantID(t, s), nexusRequestID(t), nexusRequestID(t)
	_ = s.Record(ctx, metering.Event{ID: "ai-request:" + platformReq, OrgID: "org_1", Feature: agentsettings.FeatureAIPlatformRequests,
		ResourceRef: tenant + ":" + platformReq, Quantity: 1, OccurredAt: clock, PlatformOwned: true})
	nexusRecord(t, s, tenant, platformReq, "0.50", clock)
	nexusRecord(t, s, tenant, ownKeyReq, "9.00", clock) // the workspace's own key: never claimed
	if err := s.PriceAIClaims(ctx); err != nil {
		t.Fatal(err)
	}
	var micro int64
	_ = pg.QueryRow(ctx, `SELECT coalesce(sum(micro_usd),0) FROM loomplane_billing_usage WHERE feature='ai_usd'`).Scan(&micro)
	if micro != 600_000 {
		t.Fatalf("ai_usd micro-USD %d, want 600000 ($0.50 at cost plus 20%%)", micro)
	}
}

// Two runs over the same claim and record leave one ai_usd row, and so does
// a run after a crash between recording the usage and settling the claim.
func TestPriceAI_Idempotent(t *testing.T) {
	clock := at("2026-11-10T12:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex")
	tenant, req := nexusTenantID(t, s), nexusRequestID(t)
	claimAI(t, s, tenant, req, clock)
	recID := nexusRecord(t, s, tenant, req, "0.25", clock.Add(-2*time.Second))
	for range 2 {
		if err := s.PriceAIClaims(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// The crash: the usage row is there and the claim still open.
	if _, err := pg.Exec(ctx, `UPDATE loomplane_billing_ai_claims SET settled_at = NULL, usage_id = NULL`); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(15 * time.Minute)
	if err := s.PriceAIClaims(ctx); err != nil {
		t.Fatal(err)
	}
	var (
		rows, micro int64
		usageID     string
		occurred    time.Time
	)
	if err := pg.QueryRow(ctx, `SELECT count(*), coalesce(sum(micro_usd),0), coalesce(min(id),''), coalesce(min(occurred_at), now())
		FROM loomplane_billing_usage WHERE feature='ai_usd'`).Scan(&rows, &micro, &usageID, &occurred); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || micro != 250_000 || usageID != "ai:"+recID || !occurred.Equal(at("2026-11-10T11:59:58Z")) {
		t.Fatalf("rows %d micro %d id %q at %v", rows, micro, usageID, occurred)
	}
	var (
		settled bool
		claimed string
	)
	if err := pg.QueryRow(ctx, `SELECT settled_at IS NOT NULL, coalesce(usage_id,'') FROM loomplane_billing_ai_claims WHERE request_id=$1`, req).
		Scan(&settled, &claimed); err != nil {
		t.Fatal(err)
	}
	if !settled || claimed != usageID {
		t.Fatalf("claim settled %v usage %q", settled, claimed)
	}
}

// A claim 25 hours old with no nexus record is settled with no usage row
// and logged; one an hour old waits for its record.
func TestPriceAI_ClaimWithNoRecordClosesAfterADay(t *testing.T) {
	clock := at("2026-11-10T12:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex")
	tenant, stale, fresh := nexusTenantID(t, s), nexusRequestID(t), nexusRequestID(t)
	claimAI(t, s, tenant, stale, clock.Add(-25*time.Hour))
	claimAI(t, s, tenant, fresh, clock.Add(-time.Hour))
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	if err := s.PriceAIClaims(ctx); err != nil {
		t.Fatal(err)
	}
	var rows int64
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_usage`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("usage rows %d, want none", rows)
	}
	open := map[string]bool{}
	r, err := pg.Query(ctx, `SELECT request_id, settled_at IS NULL, usage_id IS NULL FROM loomplane_billing_ai_claims`)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var (
			req           string
			isOpen, empty bool
		)
		if err = r.Scan(&req, &isOpen, &empty); err != nil {
			t.Fatal(err)
		}
		if !empty {
			t.Errorf("claim %s has a usage id", req)
		}
		open[req] = isOpen
	}
	_ = r.Close()
	if open[stale] || !open[fresh] {
		t.Fatalf("open claims %v: the day-old one should close, the hour-old one wait", open)
	}
	if !strings.Contains(logged.String(), "no nexus usage record after a day") || !strings.Contains(logged.String(), stale) ||
		strings.Contains(logged.String(), fresh) {
		t.Fatalf("logged %q", logged.String())
	}

	// The hour-old one's record lands, and it bills.
	nexusRecord(t, s, tenant, fresh, "1.00", clock.Add(-time.Hour-time.Second))
	if err = s.PriceAIClaims(ctx); err != nil {
		t.Fatal(err)
	}
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_usage WHERE feature='ai_usd'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("usage rows %d after the record landed, want 1", rows)
	}
}

// A record nexus couldn't price bills nothing, settles its claim and is
// logged with its model; another tenant's records never bill this claim.
func TestPriceAI_UnpricedRecordAndOtherTenants(t *testing.T) {
	clock := at("2026-11-10T12:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex")
	tenant, other, req := nexusTenantID(t, s), nexusTenantID(t, s), nexusRequestID(t)
	claimAI(t, s, tenant, req, clock)
	rid, _ := nexusid.ParseRequestID(req)
	tid, _ := nexusid.ParseTenantID(tenant)
	if err := nexuspg.New(s.db).Usage().Insert(ctx, &usage.Record{ID: nexusid.NewUsageID(), TenantID: tid, RequestID: rid,
		Provider: "anthropic", Model: "claude-new", PricingStatus: usage.PricingUnpricedModel, CreatedAt: clock}); err != nil {
		t.Fatal(err)
	}
	// The same request id under another tenant is someone else's record.
	nexusRecord(t, s, other, req, "5.00", clock)
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	if err := s.PriceAIClaims(ctx); err != nil {
		t.Fatal(err)
	}
	var rows, micro int64
	_ = pg.QueryRow(ctx, `SELECT count(*), coalesce(sum(micro_usd),0) FROM loomplane_billing_usage WHERE feature='ai_usd'`).Scan(&rows, &micro)
	if rows != 1 || micro != 0 {
		t.Fatalf("rows %d micro %d, want one row of 0", rows, micro)
	}
	var settled bool
	_ = pg.QueryRow(ctx, `SELECT settled_at IS NOT NULL FROM loomplane_billing_ai_claims WHERE request_id=$1`, req).Scan(&settled)
	if !settled {
		t.Fatal("the unpriced request's claim is still open")
	}
	if !strings.Contains(logged.String(), "couldn't price") || !strings.Contains(logged.String(), "claude-new") {
		t.Fatalf("logged %q", logged.String())
	}
}

// More records than one page of nexus's: the claimed one on a later page
// still bills.
func TestPriceAI_ReadsEveryPage(t *testing.T) {
	clock := at("2026-11-10T12:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex")
	tenant, req := nexusTenantID(t, s), nexusRequestID(t)
	claimAI(t, s, tenant, req, clock.Add(-10*time.Minute))
	// Pages run newest id first, so the claimed record, written first, is
	// on the last page.
	nexusRecord(t, s, tenant, req, "2.00", clock.Add(-10*time.Minute))
	for range nexusPage + 5 {
		nexusRecord(t, s, tenant, nexusRequestID(t), "1.00", clock.Add(-5*time.Minute))
	}
	if err := s.PriceAIClaims(ctx); err != nil {
		t.Fatal(err)
	}
	var micro int64
	_ = pg.QueryRow(ctx, `SELECT coalesce(sum(micro_usd),0) FROM loomplane_billing_usage WHERE feature='ai_usd'`).Scan(&micro)
	if micro != 2_000_000 {
		t.Fatalf("micro-USD %d, want 2000000", micro)
	}
}

// The month close prices AI before it pushes, so October's last AI call is
// on October's invoice.
func TestCloseMonth_PricesAIBeforeThePush(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	_ = s.SetRate(ctx, FeatureAIUSD, 1_000_000, "rex")
	if _, err := s.SetAI(ctx, "org_1", true); err != nil {
		t.Fatal(err)
	}
	tenant, req := nexusTenantID(t, s), nexusRequestID(t)
	claimAI(t, s, tenant, req, at("2026-10-31T23:59:30Z"))
	nexusRecord(t, s, tenant, req, "15.00", at("2026-10-31T23:59:29Z"))
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	oct := aiInvoices(t, s)["2026-10"]
	// $10 base + $15 of AI - $10 credit.
	if len(oct) != 1 || oct[0].Total.Amount != 1500 {
		t.Fatalf("October's ai invoices %+v", oct)
	}
}
