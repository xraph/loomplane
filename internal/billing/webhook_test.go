//go:build cloud

package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	stripego "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/provider"
	"github.com/xraph/ledger/provider/stripe"
	"github.com/xraph/ledger/subscription"
	"github.com/xraph/ledger/types"

	"github.com/xraph/loomplane/internal/notifications"
)

const testWebhookSecret = "whsec_test"

func signed(t *testing.T, body string) (payload []byte, header string) {
	t.Helper()
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: []byte(body), Secret: testWebhookSecret, Timestamp: time.Now()})
	return sp.Payload, sp.Header
}

func event(typ, object string) string {
	return `{"id":"evt_1","object":"event","type":"` + typ + `","api_version":"` + stripego.APIVersion + `","created":1767225600,"data":{"object":` + object + `}}`
}

// webhookService is billing over a ledger whose payment provider is the
// real Stripe provider, built with testWebhookSecret. Nothing it does here
// calls Stripe: verifying and mapping an invoice event are local.
func webhookService(t *testing.T) *Service {
	t.Helper()
	db, pg := scratchDB(t)
	p, err := stripe.New(stripe.Config{SecretKey: "sk_test_never_called", WebhookSecret: testWebhookSecret, Customers: pgCustomers{pg: pg}})
	if err != nil {
		t.Fatal(err)
	}
	l := newLedgerWith(db, stripe.NewPlugin(p))
	if err = SeedCatalog(t.Context(), l, testAppID); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{DB: db, Ledger: l, AppID: testAppID, Payments: p, Suspender: &suspenderSpy{},
		Config: Config{StripeWebhookSecret: testWebhookSecret}})
	s.notify = func(context.Context, notifications.Notice) error { return nil }
	return s
}

// notices records what a Service sends.
type notices struct {
	mu   sync.Mutex
	sent []notifications.Notice
}

func (n *notices) record(_ context.Context, x notifications.Notice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, x)
	return nil
}

func (n *notices) kinds() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.sent))
	for _, x := range n.sent {
		out = append(out, x.Kind+" "+x.SubjectID)
	}
	return out
}

