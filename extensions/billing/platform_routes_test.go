//go:build cloud

package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/billing"
	"github.com/xraph/loomplane/internal/security"
)

// fakePlatform is the platform routes' billing service.
type fakePlatform struct {
	mu      sync.Mutex
	rates   map[string]int64
	rateBy  string
	credits []grantCall
	refunds []refundCall
	calls   int   // every call, reads included
	err     error // returned by every call when set
}

type grantCall struct {
	org, note, by string
	cents         int64
}

type refundCall struct {
	invoice, key, by string
	cents            *int64
}

func (f *fakePlatform) touch() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakePlatform) Overview(context.Context) (billing.Overview, error) {
	return billing.Overview{StripeMode: billing.StripeLive, Plans: []billing.PlanPrice{
		{Slug: billing.PlanCloud, BaseCents: 500, CreditCents: 500}, {Slug: billing.PlanAI, BaseCents: 1000, CreditCents: 1000},
	}}, f.touch()
}

func (f *fakePlatform) Rates(context.Context) (map[string]int64, error) {
	if err := f.touch(); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, feature := range billing.Features {
		out[feature] = f.rates[feature]
	}
	return out, nil
}

func (f *fakePlatform) SetRate(_ context.Context, feature string, micro int64, by string) error {
	if err := f.touch(); err != nil {
		return err
	}
	f.rates[feature], f.rateBy = micro, by
	return nil
}

func (f *fakePlatform) Workspaces(context.Context) ([]billing.WorkspaceBilling, error) {
	return []billing.WorkspaceBilling{{OrgID: "org_1", Name: "Acme", Status: billing.StatusActive,
		PeriodTotalCents: 1800, OverageCents: 300, AIEnabled: true}}, f.touch()
}

func (f *fakePlatform) PlatformInvoices(_ context.Context, org string) ([]billing.PlatformInvoice, error) {
	return []billing.PlatformInvoice{{InvoiceSummary: billing.InvoiceSummary{ID: "inv_" + org, TotalCents: 500, Status: "paid", Plan: "cloud"},
		RefundedCents: 300, RefundableCents: 200}}, f.touch()
}

func (f *fakePlatform) GrantCredit(_ context.Context, org string, cents int64, note, by string) error {
	if err := f.touch(); err != nil {
		return err
	}
	f.credits = append(f.credits, grantCall{org: org, note: note, by: by, cents: cents})
	return nil
}

func (f *fakePlatform) Refund(_ context.Context, inv string, cents *int64, key, by string) (bool, error) {
	if err := f.touch(); err != nil {
		return false, err
	}
	f.refunds = append(f.refunds, refundCall{invoice: inv, key: key, by: by, cents: cents})
	return cents == nil, nil
}

type auditEvent struct{ action, user, resource, id, outcome string }

type memAudit struct {
	mu     sync.Mutex
	events []auditEvent
	metas  []map[string]string
}

func (a *memAudit) Record(_ context.Context, action, userID, resource, id, outcome string, meta map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, auditEvent{action, userID, resource, id, outcome})
	a.metas = append(a.metas, meta)
}

// testPlatformOwner answers ok only for the session user platform_owner.
// It doesn't look at how the caller signed in, so a key or an agent of
// the owner reaches the routes' own gate, which must turn it away.
func testPlatformOwner(ctx context.Context) (string, bool) {
	p, ok := security.PrincipalFrom(ctx)
	if !ok || p.UserID != "platform_owner" {
		return "", false
	}
	return p.UserID, true
}

var (
	platformOwner       = caller{"X-User": "platform_owner"}
	platformAdmin       = caller{"X-User": "platform_admin"}
	workspaceOwner      = owner
	platformOwnersKey   = caller{"X-Key": "platform_owner"}
	platformOwnersAgent = caller{"X-Agent": "platform_owner"}
)

type platformRig struct {
	r     forge.Router
	svc   *fakePlatform
	audit *memAudit
}

func newPlatformRig(t *testing.T) *platformRig {
	t.Helper()
	rg := &platformRig{r: forge.NewRouter(), svc: &fakePlatform{rates: map[string]int64{}}, audit: &memAudit{}}
	if err := mountPlatformRoutes(rg.r, testGuard, testPlatformOwner, rg.svc, rg.audit); err != nil {
		t.Fatal(err)
	}
	return rg
}

