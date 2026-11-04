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
	"testing"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/ledger/provider"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/billing"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/security"
)

// fakeBilling is the routes' billing service. It records the workspace
// each call was for.
type fakeBilling struct {
	lastOrg    string
	lastUser   string
	limit      *int64
	limitErr   error
	aiSet      *bool // the last SetAI call's enabled
	aiErr      error
	invoiceOrg map[string]string // invoice id -> its workspace
	err        error             // returned by every read when set
	webhookErr error
	payload    []byte
	signature  string
}

func (f *fakeBilling) Account(_ context.Context, org string) (billing.Account, error) {
	f.lastOrg = org
	return billing.Account{OrgID: org, Status: billing.StatusPendingPayment}, f.err
}

func (f *fakeBilling) Summary(_ context.Context, org string) (billing.Summary, error) {
	f.lastOrg = org
	if f.err != nil {
		return billing.Summary{}, f.err
	}
	limit := int64(2500)
	return billing.Summary{
		Account: billing.Account{OrgID: org, Status: billing.StatusActive, SpendingLimitCents: &limit},
		Usage:   []billing.FeatureUsage{{Feature: "build_minutes", Cents: 120}},
		PaymentMethods: []provider.PaymentMethod{
			{ID: "pm_1", Brand: "visa", Last4: "4242", ExpiryMonth: 12, ExpiryYear: 2030, IsDefault: true, ProviderID: "pm_1"},
		},
		CreditUsedCents: 120, ProjectedTotalCents: 500,
	}, nil
}

func (f *fakeBilling) SetupSession(_ context.Context, org, user string) (string, error) {
	f.lastOrg, f.lastUser = org, user
	return "https://checkout.stripe.com/c/pay/cs_test", f.err
}

func (f *fakeBilling) SetSpendingLimit(_ context.Context, org string, cents *int64) (billing.Account, error) {
	f.lastOrg, f.limit = org, cents
	return billing.Account{OrgID: org, Status: billing.StatusActive, SpendingLimitCents: cents}, f.limitErr
}

func (f *fakeBilling) SetAI(_ context.Context, org string, enabled bool) (billing.Account, error) {
	f.lastOrg, f.aiSet = org, &enabled
	if f.aiErr != nil {
		return billing.Account{}, f.aiErr
	}
	a := billing.Account{OrgID: org, Status: billing.StatusActive, AIEnabled: true}
	if !enabled {
		ends := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
		a.AIEndsAt = &ends
	}
	return a, nil
}

func (f *fakeBilling) Invoices(_ context.Context, org string) ([]billing.InvoiceSummary, error) {
	f.lastOrg = org
	var out []billing.InvoiceSummary
	for inv, o := range f.invoiceOrg {
		if o == org {
			out = append(out, billing.InvoiceSummary{ID: inv, TotalCents: 500, Status: "pending", Plan: "cloud"})
		}
	}
	return out, f.err
}

func (f *fakeBilling) InvoiceLink(_ context.Context, org, inv string) (string, error) {
	f.lastOrg = org
	if f.err != nil {
		return "", f.err
	}
	if f.invoiceOrg[inv] != org {
		return "", billing.ErrInvoiceNotFound
	}
	return "https://invoice.stripe.com/i/" + inv, nil
}

func (f *fakeBilling) HandleStripeWebhook(_ context.Context, payload []byte, sig string) error {
	f.payload, f.signature = payload, sig
	return f.webhookErr
}

// fakeAuthz grants by role, as the role catalog does: the user id is the
// role.
type fakeAuthz struct{}

func (fakeAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	switch p.UserID {
	case security.RoleOwner:
		return nil
	case security.RoleAdmin:
		if perm == security.MemberManage || perm == security.MemberRead {
			return nil
		}
	case security.RoleDeveloper:
		if perm == security.MemberRead {
			return nil
		}
	}
	return security.ErrForbidden
}

