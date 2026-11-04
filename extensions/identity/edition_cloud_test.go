//go:build cloud

package identity

import (
	"strings"
	"testing"
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
