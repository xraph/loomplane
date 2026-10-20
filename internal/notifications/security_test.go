package notifications

import (
	"context"
	"strings"
	"testing"

	heraldid "github.com/xraph/herald/id"
	"github.com/xraph/herald/message"
	heraldmem "github.com/xraph/herald/store/memory"
	"github.com/xraph/relay/endpoint"
	relayid "github.com/xraph/relay/id"
	relaymem "github.com/xraph/relay/store/memory"
)

func TestWebhookDestinationsRejectLocalCredentialsAndUnsafeSchemes(t *testing.T) {
	for _, raw := range []string{"http://example.com/hook", "https://name:secret@example.com/hook", "https://127.0.0.1/hook", "https://[::1]/", "https://169.254.169.254/latest", "https://10.1.2.3/", "https://100.64.0.1/", "https://192.88.99.1/", "https://[::192.168.1.1]/", "https://[2001:2::1]/", "https://localhost/", "https://example.com/#token"} {
		if err := validateWebhookURL(raw); err == nil {
			t.Errorf("accepted unsafe destination %s", raw)
		}
	}
	if err := validateWebhookURL("https://hooks.example.com/loomplane"); err != nil {
		t.Fatal(err)
	}
}

func TestSigningSecretIsEncryptedInStoreAndReturnedOnlyToSigner(t *testing.T) {
	ctx := context.Background()
	underlying := relaymem.New()
	wrapped, err := newRelayStore(underlying, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	ep := &endpoint.Endpoint{ID: relayid.NewEndpointID(), TenantID: "org-a", URL: "https://example.com/hooks", Secret: "signing-secret", Enabled: true, EventTypes: []string{"deployment.*"}}
	if err = wrapped.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	raw, err := underlying.GetEndpoint(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Secret == ep.Secret || strings.Contains(raw.Secret, "signing-secret") {
		t.Fatal("plaintext signing secret retained")
	}
	got, err := wrapped.GetEndpoint(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "signing-secret" {
		t.Fatal("signer could not load original secret")
	}
	got.Secret = "rotated-secret"
	if err = wrapped.UpdateEndpoint(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = wrapped.GetEndpoint(ctx, ep.ID)
	if err != nil || got.Secret != "rotated-secret" {
		t.Fatalf("rotation: %v", err)
	}
	raw, err = underlying.GetEndpoint(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw.Secret = "plaintext-downgrade"
	if err = underlying.UpdateEndpoint(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = wrapped.GetEndpoint(ctx, ep.ID); err == nil {
		t.Fatal("plaintext credential downgrade accepted")
	}
}

func TestMailHistoryNeverRetainsSecurityContentOrProviderErrors(t *testing.T) {
	ctx := context.Background()
	raw := heraldmem.New()
	wrapped := privateHeraldStore{Store: raw}
	msg := &message.Message{ID: heraldid.NewMessageID(), AppID: "app", TemplateID: "auth.password-reset", Recipient: "private@example.com", Subject: "reset private@example.com", Body: "https://app/reset?token=live-token", Metadata: map[string]string{"token": "live-token"}}
	if err := wrapped.CreateMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if msg.Body == "" {
		t.Fatal("scrubbing changed content still needed by sender")
	}
	if err := wrapped.RecordDelivery(ctx, msg.ID, message.Delivery{Status: message.StatusFailed, Error: "live-token private@example.com rejected", ProviderMessageID: "contains-live-token"}); err != nil {
		t.Fatal(err)
	}
	got, err := raw.GetMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "" || got.Subject != "" || got.Recipient != "" || len(got.Metadata) != 0 || strings.Contains(got.Error, "live-token") || got.ProviderMessageID != "" {
		t.Fatalf("security content persisted: %#v", got)
	}
	if got.Status != message.StatusFailed {
		t.Fatal("delivery diagnostics lost status")
	}
}