// testGuard authenticates the way identity's does, as far as the routes
// can tell: X-User is a session's user, X-Key an API key made by that
// user, X-Org the session's workspace (org_1 when unset), and nothing at
// all is a 401.
var testGuard = identity.Guard{func(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		req := c.Request()
		org := req.Header.Get("X-Org")
		if org == "" {
			org = "org_1"
		}
		var p security.Principal
		switch {
		case req.Header.Get("X-Key") != "":
			p = security.Principal{OrgID: org, UserID: req.Header.Get("X-Key"), KeyID: "key_1"}
		case req.Header.Get("X-Agent") != "":
			p = security.Principal{OrgID: org, UserID: "agent:" + org, Kind: security.KindAgent,
				OnBehalfOf: &security.Principal{OrgID: org, UserID: req.Header.Get("X-Agent")}}
		case req.Header.Get("X-User") != "":
			p = security.Principal{OrgID: org, UserID: req.Header.Get("X-User")}
		default:
			return forge.NewHTTPError(http.StatusUnauthorized, "sign in")
		}
		c.WithContext(security.WithPrincipal(req.Context(), p))
		return next(c)
	}
}}

type caller map[string]string

func (c caller) String() string {
	for k, v := range c {
		return k + "=" + v
	}
	return "anonymous"
}

var (
	owner     = caller{"X-User": security.RoleOwner}
	admin     = caller{"X-User": security.RoleAdmin}
	developer = caller{"X-User": security.RoleDeveloper}
	ownersKey = caller{"X-Key": security.RoleOwner}
	ownersBot = caller{"X-Agent": security.RoleOwner}
	anonymous = caller{}
)

type rig struct {
	r   forge.Router
	svc *fakeBilling
}

func newRig(t *testing.T) *rig {
	t.Helper()
	rg := &rig{r: forge.NewRouter(), svc: &fakeBilling{}}
	if err := mountRoutes(rg.r, testGuard, rg.svc, fakeAuthz{}); err != nil {
		t.Fatal(err)
	}
	return rg
}

func (rg *rig) do(t *testing.T, method, path, body string, who caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range who {
		req.Header.Set(k, v)
	}
	out := httptest.NewRecorder()
	rg.r.ServeHTTP(out, req)
	return out
}

func TestAccount_MemberForbidden(t *testing.T) {
	rg := newRig(t)
	if got := rg.do(t, http.MethodGet, "/api/v1/billing/account", "", developer).Code; got != http.StatusForbidden {
		t.Fatalf("developer read the account: %d", got)
	}
	if got := rg.do(t, http.MethodGet, "/api/v1/billing/status", "", developer).Code; got != http.StatusOK {
		t.Fatalf("developer can't read the status the card gate needs: %d", got)
	}
}

func TestAccount_OwnerAndAdminRead(t *testing.T) {
	rg := newRig(t)
	for _, who := range []caller{owner, admin} {
		for _, path := range []string{"/api/v1/billing/account", "/api/v1/billing/invoices"} {
			if got := rg.do(t, http.MethodGet, path, "", who).Code; got != http.StatusOK {
				t.Errorf("%v %s: %d", who, path, got)
			}
		}
	}
	if got := rg.do(t, http.MethodGet, "/api/v1/billing/invoices", "", developer).Code; got != http.StatusForbidden {
		t.Errorf("developer read the invoices: %d", got)
	}
	if got := rg.do(t, http.MethodGet, "/api/v1/billing/status", "", anonymous).Code; got != http.StatusUnauthorized {
		t.Errorf("anonymous read the status: %d", got)
	}
}

func TestAccount_Shape(t *testing.T) {
	rg := newRig(t)
	res := rg.do(t, http.MethodGet, "/api/v1/billing/account", "", owner)
	if res.Code != http.StatusOK {
		t.Fatalf("account: %d %s", res.Code, res.Body)
	}
	var v map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"status", "ai_enabled", "ai_ends_at", "spending_limit_usd", "period", "usage",
		"credit_used_cents", "overage_cents", "projected_total_cents", "payment_methods"} {
		if _, ok := v[k]; !ok {
			t.Errorf("the account has no %s: %s", k, res.Body)
		}
	}
	if v["spending_limit_usd"] != "25.00" || v["ai_ends_at"] != nil {
		t.Errorf("limit %v ai_ends_at %v", v["spending_limit_usd"], v["ai_ends_at"])
	}
	pms, _ := v["payment_methods"].([]any)
	if len(pms) != 1 {
		t.Fatalf("payment methods %v", v["payment_methods"])
	}
	pm, _ := pms[0].(map[string]any)
	if pm["brand"] != "visa" || pm["last4"] != "4242" || pm["exp_month"] != float64(12) || pm["exp_year"] != float64(2030) || pm["default"] != true {
		t.Errorf("card %v", pm)
	}
	// Stripe's own ids never reach the dashboard.
	if strings.Contains(res.Body.String(), "pm_1") {
		t.Errorf("the account carries a Stripe id: %s", res.Body)
	}
}