// seedWorkspace makes the authsome rows a workspace and its owner are:
// the app, its environment, the user and the organization.
func seedWorkspace(t *testing.T, pg *pgdriver.PgDB, org, user, email, name string) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO authsome_apps(id,name,slug) VALUES($1,'App','app') ON CONFLICT DO NOTHING`, []any{testAppID}},
		{`INSERT INTO authsome_environments(id,app_id,name,slug,type,is_default) VALUES('env',$1,'Production','production','production',true)
			ON CONFLICT DO NOTHING`, []any{testAppID}},
		{`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES($1,$2,'env',$3,'','')`, []any{user, testAppID, email}},
		{`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by) VALUES($1,$2,'env',$3,$1,$4)`, []any{org, testAppID, name, user}},
	} {
		if _, err := pg.Exec(t.Context(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

// webhookWorld is billingWorld with org_1 in authsome_organizations.
func webhookWorld(t *testing.T) (*Service, *pgdriver.PgDB) {
	t.Helper()
	clock := at("2026-11-04T10:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	seedWorkspace(t, pg, "org_1", "user_owner", "owner@example.com", "Acme")
	return s, pg
}

// invoiceWorld is billingWorld with org_1 carded in October and its
// October invoice made, finalized and sent to Stripe. The clock is the
// close, 01:15 on 1 November.
func invoiceWorld(t *testing.T, clock *time.Time) (s *Service, pg *pgdriver.PgDB, invID string) {
	t.Helper()
	*clock = at("2026-10-05T00:00:00Z")
	s, pg, _ = billingWorld(t, clock)
	seedWorkspace(t, pg, "org_1", "user_owner", "owner@example.com", "Acme")
	if err := s.MarkCardReady(t.Context(), "org_1"); err != nil {
		t.Fatal(err)
	}
	*clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(t.Context()); err != nil {
		t.Fatal(err)
	}
	var providerID string
	if err := pg.QueryRow(t.Context(), `SELECT invoice_id, provider_id FROM loomplane_billing_invoices WHERE org_id='org_1'`).
		Scan(&invID, &providerID); err != nil {
		t.Fatal(err)
	}
	if providerID == "" {
		t.Fatal("the October invoice wasn't sent")
	}
	return s, pg, invID
}

// webhookWorldWithInvoice is invoiceWorld with its own clock.
func webhookWorldWithInvoice(t *testing.T) (*Service, *pgdriver.PgDB, string) {
	t.Helper()
	var clock time.Time
	return invoiceWorld(t, &clock)
}

func TestWebhook_BadSignature(t *testing.T) {
	s := webhookService(t)
	payload, _ := signed(t, event("invoice.paid", `{}`))
	if err := s.HandleStripeWebhook(t.Context(), payload, "t=1,v1=bad"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("bad signature: %v", err)
	}
	if err := s.HandleStripeWebhook(t.Context(), payload, ""); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("no signature: %v", err)
	}
	// Signed with another secret.
	other := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_other", Timestamp: time.Now()})
	if err := s.HandleStripeWebhook(t.Context(), other.Payload, other.Header); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("another secret: %v", err)
	}
	// Signed an hour ago: a replay of a captured event.
	old := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: testWebhookSecret, Timestamp: time.Now().Add(-time.Hour)})
	if err := s.HandleStripeWebhook(t.Context(), old.Payload, old.Header); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("an old signature: %v", err)
	}
	// The body changed after it was signed.
	_, header := signed(t, event("invoice.paid", `{}`))
	if err := s.HandleStripeWebhook(t.Context(), []byte(event("invoice.paid", `{"id":"in_x"}`)), header); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a changed body: %v", err)
	}
}

func TestWebhook_SignedEventsAreMappedAndApplied(t *testing.T) {
	s := webhookService(t)
	ctx := t.Context()
	// An event billing doesn't act on, and an invoice ledger doesn't have.
	for _, body := range []string{
		event("customer.created", `{"id":"cus_1","object":"customer"}`),
		event("invoice.paid", `{"id":"in_1","object":"invoice"}`),
		event("invoice.paid", `{"id":"in_1","object":"invoice","metadata":{"ledger_invoice_id":"`+id.NewInvoiceID().String()+`"}}`),
		event("invoice.payment_failed", `{"id":"in_1","object":"invoice","metadata":{"ledger_invoice_id":"`+id.NewInvoiceID().String()+`"}}`),
	} {
		payload, header := signed(t, body)
		if err := s.HandleStripeWebhook(ctx, payload, header); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
	// A signed event Loomplane can't read is refused, not retried.
	payload, header := signed(t, event("invoice.paid", `{"id":5}`))
	if err := s.HandleStripeWebhook(ctx, payload, header); !errors.Is(err, ErrBadEvent) {
		t.Fatalf("an unreadable event: %v", err)
	}
	noData := `{"id":"evt_1","object":"event","type":"invoice.paid","api_version":"` + stripego.APIVersion + `","created":1767225600}`
	payload, header = signed(t, noData)
	if err := s.HandleStripeWebhook(ctx, payload, header); !errors.Is(err, ErrBadEvent) {
		t.Fatalf("an event with no data: %v", err)
	}
}

func TestWebhook_SignedPaidEventMarksTheInvoicePaid(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, pg, invID := invoiceWorld(t, &clock)
	// The same database and ledger, verified and mapped by the real Stripe
	// provider.
	p, err := stripe.New(stripe.Config{SecretKey: "sk_test_never_called", WebhookSecret: testWebhookSecret, Customers: pgCustomers{pg: pg}})
	if err != nil {
		t.Fatal(err)
	}
	live := New(Deps{DB: s.db, Ledger: newLedgerWith(s.db, stripe.NewPlugin(p)), AppID: testAppID, Payments: p,
		Suspender: &suspenderSpy{}, Now: func() time.Time { return clock }})
	live.notify = func(context.Context, notifications.Notice) error { return nil }
	payload, header := signed(t, event("invoice.paid", `{"id":"in_1","object":"invoice","metadata":{"ledger_invoice_id":"`+invID+`"}}`))
	for range 2 {
		if err = live.HandleStripeWebhook(t.Context(), payload, header); err != nil {
			t.Fatalf("paid: %v", err)
		}
	}
	var paidAt time.Time
	if err = pg.QueryRow(t.Context(), `SELECT paid_at FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&paidAt); err != nil {
		t.Fatal(err)
	}
	if !paidAt.Equal(time.Unix(1767225600, 0)) {
		t.Fatalf("paid_at %s, want the event's time", paidAt)
	}
	iid, _ := id.ParseInvoiceID(invID)
	inv, err := live.l.Store().GetInvoice(t.Context(), iid)
	if err != nil || inv.Status != invoice.StatusPaid || inv.PaymentRef != "in_1" {
		t.Fatalf("ledger invoice %+v %v", inv, err)
	}
}