func (rg *platformRig) do(t *testing.T, method, path, body string, who caller) *httptest.ResponseRecorder {
	t.Helper()
	return serve(t, rg.r, method, path, body, who)
}

// Every platform billing route, with a body each would accept.
var platformRoutesList = []struct{ method, path, body string }{
	{http.MethodGet, "/api/v1/platform/billing/access", ""},
	{http.MethodGet, "/api/v1/platform/billing/overview", ""},
	{http.MethodGet, "/api/v1/platform/billing/rates", ""},
	{http.MethodPut, "/api/v1/platform/billing/rates/egress_gb", `{"usd_per_unit":"0.01"}`},
	{http.MethodGet, "/api/v1/platform/billing/workspaces", ""},
	{http.MethodGet, "/api/v1/platform/billing/workspaces/org_1/invoices", ""},
	{http.MethodPost, "/api/v1/platform/billing/workspaces/org_1/credit", `{"usd":"5.00","note":"sorry"}`},
	{http.MethodPost, "/api/v1/platform/billing/invoices/inv_1/refund", `{"usd":null,"refund_key":"b8f0c1d2-3e4f-4a5b-8c6d-7e8f9a0b1c2d"}`},
}

func TestPlatformBilling_OnlyTheOwner(t *testing.T) {
	rg := newPlatformRig(t) // testPlatformOwner answers ok only for X-User: platform_owner
	for _, c := range []struct {
		who  caller
		want int
	}{{platformOwner, http.StatusNoContent}, {platformAdmin, http.StatusNotFound}, {workspaceOwner, http.StatusNotFound}, {ownersKey, http.StatusNotFound}} {
		if got := rg.do(t, http.MethodGet, "/api/v1/platform/billing/access", "", c.who).Code; got != c.want {
			t.Errorf("%v: %d, want %d", c.who, got, c.want)
		}
	}
}

// On every route, everyone but the owner signed in gets 404 and billing is
// never called; the owner gets through.
func TestPlatformBilling_EveryRouteIsTheOwners(t *testing.T) {
	for _, rt := range platformRoutesList {
		for _, who := range []caller{platformAdmin, workspaceOwner, admin, developer, ownersKey, platformOwnersKey, platformOwnersAgent, anonymous} {
			rg := newPlatformRig(t)
			res := rg.do(t, rt.method, rt.path, rt.body, who)
			if res.Code != http.StatusNotFound {
				t.Errorf("%s %s as %v: %d, want 404", rt.method, rt.path, who, res.Code)
			}
			if rg.svc.calls != 0 || len(rg.audit.events) != 0 {
				t.Errorf("%s %s as %v reached billing: %d calls, %d audit events", rt.method, rt.path, who, rg.svc.calls, len(rg.audit.events))
			}
		}
		rg := newPlatformRig(t)
		if res := rg.do(t, rt.method, rt.path, rt.body, platformOwner); res.Code >= 300 {
			t.Errorf("%s %s as the owner: %d %s", rt.method, rt.path, res.Code, res.Body)
		}
	}
}

