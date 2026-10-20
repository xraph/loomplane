//go:build integration && acceptance

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// Mailpit is optional for hosted CI. Ordinary integration tests exercise the
// same delivery flow against a rejecting and accepting SMTP fixture.
func TestAcceptanceMailpitInvitationsAndRecovery(t *testing.T) {
	endpoint := os.Getenv("LOOMPLANE_ACCEPTANCE_MAILPIT_URL")
	if endpoint == "" {
		t.Skip("set LOOMPLANE_ACCEPTANCE_MAILPIT_URL and SMTP_HOST/SMTP_PORT for a local Mailpit run")
	}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.PublicURL = "https://console.loomplane.test"
	cfg.Notifications.SMTPHost = os.Getenv("LOOMPLANE_ACCEPTANCE_SMTP_HOST")
	port, err := strconv.Atoi(os.Getenv("LOOMPLANE_ACCEPTANCE_SMTP_PORT"))
	if err != nil {
		t.Fatal("SMTP_PORT must be a port number")
	}
	cfg.Notifications.SMTPPort = port
	cfg.Notifications.From = "accounts@loomplane.test"
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	recipient := fmt.Sprintf("mailpit-%d@example.com", time.Now().UnixNano())
	var invitation struct {
		EmailStatus string `json:"email_status"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": recipient, "role": "viewer"}, &invitation)
	if invitation.EmailStatus != "sent" {
		t.Fatalf("invitation outcome: %s", invitation.EmailStatus)
	}
	invite := mailpitText(t, endpoint, recipient)
	link := recoveryLink(t, invite, "/invitations/accept")
	if link.Host != "console.loomplane.test" || link.Query().Get("token") == "" {
		t.Fatal("Mailpit invitation has no usable trusted link")
	}
	account := fmt.Sprintf("recovery-%d@example.com", time.Now().UnixNano())
	c.token = ""
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": account, "password": "Correct-horse-battery-9"}, nil)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/forgot-password", map[string]string{"email": account}, nil)
	reset := recoveryLink(t, mailpitText(t, endpoint, account), "/reset-password")
	if reset.Host != "console.loomplane.test" || reset.Query().Get("token") == "" {
		t.Fatal("Mailpit reset has no usable trusted link")
	}
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/reset-password", map[string]string{"token": reset.Query().Get("token"), "new_password": "Mailpit-correct-battery-42"}, nil)
	t.Log("Mailpit captured invitation and reset email; trusted reset link redeemed successfully")
}

func mailpitText(t *testing.T, base, recipient string) string {
	t.Helper()
	httpClient := &http.Client{Timeout: 5 * time.Second}
	get := func(path string, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, strings.TrimRight(base, "/")+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal("Mailpit unavailable")
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("Mailpit status %d", res.StatusCode)
		}
		if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var list struct {
			Messages []struct {
				ID string
				To []struct{ Address string }
			}
		}
		get("/api/v1/messages", &list)
		for _, msg := range list.Messages {
			for _, to := range msg.To {
				if to.Address == recipient {
					var body struct{ Text, HTML string }
					get("/api/v1/message/"+url.PathEscape(msg.ID), &body)
					return body.Text + "\n" + body.HTML
				}
			}
		}
		select {
		case <-t.Context().Done():
			t.Fatal(context.Cause(t.Context()))
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("Mailpit did not capture the expected recipient")
	return ""
}