func TestWebhook_UnknownTenantIs200(t *testing.T) {
	s, pg := webhookWorld(t)
	err := s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "setup_intent.succeeded", EntityType: "tenant", EntityID: "org_gone", Handled: true})
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	_ = pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_billing_accounts WHERE org_id='org_gone'`).Scan(&rows)
	if rows != 0 {
		t.Fatal("a deleted workspace got an account row")
	}
	subs, err := s.l.Store().ListSubscriptions(t.Context(), "org_gone", testAppID, subscription.ListOpts{})
	if err != nil || len(subs) != 0 {
		t.Fatalf("a deleted workspace got subscriptions %v %v", subs, err)
	}
	// No workspace named at all is nothing too.
	if err = s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "checkout.session.completed", EntityType: "tenant", Handled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestWebhook_SetupMarksCardReady(t *testing.T) {
	s, _ := webhookWorld(t) // seeds authsome_organizations with org_1
	res := &provider.WebhookResult{EventType: "checkout.session.completed", EntityType: "tenant", EntityID: "org_1", Handled: true}
	for range 2 { // a replay is a no-op
		if err := s.applyWebhook(t.Context(), res); err != nil {
			t.Fatal(err)
		}
	}
	if a, _ := s.Account(t.Context(), "org_1"); a.Status != StatusActive {
		t.Fatalf("status %s", a.Status)
	}
	subs, err := s.l.Store().ListSubscriptions(t.Context(), "org_1", testAppID, subscription.ListOpts{})
	if err != nil || len(subs) != 1 {
		t.Fatalf("subscriptions after a replayed card %d %v, want one", len(subs), err)
	}
}

// When MarkCardReady fails the webhook fails, so Stripe sends it again.
func TestWebhook_CardReadyFailureIsAnError(t *testing.T) {
	s, pg := webhookWorld(t)
	if _, err := pg.Exec(t.Context(), `UPDATE ledger_plans SET slug='gone' WHERE slug=$1`, PlanCloud); err != nil {
		t.Fatal(err)
	}
	res := &provider.WebhookResult{EventType: "setup_intent.succeeded", EntityType: "tenant", EntityID: "org_1", Handled: true}
	if err := s.applyWebhook(t.Context(), res); err == nil {
		t.Fatal("a card that couldn't be subscribed answered success")
	}
	// Stripe's next delivery, once ledger works, finishes it.
	if _, err := pg.Exec(t.Context(), `UPDATE ledger_plans SET slug=$1 WHERE slug='gone'`, PlanCloud); err != nil {
		t.Fatal(err)
	}
	if err := s.applyWebhook(t.Context(), res); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(t.Context(), "org_1"); a.Status != StatusActive || a.CloudSubscription == "" {
		t.Fatalf("after the redelivery: %+v", a)
	}
}

func TestWebhook_ReplayIdempotent(t *testing.T) {
	s, pg, invID := webhookWorldWithInvoice(t) // a finalized, synced October invoice for org_1
	paid := &provider.WebhookResult{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID, ProviderID: "in_1", Handled: true, Timestamp: at("2026-11-02T10:00:00Z")}
	for range 2 {
		if err := s.applyWebhook(t.Context(), paid); err != nil {
			t.Fatalf("replayed invoice.paid: %v", err)
		}
	}
	var paidAt time.Time
	_ = pg.QueryRow(t.Context(), `SELECT paid_at FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&paidAt)
	if !paidAt.Equal(at("2026-11-02T10:00:00Z")) {
		t.Fatalf("paid_at %s", paidAt)
	}
	// A later delivery of the same payment keeps the first time.
	later := *paid
	later.Timestamp = at("2026-11-05T10:00:00Z")
	if err := s.applyWebhook(t.Context(), &later); err != nil {
		t.Fatal(err)
	}
	_ = pg.QueryRow(t.Context(), `SELECT paid_at FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&paidAt)
	if !paidAt.Equal(at("2026-11-02T10:00:00Z")) {
		t.Fatalf("paid_at moved to %s", paidAt)
	}
	iid, _ := id.ParseInvoiceID(invID)
	inv, err := s.l.Store().GetInvoice(t.Context(), iid)
	if err != nil || inv.Status != invoice.StatusPaid || inv.PaidAt == nil || !inv.PaidAt.Equal(at("2026-11-02T10:00:00Z")) {
		t.Fatalf("ledger invoice %+v %v", inv, err)
	}
}

func TestWebhook_FailedAfterPaidIgnored(t *testing.T) {
	s, pg, invID := webhookWorldWithInvoice(t)
	sent := &notices{}
	s.notify = sent.record
	paid := &provider.WebhookResult{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID, ProviderID: "in_1", Handled: true, Timestamp: at("2026-11-02T10:00:00Z")}
	if err := s.applyWebhook(t.Context(), paid); err != nil {
		t.Fatal(err)
	}
	failed := &provider.WebhookResult{EventType: "invoice.payment_failed", EntityType: "invoice", EntityID: invID, Handled: true, Timestamp: at("2026-11-03T10:00:00Z")}
	if err := s.applyWebhook(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	var failedAt *time.Time
	_ = pg.QueryRow(t.Context(), `SELECT payment_failed_at FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&failedAt)
	if failedAt != nil {
		t.Fatal("a payment_failed after paid marked the invoice failed")
	}
	if k := sent.kinds(); len(k) != 0 {
		t.Fatalf("a failure after payment sent %v", k)
	}
}

