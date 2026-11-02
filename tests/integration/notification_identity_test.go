//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"net/http"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
)

// Removing the disabled outcome would make admins believe an invitation was
// delivered even when this installation has no email provider.
func TestNotifications_invitationWithoutMailKeepsOneUseManualLink(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "owner@example.com", "Correct-horse-battery-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Delivery", "slug": "delivery"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var invitation struct {
		AcceptPath  string `json:"accept_path"`
		EmailStatus string `json:"email_status"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "invitee@example.com", "role": "viewer"}, &invitation)
	if invitation.EmailStatus != "disabled" {
		t.Fatalf("email status %q, want disabled", invitation.EmailStatus)
	}
	link, err := url.Parse(invitation.AcceptPath)
	if err != nil || link.Path != "/invitations/accept" || link.Query().Get("token") == "" {
		t.Fatal("missing manual invitation link")
	}
}

// Missing notification registration would leave a reset token in the database
// without delivering it. Accepting Host here would send a credential to an attacker.
func TestNotifications_recoveryDeliversTrustedLinkAndRevokesSessions(t *testing.T) {
	mail := recoverySMTP(t)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.PublicURL = "https://console.loomplane.test"
	cfg.Notifications.SMTPHost = "127.0.0.1"
	cfg.Notifications.SMTPPort = mail.port
	cfg.Notifications.From = "accounts@example.com"
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "recover@example.com", "Correct-horse-battery-9")
	_ = mail.next(t) // Authsome also requests email verification on signup.
	oldToken := c.token
	c.token = ""
	var known, unknown struct {
		Status string `json:"status"`
	}
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/forgot-password", map[string]string{"email": "nobody@example.com"}, &unknown)
	select {
	case <-mail.messages:
		t.Fatal("unknown account received email")
	default:
	}
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/forgot-password", map[string]string{"email": "recover@example.com"}, &known)
	if known.Status != unknown.Status || known.Status != "ok" {
		t.Fatal("reset reveals account existence")
	}
	raw := mail.next(t)
	reset := recoveryLink(t, raw, "/reset-password")
	if reset.Scheme != "https" || reset.Host != "console.loomplane.test" {
		t.Fatal("reset email used an untrusted origin")
	}
	token := reset.Query().Get("token")
	if len(token) < 32 {
		t.Fatal("reset link has no high entropy credential")
	}
	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	assertRecoveryMailRedacted(t, conn, token, "recover@example.com")
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/reset-password", map[string]string{"token": token, "new_password": "Fresh-correct-battery-42"}, nil)
	c.must(http.StatusUnauthorized, http.MethodPost, "/authsome/v1/reset-password", map[string]string{"token": token, "new_password": "Other-correct-battery-43"}, nil)
	c.token = oldToken
	c.must(http.StatusUnauthorized, http.MethodGet, "/authsome/v1/me", nil, nil)
	c.token = ""
	c.must(http.StatusUnauthorized, http.MethodPost, "/authsome/v1/signin", map[string]string{"email": "recover@example.com", "password": "Correct-horse-battery-9"}, nil)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/signin", map[string]string{"email": "recover@example.com", "password": "Fresh-correct-battery-42"}, nil)
	_ = mail.next(t) // Password-change confirmation.
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/forgot-password", map[string]string{"email": "recover@example.com"}, nil)
	expired := recoveryLink(t, mail.next(t), "/reset-password").Query().Get("token")
	if _, err = conn.Exec(t.Context(), "UPDATE authsome_password_resets SET expires_at = now() - interval '1 minute' WHERE NOT consumed"); err != nil {
		t.Fatal(err)
	}
	c.must(http.StatusUnauthorized, http.MethodPost, "/authsome/v1/reset-password", map[string]string{"token": expired, "new_password": "Another-correct-battery-44"}, nil)
	assertRecoveryMailRedacted(t, conn, expired, "recover@example.com")
}

// The OTP form must work without a session, since verified-email signup
// intentionally withholds a session until the code has been redeemed.
func TestNotifications_emailVerificationDeliversUsableCode(t *testing.T) {
	mail := recoverySMTP(t)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.PublicURL = "https://console.loomplane.test"
	cfg.Identity.RequireEmailVerification = true
	cfg.Notifications.SMTPHost = "127.0.0.1"
	cfg.Notifications.SMTPPort = mail.port
	cfg.Notifications.From = "accounts@example.com"
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "verify@example.com", "Correct-horse-battery-9")
	c.token = ""
	raw := mail.next(t)
	link := recoveryLink(t, raw, "/verify-email")
	if link.Host != "console.loomplane.test" || link.Query().Get("email") != "verify@example.com" {
		t.Fatal("verification email has no trusted account link")
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(raw)
	if code == "" {
		t.Fatal("verification email has no code")
	}
	c.must(http.StatusUnauthorized, http.MethodPost, "/authsome/v1/verify-email", map[string]string{"email": "other@example.com", "code": code}, nil)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/verify-email", map[string]string{"email": "verify@example.com", "code": code}, nil)
	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	assertRecoveryMailRedacted(t, conn, code, "verify@example.com")
}

type recoveryMailbox struct {
	port     int
	messages chan string
	close    func() error
}

func recoverySMTP(t *testing.T) *recoveryMailbox {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	box := &recoveryMailbox{port: listener.Addr().(*net.TCPAddr).Port, messages: make(chan string, 16), close: listener.Close}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := textproto.NewReader(bufio.NewReader(conn))
				_, _ = io.WriteString(conn, "220 capture ESMTP\r\n")
				for {
					line, err := reader.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						_, _ = io.WriteString(conn, "250 capture\r\n")
					case line == "DATA":
						_, _ = io.WriteString(conn, "354 End with dot\r\n")
						body, err := reader.ReadDotBytes()
						if err != nil {
							return
						}
						box.messages <- string(body)
						_, _ = io.WriteString(conn, "250 accepted\r\n")
					case line == "QUIT":
						_, _ = io.WriteString(conn, "221 bye\r\n")
						return
					default:
						_, _ = io.WriteString(conn, "250 ok\r\n")
					}
				}
			}()
		}
	}()
	return box
}

func (b *recoveryMailbox) next(t *testing.T) string {
	t.Helper()
	select {
	case raw := <-b.messages:
		return raw
	case <-time.After(5 * time.Second):
		t.Fatal("no account email delivered")
		return ""
	}
}

func recoveryLink(t *testing.T, raw, path string) *url.URL {
	t.Helper()
	for _, text := range regexp.MustCompile(`https?://[^\s<>"']+`).FindAllString(html.UnescapeString(raw), -1) {
		u, err := url.Parse(text)
		if err == nil && u.Path == path {
			return u
		}
	}
	t.Fatalf("email has no %s link", path)
	return nil
}

