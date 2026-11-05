//go:build cloud

package billing

import (
	"strings"
	"testing"

	"github.com/xraph/ledger/plan"
	"github.com/xraph/ledger/types"
)

func TestNewLedger_RoundTripsAPlanAndACustomer(t *testing.T) {
	db, pg := scratchDB(t)
	l, p, err := NewLedger(db, Config{StripeSecretKey: "sk_test_x", StripeWebhookSecret: "whsec_x", PublicURL: "https://c"})
	if err != nil || p == nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	pl := &plan.Plan{Name: "Probe", Slug: "probe", Currency: "usd", Status: plan.StatusActive, AppID: testAppID,
		Pricing: &plan.Pricing{BaseAmount: types.USD(100), BillingPeriod: plan.PeriodMonthly}}
	if err = l.CreatePlan(ctx, pl); err != nil {
		t.Fatal(err)
	}
	got, err := l.GetPlanBySlug(ctx, "probe", testAppID)
	if err != nil || got.Pricing.BaseAmount.Amount != 100 {
		t.Fatalf("%+v %v", got, err)
	}
	c := pgCustomers{pg: pg}
	id, err := c.CustomerID(ctx, "org_1")
	if err != nil || id != "" {
		t.Fatalf("a tenant with no row has customer %q, %v", id, err)
	}
	if err = c.SetCustomerID(ctx, "org_1", "cus_1"); err != nil {
		t.Fatal(err)
	}
	if id, _ := c.CustomerID(ctx, "org_1"); id != "cus_1" {
		t.Fatalf("customer %q", id)
	}
	if l.LifecycleInterval() != 0 {
		t.Fatal("the lifecycle worker is on")
	}
}

func TestSetCustomerID_KeepsTheRestOfTheAccount(t *testing.T) {
	_, pg := scratchDB(t)
	ctx := t.Context()
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_accounts (org_id, card_ready, spending_limit_cents) VALUES ('org_2', true, 2500)`); err != nil {
		t.Fatal(err)
	}
	c := pgCustomers{pg: pg}
	if err := c.SetCustomerID(ctx, "org_2", "cus_2"); err != nil {
		t.Fatal(err)
	}
	var (
		customer string
		ready    bool
		limit    int64
	)
	if err := pg.QueryRow(ctx, `SELECT stripe_customer_id, card_ready, spending_limit_cents FROM loomplane_billing_accounts WHERE org_id='org_2'`).
		Scan(&customer, &ready, &limit); err != nil {
		t.Fatal(err)
	}
	if customer != "cus_2" || !ready || limit != 2500 {
		t.Fatalf("account after SetCustomerID: %q %v %d", customer, ready, limit)
	}
}

func TestNewLedger_RefusesWithoutAStripeSecret(t *testing.T) {
	db, _ := scratchDB(t)
	_, _, err := NewLedger(db, Config{StripeWebhookSecret: "whsec_x", PublicURL: "https://c"})
	if err == nil || strings.Contains(err.Error(), "whsec_x") {
		t.Fatalf("NewLedger without a secret key: %v", err)
	}
}

func TestNewLedger_RefusesWithoutADatabase(t *testing.T) {
	if _, _, err := NewLedger(nil, Config{StripeSecretKey: "sk_test_x", StripeWebhookSecret: "whsec_x", PublicURL: "https://c"}); err == nil {
		t.Fatal("NewLedger without a database")
	}
}