func TestRates_RejectsTooPrecise(t *testing.T) {
	rg := newPlatformRig(t)
	for body, want := range map[string]int{
		`{"usd_per_unit":"0.012345"}`: http.StatusOK, `{"usd_per_unit":"0.0123456"}`: http.StatusUnprocessableEntity,
		`{"usd_per_unit":"-0.01"}`: http.StatusUnprocessableEntity, `{"usd_per_unit":"1000.01"}`: http.StatusUnprocessableEntity,
	} {
		if got := rg.do(t, http.MethodPut, "/api/v1/platform/billing/rates/egress_gb", body, platformOwner).Code; got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	if rg.svc.rates["egress_gb"] != 12_345 {
		t.Fatalf("stored %d micro-USD", rg.svc.rates["egress_gb"])
	}
	if len(rg.audit.events) != 1 {
		t.Fatalf("rate change audited %d times", len(rg.audit.events))
	}
	ev, meta := rg.audit.events[0], rg.audit.metas[0]
	if ev != (auditEvent{"billing.rate.set", "platform_owner", "billing_rate", "egress_gb", outcomeSuccess}) ||
		meta["old_micro_usd"] != "0" || meta["new_micro_usd"] != "12345" || rg.svc.rateBy != "platform_owner" {
		t.Fatalf("audit %+v %v, set by %q", ev, meta, rg.svc.rateBy)
	}
}

func TestRates_Bounds(t *testing.T) {
	rg := newPlatformRig(t)
	for body, want := range map[string]int{
		`{"usd_per_unit":"0"}`: http.StatusOK, `{"usd_per_unit":"1000"}`: http.StatusOK, `{"usd_per_unit":"1000.000000"}`: http.StatusOK,
		`{"usd_per_unit":"1000.000001"}`: http.StatusUnprocessableEntity, `{"usd_per_unit":0.01}`: http.StatusUnprocessableEntity,
		`{"usd_per_unit":"01"}`: http.StatusUnprocessableEntity, `{"usd_per_unit":"1e3"}`: http.StatusUnprocessableEntity,
		`{"usd_per_unit":""}`: http.StatusUnprocessableEntity, `{}`: http.StatusUnprocessableEntity, `nope`: http.StatusBadRequest,
	} {
		if got := rg.do(t, http.MethodPut, "/api/v1/platform/billing/rates/build_minutes", body, platformOwner).Code; got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	if got := rg.do(t, http.MethodPut, "/api/v1/platform/billing/rates/gold_bars", `{"usd_per_unit":"1"}`, platformOwner).Code; got != http.StatusNotFound {
		t.Errorf("unknown feature: %d", got)
	}
	if _, ok := rg.svc.rates["gold_bars"]; ok {
		t.Error("an unknown feature was priced")
	}
}

func TestRates_ListsEveryFeatureWithItsUnit(t *testing.T) {
	rg := newPlatformRig(t)
	rg.svc.rates["compute_vcpu_hours"] = 12_345
	res := rg.do(t, http.MethodGet, "/api/v1/platform/billing/rates", "", platformOwner)
	var rates []rateView
	if err := json.Unmarshal(res.Body.Bytes(), &rates); err != nil || res.Code != http.StatusOK {
		t.Fatalf("%d %s %v", res.Code, res.Body, err)
	}
	want := map[string]string{
		"build_minutes": "minute of build", "compute_vcpu_hours": "vCPU-hour", "compute_gb_ram_hours": "GiB-hour of memory",
		"storage_gb_months": "GB-month of storage", "egress_gb": "GB of egress (10^9 bytes)", "ai_usd": "USD of model cost",
	}
	if len(rates) != len(want) {
		t.Fatalf("rates %+v", rates)
	}
	for _, r := range rates {
		if want[r.Feature] != r.Unit {
			t.Errorf("%s: unit %q, want %q", r.Feature, r.Unit, want[r.Feature])
		}
		wantUSD := "0.000000"
		if r.Feature == "compute_vcpu_hours" {
			wantUSD = "0.012345"
		}
		if r.USDPerUnit != wantUSD {
			t.Errorf("%s: %s, want %s", r.Feature, r.USDPerUnit, wantUSD)
		}
	}
}

func TestOverview_ModeNeverTheKey(t *testing.T) {
	rg := newPlatformRig(t)
	res := rg.do(t, http.MethodGet, "/api/v1/platform/billing/overview", "", platformOwner)
	var v map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &v)
	plans, _ := v["plans"].([]any)
	if res.Code != http.StatusOK || v["stripe_mode"] != "live" || len(plans) != 2 || strings.Contains(res.Body.String(), "sk_") {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	p, _ := plans[0].(map[string]any)
	if p["slug"] != "cloud" || p["base_cents"] != float64(500) || p["credit_cents"] != float64(500) {
		t.Fatalf("plan %v", p)
	}
}

func TestWorkspacesAndInvoices_Shape(t *testing.T) {
	rg := newPlatformRig(t)
	res := rg.do(t, http.MethodGet, "/api/v1/platform/billing/workspaces", "", platformOwner)
	var ws []map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &ws)
	if len(ws) != 1 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	for k, want := range map[string]any{"org_id": "org_1", "name": "Acme", "status": "active", "period_total_cents": float64(1800),
		"overage_cents": float64(300), "ai_enabled": true} {
		if ws[0][k] != want {
			t.Errorf("workspace %s: %v, want %v", k, ws[0][k], want)
		}
	}
	res = rg.do(t, http.MethodGet, "/api/v1/platform/billing/workspaces/org_9/invoices", "", platformOwner)
	var invs []map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &invs)
	if len(invs) != 1 || invs[0]["id"] != "inv_org_9" || invs[0]["refunded_cents"] != float64(300) || invs[0]["refundable_cents"] != float64(200) {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
}

func TestCredit_Route(t *testing.T) {
	rg := newPlatformRig(t)
	for body, want := range map[string]int{
		`{"usd":"0.00"}`: http.StatusUnprocessableEntity, `{"usd":"-5.00"}`: http.StatusUnprocessableEntity,
		`{"usd":"5.001"}`: http.StatusUnprocessableEntity, `{"usd":"1000.01"}`: http.StatusUnprocessableEntity,
		`{"usd":5}`: http.StatusUnprocessableEntity, `{"note":"x"}`: http.StatusUnprocessableEntity,
		`{"usd":"5.00","note":"` + strings.Repeat("x", billing.MaxCreditNote+1) + `"}`: http.StatusUnprocessableEntity,
	} {
		if got := rg.do(t, http.MethodPost, "/api/v1/platform/billing/workspaces/org_1/credit", body, platformOwner).Code; got != want {
			t.Errorf("%.40s: %d, want %d", body, got, want)
		}
	}
	if len(rg.svc.credits) != 0 || len(rg.audit.events) != 0 {
		t.Fatalf("a refused credit reached billing: %v %v", rg.svc.credits, rg.audit.events)
	}
	if got := rg.do(t, http.MethodPost, "/api/v1/platform/billing/workspaces/org_7/credit", `{"usd":"1000.00","note":" outage "}`, platformOwner).Code; got != http.StatusNoContent {
		t.Fatalf("credit: %d", got)
	}
	if len(rg.svc.credits) != 1 || rg.svc.credits[0] != (grantCall{org: "org_7", note: "outage", by: "platform_owner", cents: 100_000}) {
		t.Fatalf("granted %+v", rg.svc.credits)
	}
	if len(rg.audit.events) != 1 || rg.audit.events[0] != (auditEvent{"billing.credit.grant", "platform_owner", "workspace", "org_7", outcomeSuccess}) ||
		rg.audit.metas[0]["cents"] != "100000" {
		t.Fatalf("audit %+v %v", rg.audit.events, rg.audit.metas)
	}
	rg.svc.err = billing.ErrNeedsPayment
	if got := rg.do(t, http.MethodPost, "/api/v1/platform/billing/workspaces/org_7/credit", `{"usd":"5.00"}`, platformOwner).Code; got != http.StatusConflict {
		t.Fatalf("no subscription: %d", got)
	}
	if last := rg.audit.events[len(rg.audit.events)-1]; last.outcome != outcomeFailure {
		t.Fatalf("a failed credit audited as %s", last.outcome)
	}
}

func TestRefund_Route(t *testing.T) {
	rg := newPlatformRig(t)
	const key = "b8f0c1d2-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
	for body, want := range map[string]int{
		`{"refund_key":"` + key + `"}`:                                   http.StatusUnprocessableEntity, // no usd: never "everything" by mistake
		`{"usd":"0.00","refund_key":"` + key + `"}`:                      http.StatusUnprocessableEntity,
		`{"usd":"3.001","refund_key":"` + key + `"}`:                     http.StatusUnprocessableEntity,
		`{"usd":3,"refund_key":"` + key + `"}`:                           http.StatusUnprocessableEntity,
		`{"usd":"3.00"}`:                                                 http.StatusUnprocessableEntity,
		`{"usd":"3.00","refund_key":"a b"}`:                              http.StatusUnprocessableEntity,
		`{"usd":"3.00","refund_key":"` + strings.Repeat("k", 201) + `"}`: http.StatusUnprocessableEntity,
	} {
		if got := rg.do(t, http.MethodPost, "/api/v1/platform/billing/invoices/inv_1/refund", body, platformOwner).Code; got != want {
			t.Errorf("%.50s: %d, want %d", body, got, want)
		}
	}
	if len(rg.svc.refunds) != 0 {
		t.Fatalf("a refused refund reached billing: %+v", rg.svc.refunds)
	}
	res := rg.do(t, http.MethodPost, "/api/v1/platform/billing/invoices/inv_1/refund", `{"usd":"3.00","refund_key":"`+key+`"}`, platformOwner)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"full":false`) {
		t.Fatalf("partial: %d %s", res.Code, res.Body)
	}
	res = rg.do(t, http.MethodPost, "/api/v1/platform/billing/invoices/inv_1/refund", `{"usd":null,"refund_key":"k2"}`, platformOwner)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"full":true`) {
		t.Fatalf("the rest: %d %s", res.Code, res.Body)
	}
	if len(rg.svc.refunds) != 2 || *rg.svc.refunds[0].cents != 300 || rg.svc.refunds[0].key != key || rg.svc.refunds[0].by != "platform_owner" ||
		rg.svc.refunds[1].cents != nil {
		t.Fatalf("refunds %+v", rg.svc.refunds)
	}
	if len(rg.audit.events) != 2 || rg.audit.events[0] != (auditEvent{"billing.invoice.refund", "platform_owner", "invoice", "inv_1", outcomeSuccess}) ||
		rg.audit.metas[0]["cents"] != "300" || rg.audit.metas[1]["cents"] != "rest" {
		t.Fatalf("audit %+v %v", rg.audit.events, rg.audit.metas)
	}
	for err, want := range map[error]int{
		fmt.Errorf("x: %w", billing.ErrRefundTooLarge):  http.StatusUnprocessableEntity,
		fmt.Errorf("x: %w", billing.ErrNotRefundable):   http.StatusConflict,
		fmt.Errorf("x: %w", billing.ErrRefundKeyReused): http.StatusConflict,
		billing.ErrInvoiceNotFound:                      http.StatusNotFound,
	} {
		rg.svc.err = err
		if got := rg.do(t, http.MethodPost, "/api/v1/platform/billing/invoices/inv_1/refund", `{"usd":"3.00","refund_key":"k3"}`, platformOwner).Code; got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
}

func TestPlatformErrorsNeverCarryInternalText(t *testing.T) {
	rg := newPlatformRig(t)
	rg.svc.err = errors.New("stripe: refund invoice in_1: Invalid API Key provided: sk_live_****cret")
	for _, rt := range platformRoutesList[1:] {
		res := rg.do(t, rt.method, rt.path, rt.body, platformOwner)
		if res.Code != http.StatusInternalServerError || strings.Contains(res.Body.String(), "stripe") || strings.Contains(res.Body.String(), "sk_") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, res.Code, res.Body)
		}
	}
}

func TestPlatformBillingStaysOutOfOpenAPI(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Loomplane", Version: "1", SpecEnabled: true}))
	if err := mountPlatformRoutes(r, testGuard, testPlatformOwner, &fakePlatform{rates: map[string]int64{}}, &memAudit{}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/api/v1/platform/billing") {
		t.Fatal("platform billing routes are in the OpenAPI spec")
	}
}

func TestParseFixedAndMicroDollars(t *testing.T) {
	for s, want := range map[string]int64{"0": 0, "0.000001": 1, "0.012345": 12_345, "1": 1_000_000, "1000": 1_000_000_000} {
		if got, ok := parseFixed(s, ratePattern, 6); !ok || got != want {
			t.Errorf("%s: %d %v, want %d", s, got, ok, want)
		}
		if got := microDollars(want); got != map[int64]string{0: "0.000000", 1: "0.000001", 12_345: "0.012345", 1_000_000: "1.000000", 1_000_000_000: "1000.000000"}[want] {
			t.Errorf("%d: %s", want, got)
		}
	}
}