func assertRecoveryMailRedacted(t *testing.T, conn *pgx.Conn, token, email string) {
	t.Helper()
	var leaked bool
	if err := conn.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM herald_messages m WHERE strpos(row_to_json(m)::text,$1)>0 OR strpos(row_to_json(m)::text,$2)>0)", token, email).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("account credential or address persisted in delivery history")
	}
}

// Keeping creation and delivery outcomes separate prevents a sender failure
// from inviting retries that conflict with the invitation already in storage.
func TestNotifications_invitationEmailReportsDeliveryFailure(t *testing.T) {
	mail := recoverySMTP(t)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.PublicURL = "https://console.loomplane.test"
	cfg.Notifications.SMTPHost = "127.0.0.1"
	cfg.Notifications.SMTPPort = mail.port
	cfg.Notifications.From = "accounts@example.com"
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "inviter@example.com", "Correct-horse-battery-9")
	_ = mail.next(t) // Authsome also requests email verification on signup.
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Mail outcomes", "slug": "mail-outcomes"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var sent struct {
		EmailStatus string `json:"email_status"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "recipient@example.com", "role": "viewer"}, &sent)
	if sent.EmailStatus != "sent" {
		t.Fatalf("invitation email status %q, want sent", sent.EmailStatus)
	}
	link := recoveryLink(t, mail.next(t), "/invitations/accept")
	if link.Host != "console.loomplane.test" || link.Query().Get("token") == "" {
		t.Fatal("invitation email did not use trusted URL")
	}
	if err := mail.close(); err != nil {
		t.Fatal(err)
	}
	var failed struct {
		EmailStatus  string `json:"email_status"`
		EmailMessage string `json:"email_message"`
		AcceptPath   string `json:"accept_path"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "failed@example.com", "role": "viewer"}, &failed)
	if failed.EmailStatus != "failed" || failed.AcceptPath == "" {
		t.Fatal("failed email hid the durable invitation outcome")
	}
	if strings.Contains(failed.EmailMessage, "failed@example.com") || strings.Contains(failed.EmailMessage, "smtp:") {
		t.Fatal("raw sender error leaked")
	}
	// The public recovery response stays generic even when delivery fails.
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/forgot-password", map[string]string{"email": "inviter@example.com"}, nil)
	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	assertRecoveryMailRedacted(t, conn, link.Query().Get("token"), "recipient@example.com")
}

func TestNotifications_recoveryTokenHasOneConcurrentRedeemer(t *testing.T) {
	mail := recoverySMTP(t)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Notifications.SMTPHost = "127.0.0.1"
	cfg.Notifications.SMTPPort = mail.port
	cfg.Notifications.From = "accounts@example.com"
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "race@example.com", "Correct-horse-battery-9")
	c.token = ""
	_ = mail.next(t) // Authsome also requests email verification on signup.
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/forgot-password", map[string]string{"email": "race@example.com"}, nil)
	token := recoveryLink(t, mail.next(t), "/reset-password").Query().Get("token")
	body, err := json.Marshal(map[string]string{"token": token, "new_password": "Race-correct-battery-42"})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		err    error
	}
	second := start(t, cfg)
	defer second.stop()
	results := make(chan result, 8)
	begin := make(chan struct{})
	for index := range 8 {
		base := r.srv.URL
		if index%2 == 1 {
			base = second.srv.URL
		}
		go func() {
			<-begin
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/authsome/v1/reset-password", bytes.NewReader(body))
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Publishable-Key", cfg.Identity.PublishableKey)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			_ = res.Body.Close()
			results <- result{status: res.StatusCode}
		}()
	}
	close(begin)
	succeeded := 0
	for range 8 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.status == http.StatusOK {
			succeeded++
		} else if got.status != http.StatusUnauthorized && got.status != http.StatusTooManyRequests {
			t.Fatalf("concurrent reset status %d", got.status)
		}
	}
	if succeeded != 1 {
		t.Fatalf("one reset token was accepted %d times concurrently", succeeded)
	}
}