func TestAccount_OtherOrgQueryIgnored(t *testing.T) {
	rg := newRig(t)
	_ = rg.do(t, http.MethodGet, "/api/v1/billing/account?org_id=org_other", "", owner)
	if rg.svc.lastOrg != "org_1" {
		t.Fatalf("read org %q, want the principal's", rg.svc.lastOrg)
	}
	_ = rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":"25.00","org_id":"org_other"}`, owner)
	if rg.svc.lastOrg != "org_1" {
		t.Fatalf("wrote org %q, want the principal's", rg.svc.lastOrg)
	}
}

func TestWritesAreOwnerSessionsOnly(t *testing.T) {
	rg := newRig(t)
	for _, c := range []struct {
		who  caller
		want int
	}{{owner, http.StatusOK}, {admin, http.StatusForbidden}, {developer, http.StatusForbidden},
		{ownersKey, http.StatusForbidden}, {ownersBot, http.StatusForbidden}, {anonymous, http.StatusUnauthorized}} {
		if got := rg.do(t, http.MethodPost, "/api/v1/billing/setup-session", "", c.who).Code; got != c.want {
			t.Errorf("%v: %d, want %d", c.who, got, c.want)
		}
	}
	if rg.svc.lastUser != security.RoleOwner || rg.svc.lastOrg != "org_1" {
		t.Fatalf("setup for %q in %q", rg.svc.lastUser, rg.svc.lastOrg)
	}
	for _, who := range []caller{admin, ownersKey, ownersBot} {
		for _, w := range []struct{ method, path, body string }{
			{http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":"25.00"}`},
			{http.MethodPut, "/api/v1/billing/ai", `{"enabled":true}`},
		} {
			if got := rg.do(t, w.method, w.path, w.body, who).Code; got != http.StatusForbidden {
				t.Errorf("%v %s: %d", who, w.path, got)
			}
		}
	}
	res := rg.do(t, http.MethodPost, "/api/v1/billing/setup-session", "", ownersKey)
	if !strings.Contains(res.Body.String(), "Billing changes need the owner signed in.") {
		t.Errorf("an API key read %s", res.Body)
	}
	if rg.svc.aiSet != nil {
		t.Fatalf("someone other than the owner turned the AI add-on to %v", *rg.svc.aiSet)
	}
	if got := rg.do(t, http.MethodPut, "/api/v1/billing/ai", `{"enabled":true}`, owner).Code; got != http.StatusOK {
		t.Errorf("the owner turning the AI add-on on: %d", got)
	}
}

func TestSetAI_Route(t *testing.T) {
	rg := newRig(t)
	for body, want := range map[string]int{
		`{"enabled":true}`: http.StatusOK, `{"enabled":false}`: http.StatusOK,
		`{}`: http.StatusBadRequest, `{"enabled":"yes"}`: http.StatusBadRequest, `not json`: http.StatusBadRequest,
	} {
		if got := rg.do(t, http.MethodPut, "/api/v1/billing/ai", body, owner).Code; got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	res := rg.do(t, http.MethodPut, "/api/v1/billing/ai", `{"enabled":false,"org_id":"org_other"}`, owner)
	var v map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if rg.svc.lastOrg != "org_1" || rg.svc.aiSet == nil || *rg.svc.aiSet {
		t.Fatalf("set %v for %q", rg.svc.aiSet, rg.svc.lastOrg)
	}
	if v["status"] != "active" || v["ai_enabled"] != true || v["ai_ends_at"] != "2026-12-01T00:00:00Z" {
		t.Fatalf("off: %s", res.Body)
	}

	// No card, or a failed payment unpaid: 409 with what to do.
	rg.svc.aiErr = fmt.Errorf("%w: org_1 is pending_payment", billing.ErrNeedsPayment)
	res = rg.do(t, http.MethodPut, "/api/v1/billing/ai", `{"enabled":true}`, owner)
	var body struct{ Error string }
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	if res.Code != http.StatusConflict || !strings.Contains(body.Error, "Add a card") || strings.Contains(body.Error, "pending_payment") {
		t.Fatalf("needs payment: %d %s", res.Code, res.Body)
	}
}

func TestSpendingLimit_Validation(t *testing.T) {
	rg := newRig(t)
	for body, want := range map[string]int{
		`{"limit_usd":"25.00"}`: http.StatusOK, `{"limit_usd":null}`: http.StatusOK,
		`{"limit_usd":"-1"}`: http.StatusUnprocessableEntity, `{"limit_usd":"1.234"}`: http.StatusUnprocessableEntity,
		`{"limit_usd":"abc"}`: http.StatusUnprocessableEntity, `{"limit_usd":"1000000.01"}`: http.StatusUnprocessableEntity,
		`{"limit_usd":"1000000.00"}`: http.StatusOK, `{"limit_usd":"0"}`: http.StatusOK, `{"limit_usd":"0.5"}`: http.StatusOK,
		`{"limit_usd":25}`: http.StatusUnprocessableEntity, `{}`: http.StatusUnprocessableEntity,
		`{"limit_usd":""}`: http.StatusUnprocessableEntity, `{"limit_usd":"1e3"}`: http.StatusUnprocessableEntity,
		`not json`: http.StatusBadRequest,
	} {
		if got := rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", body, owner).Code; got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	_ = rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":"25.00"}`, owner)
	if rg.svc.limit == nil || *rg.svc.limit != 2500 {
		t.Fatalf("stored limit %v, want 2500 cents", rg.svc.limit)
	}
	_ = rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":"0.5"}`, owner)
	if rg.svc.limit == nil || *rg.svc.limit != 50 {
		t.Fatalf("stored limit %v, want 50 cents", rg.svc.limit)
	}
	_ = rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":null}`, owner)
	if rg.svc.limit != nil {
		t.Fatalf("stored limit %v, want none", *rg.svc.limit)
	}
}

func TestSpendingLimit_SavedButNotCheckedIsAccepted(t *testing.T) {
	rg := newRig(t)
	rg.svc.limitErr = fmt.Errorf("%w: org_1: %w", billing.ErrLimitNotChecked, context.DeadlineExceeded)
	res := rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":"25.00"}`, owner)
	if res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), `"25.00"`) {
		t.Fatalf("saved, not checked: %d %s", res.Code, res.Body)
	}
	rg.svc.limitErr = context.DeadlineExceeded // waited behind an evaluation and gave up before saving
	if res = rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":"25.00"}`, owner); res.Code != http.StatusServiceUnavailable {
		t.Fatalf("timed out: %d %s", res.Code, res.Body)
	}
}

