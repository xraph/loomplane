//go:build cloud

package billing

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadConfig_MissingNamesEach(t *testing.T) {
	_, err := LoadConfig(func(string) string { return "" })
	if err == nil {
		t.Fatal("empty env loaded")
	}
	for _, name := range []string{"LOOMPLANE_BILLING_STRIPE_SECRET_KEY", "LOOMPLANE_BILLING_STRIPE_WEBHOOK_SECRET", "LOOMPLANE_BILLING_PUBLIC_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error doesn't name %s: %v", name, err)
		}
	}
}

func TestLoadConfig_OK(t *testing.T) {
	env := map[string]string{
		"LOOMPLANE_BILLING_STRIPE_SECRET_KEY":     "sk_test_1",
		"LOOMPLANE_BILLING_STRIPE_WEBHOOK_SECRET": "whsec_1",
		"LOOMPLANE_BILLING_PUBLIC_URL":            "https://cloud.example/",
	}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil || cfg.StripeSecretKey != "sk_test_1" || cfg.PublicURL != "https://cloud.example" {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestLoadConfig_ErrorNeverCarriesAKey(t *testing.T) {
	_, err := LoadConfig(func(k string) string {
		if k == "LOOMPLANE_BILLING_STRIPE_SECRET_KEY" {
			return "sk_live_secret"
		}
		return ""
	})
	if err == nil || strings.Contains(err.Error(), "sk_live_secret") {
		t.Fatalf("error leaks the key or is nil: %v", err)
	}
}

func TestLoadConfig_BlankIsMissing(t *testing.T) {
	_, err := LoadConfig(func(k string) string {
		if k == "LOOMPLANE_BILLING_STRIPE_WEBHOOK_SECRET" {
			return "  "
		}
		return "set"
	})
	if err == nil || !strings.Contains(err.Error(), "LOOMPLANE_BILLING_STRIPE_WEBHOOK_SECRET") ||
		strings.Contains(err.Error(), "LOOMPLANE_BILLING_STRIPE_SECRET_KEY") {
		t.Fatalf("a blank secret: %v", err)
	}
}

func TestConfig_FormattingNeverCarriesASecret(t *testing.T) {
	cfg := Config{StripeSecretKey: "sk_live_secret", StripeWebhookSecret: "whsec_secret", PublicURL: "https://cloud.example"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, cfg)
		if strings.Contains(out, "sk_live_secret") || strings.Contains(out, "whsec_secret") {
			t.Errorf("%s prints a secret: %s", verb, out)
		}
		if !strings.Contains(out, "https://cloud.example") {
			t.Errorf("%s drops the public URL: %s", verb, out)
		}
	}
}
