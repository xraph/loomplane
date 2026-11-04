//go:build cloud

package identity

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/config"
)

func TestEditionOptions_RefusesWithoutBillingConfig(t *testing.T) {
	t.Setenv("LOOMPLANE_BILLING_STRIPE_SECRET_KEY", "")
	t.Setenv("LOOMPLANE_BILLING_STRIPE_WEBHOOK_SECRET", "")
	t.Setenv("LOOMPLANE_BILLING_PUBLIC_URL", "")
	e := &Extension{}
	if _, err := e.editionOptions(nil); err == nil || !strings.Contains(err.Error(), "LOOMPLANE_BILLING_STRIPE_SECRET_KEY") {
		t.Fatalf("editionOptions without config: %v", err)
	}
}

// Billing's delete hook comes before authsome's subscription plugin, so a
// deleted workspace is invoiced before the plugin cancels its
// subscriptions: authsome runs AfterOrgDelete hooks in registration order.
func TestEditionOptions_HookOrderInvoicesBeforeTheCancel(t *testing.T) {
	t.Setenv("LOOMPLANE_BILLING_STRIPE_SECRET_KEY", "sk_test_x")
	t.Setenv("LOOMPLANE_BILLING_STRIPE_WEBHOOK_SECRET", "whsec_x")
	t.Setenv("LOOMPLANE_BILLING_PUBLIC_URL", "https://cloud.example")
	db, err := grove.Open(pgdriver.New()) // never connected: nothing here queries
	if err != nil {
		t.Fatal(err)
	}
	e := &Extension{cfg: config.Config{Identity: config.IdentityConfig{AppID: "aapp_test"}}}
	opts, err := e.editionOptions(db)
	if err != nil {
		t.Fatal(err)
	}
	plugins := e.editionPlugins(db, e.edition.ledger)
	if len(opts) != 1+len(plugins) {
		t.Fatalf("%d options for %d plugins and the ledger", len(opts), len(plugins))
	}
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, p.Name())
	}
	hook, sub := slices.Index(names, "loomplane-billing-org-delete"), slices.Index(names, "subscription")
	if hook < 0 || sub < 0 || hook > sub {
		t.Fatalf("plugin order %v: billing's delete hook must come before the subscription plugin", names)
	}
}