// slowLimit waits for the request's deadline, as SetSpendingLimit does
// behind a long evaluation lock.
type slowLimit struct {
	*fakeBilling
	deadline bool
}

func (s *slowLimit) SetSpendingLimit(ctx context.Context, _ string, _ *int64) (billing.Account, error) {
	_, s.deadline = ctx.Deadline()
	return billing.Account{}, context.DeadlineExceeded
}

func TestSpendingLimit_RunsWithADeadline(t *testing.T) {
	svc := &slowLimit{fakeBilling: &fakeBilling{}}
	r := forge.NewRouter()
	if err := mountRoutes(r, testGuard, svc, fakeAuthz{}); err != nil {
		t.Fatal(err)
	}
	rg := &rig{r: r, svc: svc.fakeBilling}
	if got := rg.do(t, http.MethodPut, "/api/v1/billing/spending-limit", `{"limit_usd":"25.00"}`, owner).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("status %d", got)
	}
	if !svc.deadline {
		t.Fatal("SetSpendingLimit ran with no deadline")
	}
}

func TestInvoiceLink_OnlyTheWorkspacesOwn(t *testing.T) {
	rg := newRig(t)
	rg.svc.invoiceOrg = map[string]string{"inv_mine": "org_1", "inv_theirs": "org_2"}
	if got := rg.do(t, http.MethodGet, "/api/v1/billing/invoices/inv_theirs/link", "", owner).Code; got != http.StatusNotFound {
		t.Fatalf("another workspace's invoice link: %d", got)
	}
	res := rg.do(t, http.MethodGet, "/api/v1/billing/invoices/inv_mine/link", "", admin)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "https://invoice.stripe.com/i/inv_mine") {
		t.Fatalf("own invoice link: %d %s", res.Code, res.Body)
	}
	if got := rg.do(t, http.MethodGet, "/api/v1/billing/invoices/inv_mine/link", "", developer).Code; got != http.StatusForbidden {
		t.Fatalf("developer read an invoice link: %d", got)
	}
	// org_2's owner gets its own invoice and not org_1's.
	if got := rg.do(t, http.MethodGet, "/api/v1/billing/invoices/inv_mine/link", "", caller{"X-User": security.RoleOwner, "X-Org": "org_2"}).Code; got != http.StatusNotFound {
		t.Fatalf("org_2 read org_1's invoice: %d", got)
	}
}

