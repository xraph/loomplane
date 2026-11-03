package repositories_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

func TestSetWebhookSecret_rejectsBadPastedToken(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	std := func(n int) string { return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, n)) }
	for name, tok := range map[string]string{
		"no prefix":       base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"the secret form": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"prefix only":     "whsec_",
		"31 bytes":        std(31),
		"33 bytes":        std(33),
		"not base64":      "whsec_!!!not base64!!!",
		"unpadded":        "whsec_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"url alphabet":    "whsec_" + base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)),
		"another prefix":  "whsek_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
	} {
		_, err := f.svc.SetWebhookSecret(context.Background(), p, v.ID, repositories.SetGitLabWebhookInput{SigningToken: tok})
		if !errors.Is(err, repositories.ErrGitLabSigningToken) {
			t.Fatalf("%s: %v, want ErrGitLabSigningToken", name, err)
		}
		if len(tok) > len("whsec_") && strings.Contains(err.Error(), tok) {
			t.Fatalf("%s: the error quotes the token: %v", name, err)
		}
	}
	// Nothing was kept, and the connection has no webhook.
	if d := f.secrets.dump(); strings.Contains(d, "/webhook") {
		t.Fatalf("a rejected token was stored: %s", d)
	}
	if f.store.connection(t, v.ID).WebhookSet {
		t.Fatal("a rejected token set the webhook")
	}
}

func TestSetWebhookSecret_generatesTwoFormsOfOneSecret(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	got, err := f.svc.SetWebhookSecret(context.Background(), p, v.ID, repositories.SetGitLabWebhookInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://hooks.example/api/v1/gitlab/webhook/"+v.ID {
		t.Fatalf("url %q", got.URL)
	}
	raw, ok := strings.CutPrefix(got.SigningToken, "whsec_")
	if !ok {
		t.Fatalf("signing token %q", got.SigningToken)
	}
	bytes32, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(bytes32) != 32 {
		t.Fatalf("signing token decodes to %d bytes, %v", len(bytes32), err)
	}
	if got.SecretToken != base64.RawURLEncoding.EncodeToString(bytes32) {
		t.Fatalf("secret token %q isn't the signing token's bytes", got.SecretToken)
	}
	// Vault holds the raw bytes, under the connection's webhook key.
	stored, err := f.secrets.Reveal(context.Background(), "org-a", v.ID+"/webhook")
	if err != nil || !bytes.Equal(stored, bytes32) {
		t.Fatalf("vault holds %x, %v", stored, err)
	}
	if !f.store.connection(t, v.ID).WebhookSet {
		t.Fatal("webhook_set is still false")
	}
	// The connection is what the webhook route reads.
	org, host, secret, err := f.svc.WebhookTarget(context.Background(), v.ID)
	if err != nil || org != "org-a" || host != gcHost || !bytes.Equal(secret, bytes32) {
		t.Fatalf("target %q %q %x, %v", org, host, secret, err)
	}

	// The audit names the connection and holds no secret.
	last := len(f.audit.actions) - 1
	if f.audit.actions[last] != "gitlab.webhook.secret" || f.audit.details[last]["connection_id"] != v.ID {
		t.Fatalf("audit = %v %v", f.audit.actions, f.audit.details)
	}
	auditJSON, _ := json.Marshal(f.audit.details[last])
	for _, leak := range []string{got.SigningToken, got.SecretToken, raw} {
		if strings.Contains(string(auditJSON), leak) {
			t.Fatalf("audit holds the secret: %s", auditJSON)
		}
	}

	// Status never shows it again, only that one is set.
	st, err := f.svc.Status(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, _ := json.Marshal(st)
	if !st.Connections[0].WebhookSet || strings.Contains(string(statusJSON), got.SecretToken) || strings.Contains(string(statusJSON), raw) {
		t.Fatalf("status %s", statusJSON)
	}

	// Rotating replaces it.
	again, err := f.svc.SetWebhookSecret(context.Background(), p, v.ID, repositories.SetGitLabWebhookInput{})
	if err != nil || again.SigningToken == got.SigningToken || again.SecretToken == got.SecretToken {
		t.Fatalf("rotated %+v, %v", again, err)
	}
	_, _, secret, err = f.svc.WebhookTarget(context.Background(), v.ID)
	if err != nil || base64.RawURLEncoding.EncodeToString(secret) != again.SecretToken {
		t.Fatalf("target after rotating %x, %v", secret, err)
	}
}

func TestSetWebhookSecret_keepsAPastedToken(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	want := bytes.Repeat([]byte{0xfb, 0xff, 0x01, 0x02}, 8)
	tok := "whsec_" + base64.StdEncoding.EncodeToString(want)
	got, err := f.svc.SetWebhookSecret(context.Background(), p, v.ID, repositories.SetGitLabWebhookInput{SigningToken: tok})
	if err != nil {
		t.Fatal(err)
	}
	if got.SigningToken != tok || got.SecretToken != base64.RawURLEncoding.EncodeToString(want) {
		t.Fatalf("response %+v", got)
	}
	if _, _, secret, err := f.svc.WebhookTarget(context.Background(), v.ID); err != nil || !bytes.Equal(secret, want) {
		t.Fatalf("target secret %x, %v", secret, err)
	}
}

func TestSetWebhookSecret_needsPermissionAndAnOwnConnection(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")

	// Another organization can't reach it, and the answer is a missing one.
	_, err := f.svc.SetWebhookSecret(context.Background(), gcPrincipal("org-b", "user-2"), v.ID, repositories.SetGitLabWebhookInput{})
	if !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("org-b: %v, want ErrGitLabConnectionNotFound", err)
	}
	if _, err = f.svc.SetWebhookSecret(context.Background(), p, "glc_none", repositories.SetGitLabWebhookInput{}); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("unknown connection: %v", err)
	}
	// Reading isn't enough to set a secret.
	svc := repositories.NewGitLabConnections(repositories.GitLabConnectionsConfig{
		Store: f.store, Secrets: f.secrets, Authz: readOnly, Audit: f.audit, Registry: &repositories.GitLabRegistry{},
	})
	if _, err = svc.SetWebhookSecret(context.Background(), p, v.ID, repositories.SetGitLabWebhookInput{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a reader: %v, want ErrForbidden", err)
	}
	if d := f.secrets.dump(); strings.Contains(d, "/webhook") {
		t.Fatalf("a refused request stored a secret: %s", d)
	}
	if f.store.connection(t, v.ID).WebhookSet {
		t.Fatal("a refused request set the webhook")
	}
}

func TestWebhookTarget_missingConnectionOrSecret(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	ctx := context.Background()
	if _, _, _, err := f.svc.WebhookTarget(ctx, "glc_none"); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("unknown connection: %v", err)
	}
	// Connected, but no secret created yet.
	if _, _, _, err := f.svc.WebhookTarget(ctx, v.ID); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("no secret yet: %v", err)
	}
	if _, err := f.svc.SetWebhookSecret(ctx, p, v.ID, repositories.SetGitLabWebhookInput{}); err != nil {
		t.Fatal(err)
	}
	// Disconnected, the secret goes with it.
	if err := f.svc.Disconnect(ctx, p, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.WebhookTarget(ctx, v.ID); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("after disconnecting: %v", err)
	}
}