func TestWebhook_FailedKeepsTheFirstTime(t *testing.T) {
	s, pg, invID := webhookWorldWithInvoice(t)
	sent := &notices{}
	s.notify = sent.record
	for _, ts := range []string{"2026-11-02T10:00:00Z", "2026-11-03T10:00:00Z"} {
		if err := s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "invoice.payment_failed", EntityType: "invoice", EntityID: invID, Handled: true, Timestamp: at(ts)}); err != nil {
			t.Fatal(err)
		}
	}
	var failedAt time.Time
	_ = pg.QueryRow(t.Context(), `SELECT payment_failed_at FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&failedAt)
	if !failedAt.Equal(at("2026-11-02T10:00:00Z")) {
		t.Fatalf("failed_at moved to %s", failedAt)
	}
	// Both deliveries name one notice, which the outbox makes once.
	want := NoticePaymentFailed + " payment_failed:" + invID
	if k := sent.kinds(); len(k) != 2 || k[0] != want || k[1] != want {
		t.Fatalf("notices %v, want %s for each delivery", k, want)
	}
	n := sent.sent[0]
	if n.OrgID != "org_1" || n.Title != "A payment for Loomplane Cloud failed" || n.Path != "/settings/billing" ||
		n.Body != "Stripe couldn't charge the card for this workspace's invoice. Update the card in Settings > Billing. If the invoice is still unpaid in 3 days, Loomplane Cloud work and AI on Loomplane's model keys stop." {
		t.Fatalf("notice %+v", n)
	}
}

func TestWebhook_UnknownInvoiceIs200(t *testing.T) {
	s, pg, invID := webhookWorldWithInvoice(t)
	sent := &notices{}
	s.notify = sent.record
	unknown := id.NewInvoiceID().String()
	for _, res := range []*provider.WebhookResult{
		{EventType: "invoice.paid", EntityType: "invoice", EntityID: unknown, ProviderID: "in_x", Handled: true, Timestamp: at("2026-11-02T10:00:00Z")},
		{EventType: "invoice.payment_failed", EntityType: "invoice", EntityID: unknown, Handled: true, Timestamp: at("2026-11-02T10:00:00Z")},
		{EventType: "invoice.paid", EntityType: "invoice", EntityID: "not-a-ledger-id", Handled: true},
		{EventType: "invoice.payment_failed", EntityType: "invoice", EntityID: "", Handled: true},
		// Not handled by ledger: nothing at all.
		{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID, Handled: false},
	} {
		if err := s.applyWebhook(t.Context(), res); err != nil {
			t.Errorf("%+v: %v", res, err)
		}
	}
	var changed int
	_ = pg.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_billing_invoices WHERE paid_at IS NOT NULL OR payment_failed_at IS NOT NULL`).Scan(&changed)
	if changed != 0 {
		t.Fatalf("%d invoices changed", changed)
	}
	if k := sent.kinds(); len(k) != 0 {
		t.Fatalf("notices %v", k)
	}
	if err := s.applyWebhook(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

// pastDueWorld is invoiceWorld with the payment failed on 2 November and
// the clock on 6 November, so org_1 is past due.
func pastDueWorld(t *testing.T, clock *time.Time) (*Service, *pgdriver.PgDB, string) {
	t.Helper()
	s, pg, invID := invoiceWorld(t, clock)
	if err := s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "invoice.payment_failed", EntityType: "invoice",
		EntityID: invID, Handled: true, Timestamp: at("2026-11-02T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	*clock = at("2026-11-06T10:00:00Z")
	if a, _ := s.Account(t.Context(), "org_1"); a.Status != StatusPastDue {
		t.Fatalf("status %s, want past_due", a.Status)
	}
	return s, pg, invID
}

func TestWebhook_PaidLiftsPastDue(t *testing.T) {
	var clock time.Time
	s, _, invID := pastDueWorld(t, &clock)
	spy := s.suspender.(*suspenderSpy)
	if err := s.Evaluate(t.Context(), "org_1"); err != nil {
		t.Fatal(err)
	}
	n := len(spy.since(0))
	if err := s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID,
		ProviderID: "in_1", Handled: true, Timestamp: clock}); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(t.Context(), "org_1"); a.Status != StatusActive {
		t.Fatalf("status %s after paying", a.Status)
	}
	calls := spy.since(n)
	restored := false
	for _, c := range calls {
		restored = restored || c == "restore past_due"
	}
	if !restored {
		t.Fatalf("paying didn't restore platform Cloud: %v", calls)
	}
}