func TestErrorsNeverCarryInternalText(t *testing.T) {
	rg := newRig(t)
	rg.svc.err = errors.New("stripe: retrieve customer for tenant org_1: Invalid API Key provided: sk_live_****cret")
	for _, path := range []string{"/api/v1/billing/account", "/api/v1/billing/invoices", "/api/v1/billing/invoices/x/link"} {
		res := rg.do(t, http.MethodGet, path, "", owner)
		if res.Code != http.StatusInternalServerError || strings.Contains(res.Body.String(), "stripe") || strings.Contains(res.Body.String(), "sk_") {
			t.Errorf("%s: %d %s", path, res.Code, res.Body)
		}
	}
	res := rg.do(t, http.MethodPost, "/api/v1/billing/setup-session", "", owner)
	if res.Code != http.StatusInternalServerError || strings.Contains(res.Body.String(), "sk_") {
		t.Errorf("setup: %d %s", res.Code, res.Body)
	}
	// A gate refusal says its own sentence and nothing else.
	rg.svc.err = fmt.Errorf("billing: something: %w", &metering.Blocked{Reason: "Add a card in Settings > Billing."})
	res = rg.do(t, http.MethodGet, "/api/v1/billing/status", "", developer)
	var body struct{ Error string }
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	if res.Code != http.StatusPaymentRequired || body.Error != "Add a card in Settings > Billing." {
		t.Errorf("blocked: %d %s", res.Code, res.Body)
	}
}

func TestWebhook_Statuses(t *testing.T) {
	rg := newRig(t)
	body := `{"id":"evt_1"}`
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, http.StatusOK},
		{fmt.Errorf("%w: stripe: verify webhook: no valid signature", billing.ErrBadSignature), http.StatusBadRequest},
		{fmt.Errorf("%w: bad json", billing.ErrBadEvent), http.StatusBadRequest},
		// MarkCardReady or the database failed: Stripe must send it again.
		{errors.New("billing: save the card of org_1: connection refused"), http.StatusInternalServerError},
	} {
		rg.svc.webhookErr = c.err
		res := rg.do(t, http.MethodPost, "/api/v1/billing/webhooks/stripe", body, caller{"Stripe-Signature": "t=1,v1=abc"})
		if res.Code != c.want {
			t.Errorf("%v: %d, want %d", c.err, res.Code, c.want)
		}
		if strings.Contains(res.Body.String(), "connection refused") || strings.Contains(res.Body.String(), "no valid signature") {
			t.Errorf("%v: the body carries the error: %s", c.err, res.Body)
		}
	}
	if string(rg.svc.payload) != body || rg.svc.signature != "t=1,v1=abc" {
		t.Fatalf("handed %q with %q", rg.svc.payload, rg.svc.signature)
	}
}

func TestWebhook_ReadsAtMostOneMegabyte(t *testing.T) {
	rg := newRig(t)
	big := strings.Repeat("a", maxEvent+10)
	_ = rg.do(t, http.MethodPost, "/api/v1/billing/webhooks/stripe", big, anonymous)
	if len(rg.svc.payload) != maxEvent {
		t.Fatalf("read %d bytes", len(rg.svc.payload))
	}
}

func TestBillingRoutesStayOutOfOpenAPI(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Loomplane", Version: "1", SpecEnabled: true}))
	if err := mountRoutes(r, testGuard, &fakeBilling{}, fakeAuthz{}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/api/v1/billing") {
		t.Fatal("billing routes are in the OpenAPI spec")
	}
}

func TestDollars(t *testing.T) {
	for cents, want := range map[int64]string{0: "0.00", 5: "0.05", 50: "0.50", 2500: "25.00", 100_000_000: "1000000.00"} {
		if got := *dollars(&cents); got != want {
			t.Errorf("%d: %s, want %s", cents, got, want)
		}
	}
	if dollars(nil) != nil {
		t.Error("no limit isn't null")
	}
}
