//go:build cloud

package billing

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// The variables billing reads its config from. Nothing else in Loomplane
// reads them.
const (
	envStripeSecretKey     = "LOOMPLANE_BILLING_STRIPE_SECRET_KEY"
	envStripeWebhookSecret = "LOOMPLANE_BILLING_STRIPE_WEBHOOK_SECRET"
	envPublicURL           = "LOOMPLANE_BILLING_PUBLIC_URL"
)

// Config is billing's config. The two Stripe secrets are never logged and
// never put in an error.
type Config struct{ StripeSecretKey, StripeWebhookSecret, PublicURL string }

// String keeps the secrets out of anything that formats a Config.
func (c Config) String() string {
	return fmt.Sprintf("billing.Config{StripeSecretKey: %s, StripeWebhookSecret: %s, PublicURL: %q}",
		redacted(c.StripeSecretKey), redacted(c.StripeWebhookSecret), c.PublicURL)
}

// GoString is String, for %#v.
func (c Config) GoString() string { return c.String() }

// LogValue keeps the secrets out of slog, whatever the handler.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("StripeSecretKey", redacted(c.StripeSecretKey)),
		slog.String("StripeWebhookSecret", redacted(c.StripeWebhookSecret)),
		slog.String("PublicURL", c.PublicURL),
	)
}

// MarshalJSON keeps the secrets out of JSON.
func (c Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct{ StripeSecretKey, StripeWebhookSecret, PublicURL string }{
		redacted(c.StripeSecretKey), redacted(c.StripeWebhookSecret), c.PublicURL,
	})
}

func redacted(secret string) string {
	if secret == "" {
		return `""`
	}
	return "[redacted]"
}

// LoadConfig reads billing's env. The error names every missing variable.
// PublicURL loses any trailing slash, so paths join onto it cleanly.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		StripeSecretKey:     strings.TrimSpace(getenv(envStripeSecretKey)),
		StripeWebhookSecret: strings.TrimSpace(getenv(envStripeWebhookSecret)),
		PublicURL:           strings.TrimRight(strings.TrimSpace(getenv(envPublicURL)), "/"),
	}
	var missing []string
	for _, v := range []struct{ name, value string }{
		{envStripeSecretKey, cfg.StripeSecretKey},
		{envStripeWebhookSecret, cfg.StripeWebhookSecret},
		{envPublicURL, cfg.PublicURL},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("billing: set %s; the cloud edition doesn't start without them", strings.Join(missing, ", "))
	}
	return cfg, nil
}