func TestWebhook_PaidTellsAnAIOnlyWorkspaceAIIsBack(t *testing.T) {
	var clock time.Time
	s, pg, invID := pastDueWorld(t, &clock)
	s.suspender.(*suspenderSpy).noTenant = true
	if _, err := pg.Exec(t.Context(), `UPDATE loomplane_billing_accounts SET ai_subscription_id='asub_test' WHERE org_id='org_1'`); err != nil {
		t.Fatal(err)
	}
	sent := &notices{}
	s.notify = sent.record
	paid := &provider.WebhookResult{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID, ProviderID: "in_1", Handled: true, Timestamp: clock}
	for range 2 {
		if err := s.applyWebhook(t.Context(), paid); err != nil {
			t.Fatal(err)
		}
	}
	want := NoticeRestored + " paid:" + invID
	var got []string
	for _, k := range sent.kinds() {
		if k == want {
			got = append(got, k)
		}
	}
	// The second delivery finds the invoice paid by then and says nothing
	// new; the same subject would be made once by the outbox anyway.
	if len(got) == 0 {
		t.Fatalf("notices %v, want %s", sent.kinds(), want)
	}
	for _, x := range sent.sent {
		if x.Kind == NoticeRestored && (x.Title != "Payment received" || x.Body != "The unpaid invoice is paid. AI on Loomplane's model keys is back on.") {
			t.Fatalf("notice %+v", x)
		}
	}
}

func TestWebhook_PaidSaysNothingWhenPlatformCloudDoes(t *testing.T) {
	cases := map[string]func(t *testing.T, s *Service, pg *pgdriver.PgDB){
		// Platform Cloud tells a workspace with a tenant itself.
		"tenant": func(t *testing.T, _ *Service, pg *pgdriver.PgDB) {
			_, _ = pg.Exec(t.Context(), `UPDATE loomplane_billing_accounts SET ai_subscription_id='asub_test' WHERE org_id='org_1'`)
		},
		// No AI add-on: no AI to bring back.
		"no add-on": func(_ *testing.T, s *Service, _ *pgdriver.PgDB) {
			s.suspender.(*suspenderSpy).noTenant = true
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			var clock time.Time
			s, pg, invID := pastDueWorld(t, &clock)
			setup(t, s, pg)
			sent := &notices{}
			s.notify = sent.record
			if err := s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID,
				ProviderID: "in_1", Handled: true, Timestamp: clock}); err != nil {
				t.Fatal(err)
			}
			for _, x := range sent.sent {
				if x.Kind == NoticeRestored {
					t.Fatalf("sent %+v", x)
				}
			}
		})
	}
}

// A payment that clears before the 72 hours are up never made the
// workspace past due, so nothing is said to be back.
func TestWebhook_PaidBeforePastDueSaysNothing(t *testing.T) {
	var clock time.Time
	s, pg, invID := invoiceWorld(t, &clock)
	s.suspender.(*suspenderSpy).noTenant = true
	_, _ = pg.Exec(t.Context(), `UPDATE loomplane_billing_accounts SET ai_subscription_id='asub_test' WHERE org_id='org_1'`)
	sent := &notices{}
	s.notify = sent.record
	_ = s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "invoice.payment_failed", EntityType: "invoice", EntityID: invID,
		Handled: true, Timestamp: at("2026-11-02T10:00:00Z")})
	clock = at("2026-11-03T10:00:00Z")
	if err := s.applyWebhook(t.Context(), &provider.WebhookResult{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID,
		ProviderID: "in_1", Handled: true, Timestamp: clock}); err != nil {
		t.Fatal(err)
	}
	for _, x := range sent.sent {
		if x.Kind == NoticeRestored {
			t.Fatalf("sent %+v", x)
		}
	}
}

// fakePayments is billing's Payments without Stripe.
type fakePayments struct {
	mu                        sync.Mutex
	customerOrg, email, name  string
	setupOrg, success, cancel string
	methods                   []provider.PaymentMethod
	listErr                   error
}

func (f *fakePayments) EnsureCustomer(_ context.Context, org, email, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customerOrg, f.email, f.name = org, email, name
	return "cus_1", nil
}

func (f *fakePayments) CreateSetupSession(_ context.Context, org, success, cancel string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setupOrg, f.success, f.cancel = org, success, cancel
	return "https://checkout.stripe.com/c/pay/cs_test_1", nil
}

func (f *fakePayments) ListPaymentMethods(context.Context, string) ([]provider.PaymentMethod, error) {
	return f.methods, f.listErr
}

func (f *fakePayments) RefundInvoice(context.Context, string, *types.Money, string) (bool, error) {
	return false, errors.New("not here")
}

func (f *fakePayments) Verify([]byte, string) error { return errors.New("not here") }

func TestSetupSession_CustomerFirstThenTheCardPage(t *testing.T) {
	s, pg := webhookWorld(t)
	fp := &fakePayments{}
	s.payments = fp
	s.cfg.PublicURL = "https://app.loomplane.example"
	url, err := s.SetupSession(t.Context(), "org_1", "user_owner")
	if err != nil || url != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Fatalf("setup %q %v", url, err)
	}
	if fp.customerOrg != "org_1" || fp.email != "owner@example.com" || fp.name != "Acme" {
		t.Fatalf("customer %+v", fp)
	}
	if fp.setupOrg != "org_1" || fp.success != "https://app.loomplane.example/settings/billing?card=added" ||
		fp.cancel != "https://app.loomplane.example/settings/billing" {
		t.Fatalf("setup %+v", fp)
	}
	// Someone who isn't there, or another app's workspace, gets nothing.
	if _, err = s.SetupSession(t.Context(), "org_1", "user_nobody"); !errors.Is(err, ErrNoSuchMember) {
		t.Fatalf("an unknown user: %v", err)
	}
	if _, err = pg.Exec(t.Context(), `UPDATE authsome_users SET deleted_at=now() WHERE id='user_owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetupSession(t.Context(), "org_1", "user_owner"); !errors.Is(err, ErrNoSuchMember) {
		t.Fatalf("a deleted user: %v", err)
	}
}

func TestSummary_CreditOverageAndProjection(t *testing.T) {
	clock := at("2026-11-02T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	fp := &fakePayments{listErr: stripe.ErrNoCustomer}
	s.payments = fp
	sum, err := s.Summary(ctx, "org_1")
	if err != nil {
		t.Fatal(err)
	}
	// No card: nothing owed yet, and no Stripe customer is no cards.
	if sum.Account.Status != StatusPendingPayment || sum.ProjectedTotalCents != 0 || sum.PaymentMethods == nil || len(sum.PaymentMethods) != 0 {
		t.Fatalf("pending %+v", sum)
	}
	if len(sum.Usage) != len(Features) || sum.Usage[0].Feature != Features[0] {
		t.Fatalf("usage %+v", sum.Usage)
	}
	_ = s.MarkCardReady(ctx, "org_1")
	_, _ = s.SetRate(ctx, "compute_vcpu_hours", 10_000, "rex")
	_ = s.Record(ctx, compute("a", 800, at("2026-11-01T10:00:00Z"))) // $8.00
	_ = s.Record(ctx, compute("b", 900, at("2026-10-31T23:00:00Z"))) // October: not this month
	fp.listErr = nil
	fp.methods = []provider.PaymentMethod{{Brand: "visa", Last4: "4242", IsDefault: true}}
	sum, err = s.Summary(ctx, "org_1")
	if err != nil {
		t.Fatal(err)
	}
	if sum.BaseCreditUsedCents != 500 || sum.AICreditUsedCents != 0 || sum.OverageCents != 300 || sum.ProjectedTotalCents != 800 {
		t.Fatalf("credit %d + %d overage %d projected %d, want 500, 0, 300 and $5 + $3",
			sum.BaseCreditUsedCents, sum.AICreditUsedCents, sum.OverageCents, sum.ProjectedTotalCents)
	}
	if !sum.PeriodStart.Equal(at("2026-11-01T00:00:00Z")) || !sum.PeriodEnd.Equal(at("2026-12-01T00:00:00Z")) {
		t.Fatalf("period %s to %s", sum.PeriodStart, sum.PeriodEnd)
	}
	var vcpu int64
	for _, u := range sum.Usage {
		if u.Feature == "compute_vcpu_hours" {
			vcpu = u.Cents
		}
	}
	if vcpu != 800 || len(sum.PaymentMethods) != 1 {
		t.Fatalf("usage %+v cards %+v", sum.Usage, sum.PaymentMethods)
	}
	// With the add-on, AI's credit is its own: $12 of AI uses all $10 of
	// it and none of the plan's $5, which base usage used already.
	if _, err = s.SetAI(ctx, "org_1", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pg.Exec(ctx, `INSERT INTO loomplane_billing_usage (id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ('ai_1', 'org_1', 'ai_usd', 12, 1000000, 12000000, '2026-11-01T11:00:00Z', 'tenant_1:req_1')`); err != nil {
		t.Fatal(err)
	}
	if sum, err = s.Summary(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if sum.BaseCreditUsedCents != 500 || sum.AICreditUsedCents != 1000 || sum.OverageCents != 500 || sum.ProjectedTotalCents != 2000 {
		t.Fatalf("with AI: credit %d + %d overage %d projected %d, want 500, 1000, $3 + $2 and $5 + $10 + $5",
			sum.BaseCreditUsedCents, sum.AICreditUsedCents, sum.OverageCents, sum.ProjectedTotalCents)
	}
	// Stripe failing is an error, not an empty list.
	fp.listErr = errors.New("stripe: down")
	if _, err = s.Summary(ctx, "org_1"); err == nil {
		t.Fatal("a Stripe failure read as no cards")
	}
}

func TestInvoiceLink_OnlyTheWorkspacesOwnAndSent(t *testing.T) {
	s, pg, invID := webhookWorldWithInvoice(t)
	var asked []string
	s.hostedInvoiceURL = func(_ context.Context, stripeID string) (string, error) {
		asked = append(asked, stripeID)
		return "https://invoice.stripe.com/i/acct_1/test_1", nil
	}
	url, err := s.InvoiceLink(t.Context(), "org_1", invID)
	if err != nil || url != "https://invoice.stripe.com/i/acct_1/test_1" {
		t.Fatalf("own invoice %q %v", url, err)
	}
	if len(asked) != 1 || asked[0] != "in_"+invID {
		t.Fatalf("asked Stripe for %v", asked)
	}
	if _, err = s.InvoiceLink(t.Context(), "org_2", invID); !errors.Is(err, ErrInvoiceNotFound) {
		t.Fatalf("another workspace's invoice: %v", err)
	}
	if _, err = s.InvoiceLink(t.Context(), "org_1", id.NewInvoiceID().String()); !errors.Is(err, ErrInvoiceNotFound) {
		t.Fatalf("an unknown invoice: %v", err)
	}
	if len(asked) != 1 {
		t.Fatalf("Stripe was asked about an invoice that isn't the workspace's: %v", asked)
	}
	// Not sent to Stripe yet: no page.
	_, _ = pg.Exec(t.Context(), `UPDATE loomplane_billing_invoices SET provider_id='' WHERE invoice_id=$1`, invID)
	if _, err = s.InvoiceLink(t.Context(), "org_1", invID); !errors.Is(err, ErrInvoiceNotFound) {
		t.Fatalf("an unsent invoice: %v", err)
	}
	_, _ = pg.Exec(t.Context(), `UPDATE loomplane_billing_invoices SET provider_id='in_x' WHERE invoice_id=$1`, invID)
	s.hostedInvoiceURL = func(context.Context, string) (string, error) { return "", nil }
	if _, err = s.InvoiceLink(t.Context(), "org_1", invID); !errors.Is(err, ErrInvoiceNotFound) {
		t.Fatalf("Stripe has no page: %v", err)
	}
}

func TestInvoices_NewestFirstWithThePlan(t *testing.T) {
	var clock time.Time
	s, _, october := invoiceWorld(t, &clock)
	clock = at("2026-12-01T01:15:00Z")
	if err := s.CloseMonth(t.Context()); err != nil {
		t.Fatal(err)
	}
	invs, err := s.Invoices(t.Context(), "org_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 2 || invs[1].ID != october || !invs[0].PeriodStart.Equal(at("2026-11-01T00:00:00Z")) {
		t.Fatalf("invoices %+v", invs)
	}
	for _, inv := range invs {
		if inv.Plan != PlanCloud || inv.TotalCents != 500 || inv.Status != string(invoice.StatusPending) {
			t.Fatalf("invoice %+v", inv)
		}
	}
	if other, _ := s.Invoices(t.Context(), "org_2"); len(other) != 0 {
		t.Fatalf("org_2 sees %+v", other)
	}
}

// An endpoint on another Stripe API version is told apart from a bad
// signature, and names the version to set; one on stripe-go's release
// train passes.
func TestWebhook_AnotherAPIVersionIsNotABadSignature(t *testing.T) {
	s := webhookService(t)
	for _, version := range []string{"2025-03-31.basil", "2024-06-20", ""} {
		body := `{"id":"evt_1","object":"event","type":"invoice.paid","api_version":"` + version + `","created":1767225600,"data":{"object":{}}}`
		payload, header := signed(t, body)
		err := s.HandleStripeWebhook(t.Context(), payload, header)
		if !errors.Is(err, ErrAPIVersion) || errors.Is(err, ErrBadSignature) {
			t.Fatalf("%q: %v, want ErrAPIVersion alone", version, err)
		}
		if !strings.Contains(err.Error(), StripeAPIVersion) {
			t.Fatalf("%q: %v doesn't name %s", version, err, StripeAPIVersion)
		}
		// The same body with a bad signature is still a bad signature.
		if err = s.HandleStripeWebhook(t.Context(), payload, "t=1,v1=bad"); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("%q, bad signature: %v", version, err)
		}
	}
	_, train, _ := strings.Cut(StripeAPIVersion, ".")
	sameTrain := "2099-01-01." + train
	payload, header := signed(t, `{"id":"evt_1","object":"event","type":"customer.created","api_version":"`+sameTrain+`","created":1767225600,"data":{"object":{"id":"cus_1","object":"customer"}}}`)
	if err := s.HandleStripeWebhook(t.Context(), payload, header); errors.Is(err, ErrAPIVersion) || errors.Is(err, ErrBadSignature) {
		t.Fatalf("an event on %s: %v", sameTrain, err)
	}
}

// A paid event for an invoice whose sync stopped before it recorded the
// Stripe id records it, and never replaces one already recorded.
func TestWebhook_PaidRecordsAMissingProviderID(t *testing.T) {
	s, pg, invID := webhookWorldWithInvoice(t)
	var sent string
	_ = pg.QueryRow(t.Context(), `SELECT provider_id FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&sent)
	if _, err := pg.Exec(t.Context(), `UPDATE loomplane_billing_invoices SET provider_id = '' WHERE invoice_id=$1`, invID); err != nil {
		t.Fatal(err)
	}
	paid := &provider.WebhookResult{EventType: "invoice.paid", EntityType: "invoice", EntityID: invID, ProviderID: sent, Handled: true, Timestamp: at("2026-11-02T10:00:00Z")}
	if err := s.applyWebhook(t.Context(), paid); err != nil {
		t.Fatal(err)
	}
	var got string
	_ = pg.QueryRow(t.Context(), `SELECT provider_id FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&got)
	if got != sent {
		t.Fatalf("provider id %q, want %q", got, sent)
	}
	other := *paid
	other.ProviderID = "in_other"
	if err := s.applyWebhook(t.Context(), &other); err != nil {
		t.Fatal(err)
	}
	_ = pg.QueryRow(t.Context(), `SELECT provider_id FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&got)
	if got != sent {
		t.Fatalf("a later event replaced the provider id with %q", got)
	}
}

// The payment failed notice is the owner's only warning: one that can't be
// queued fails the event, so Stripe sends it again, and the failure is
// still recorded once.
func TestWebhook_FailedNoticeNotQueuedFailsTheEvent(t *testing.T) {
	s, pg, invID := webhookWorldWithInvoice(t)
	s.notify = func(context.Context, notifications.Notice) error { return errors.New("outbox down") }
	failed := &provider.WebhookResult{EventType: "invoice.payment_failed", EntityType: "invoice", EntityID: invID, Handled: true, Timestamp: at("2026-11-02T10:00:00Z")}
	if err := s.applyWebhook(t.Context(), failed); err == nil {
		t.Fatal("a notice that wasn't queued answered 200")
	}
	sent := &notices{}
	s.notify = sent.record
	retry := *failed
	retry.Timestamp = at("2026-11-02T11:00:00Z")
	if err := s.applyWebhook(t.Context(), &retry); err != nil {
		t.Fatal(err)
	}
	if k := sent.kinds(); len(k) != 1 || k[0] != NoticePaymentFailed+" payment_failed:"+invID {
		t.Fatalf("notices on the redelivery %v", k)
	}
	var failedAt time.Time
	_ = pg.QueryRow(t.Context(), `SELECT payment_failed_at FROM loomplane_billing_invoices WHERE invoice_id=$1`, invID).Scan(&failedAt)
	if !failedAt.Equal(at("2026-11-02T10:00:00Z")) {
		t.Fatalf("failed_at %s, want the first delivery's", failedAt)
	}
}
