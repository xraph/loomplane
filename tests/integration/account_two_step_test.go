//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/xraph/authsome/plugins/mfa"
	"github.com/xraph/authsome/plugins/passkey/passkeytest"

	"github.com/xraph/loomplane/internal/config"
)

// passkeyOrigin is where the test's browser says its pages come from. On
// localhost, authsome accepts any origin whose host is the RP ID.
const passkeyOrigin = "http://localhost:3000"

// tab is a browser tab: it keeps the cookies the server sets, sends the
// publishable key and an Origin, and never an Authorization header, so the
// session is the HttpOnly cookie alone, as it is for the dashboard.
type tab struct {
	t    *testing.T
	base string
	key  string
	hc   *http.Client
}

func newTab(t *testing.T, base, key string) *tab {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &tab{t: t, base: base, key: key, hc: &http.Client{Jar: jar}}
}

// call sends body, raw if it's []byte, and decodes a 2xx answer into out.
func (b *tab) call(method, path string, body, out any) (int, string) {
	b.t.Helper()
	var data []byte
	switch v := body.(type) {
	case nil:
	case []byte:
		data = v
	default:
		var err error
		if data, err = json.Marshal(v); err != nil {
			b.t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(b.t.Context(), method, b.base+path, bytes.NewReader(data))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", b.key)
	req.Header.Set("Origin", passkeyOrigin)
	res, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	if out != nil && res.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			b.t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
	return res.StatusCode, string(raw)
}

// ok sends the request and fails the test unless it answers 200.
func (b *tab) ok(method, path string, body, out any) {
	b.t.Helper()
	if got, raw := b.call(method, path, body, out); got != http.StatusOK {
		b.t.Fatalf("%s %s: %d %s, want 200", method, path, got, raw)
	}
}

// adopt puts a session token in the tab's cookie jar, as the setup page's
// response would have.
func (b *tab) adopt(token string) {
	u, err := url.Parse(b.base)
	if err != nil {
		b.t.Fatal(err)
	}
	b.hc.Jar.SetCookies(u, []*http.Cookie{{Name: "loomplane_session", Value: token, Path: "/"}})
}

func (b *tab) twoStep() bool {
	b.t.Helper()
	var out struct {
		Enabled bool `json:"enabled"`
	}
	b.ok(http.MethodGet, "/api/v1/account/two-step", nil, &out)
	return out.Enabled
}

// twoStepEmail is the account the two-step test signs in as.
const twoStepEmail = "ada@example.com"

// signIn posts the account's password and returns the ticket when a code
// is asked for, or "" when the password alone signed the tab in.
func (b *tab) signIn() string {
	b.t.Helper()
	status, raw := b.call(http.MethodPost, "/authsome/v1/signin", map[string]string{"email": twoStepEmail, "password": testPassword}, nil)
	switch status {
	case http.StatusOK:
		return ""
	case http.StatusForbidden:
		var refused struct {
			Type   string `json:"type"`
			Ticket string `json:"mfa_ticket"`
		}
		if err := json.Unmarshal([]byte(raw), &refused); err != nil || refused.Type != "mfa_required" || refused.Ticket == "" {
			b.t.Fatalf("sign-in refused without a ticket: %s", raw)
		}
		return refused.Ticket
	}
	b.t.Fatalf("sign-in: %d %s", status, raw)
	return ""
}

func (b *tab) signedIn() bool {
	b.t.Helper()
	status, _ := b.call(http.MethodGet, "/authsome/v1/me", nil, nil)
	return status == http.StatusOK
}

// authenticator hands out TOTP codes for a secret, each from a time-step
// not yet used, since authsome refuses a step it has already accepted.
type authenticator struct {
	t      *testing.T
	secret string
	used   map[int64]bool
}

func (a *authenticator) code() string {
	a.t.Helper()
	for _, offset := range []time.Duration{0, 30 * time.Second, -30 * time.Second} {
		at := time.Now().Add(offset)
		step := at.Unix() / 30
		if a.used[step] {
			continue
		}
		a.used[step] = true
		code, err := mfa.GenerateTOTPCodeAt(a.secret, at)
		if err != nil {
			a.t.Fatal(err)
		}
		return code
	}
	a.t.Fatal("no unused time-step left within the window")
	return ""
}

func TestAccount_twoStepAndPasskeys(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Identity.PasskeyRPID = "localhost"
	cfg.Identity.PasskeyOrigins = []string{passkeyOrigin}
	r := start(t, cfg)
	defer r.stop()
	key := cfg.Identity.PublishableKey
	base := r.srv.URL

	ada := newTab(t, base, key)
	ada.adopt(setUp(t, &client{t: t, base: base, key: key}, map[string]string{
		"name": "ada", "email": twoStepEmail, "password": testPassword, "organization_name": firstOrg,
	}))

	// The status is the signed-in account's own: no session, no answer.
	if status, _ := newTab(t, base, key).call(http.MethodGet, "/api/v1/account/two-step", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("two-step status without a session: %d, want 401", status)
	}
	if ada.twoStep() {
		t.Fatal("two-step is on before it was set up")
	}

	// Turning it on: a secret for the authenticator app, then a code from
	// it, which answers with the recovery codes once.
	var enrolled struct {
		Secret     string `json:"secret"`
		OTPAuthURL string `json:"otpauth_url"`
	}
	ada.ok(http.MethodPost, "/authsome/v1/mfa/enroll", map[string]string{}, &enrolled)
	if enrolled.Secret == "" || enrolled.OTPAuthURL == "" {
		t.Fatalf("enrolment %+v", enrolled)
	}
	if ada.twoStep() {
		t.Fatal("two-step is on before a code confirmed it")
	}
	app := &authenticator{t: t, secret: enrolled.Secret, used: map[int64]bool{}}
	var verified struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	ada.ok(http.MethodPost, "/authsome/v1/mfa/verify", map[string]string{"code": app.code()}, &verified)
	if len(verified.RecoveryCodes) == 0 {
		t.Fatal("verifying gave no recovery codes")
	}
	if !ada.twoStep() {
		t.Fatal("two-step is off after it was confirmed")
	}

	// With it on, the password alone signs nobody in.
	laptop := newTab(t, base, key)
	ticket := laptop.signIn()
	if ticket == "" {
		t.Fatal("the password alone signed in with two-step on")
	}
	if laptop.signedIn() {
		t.Fatal("signed in before the code")
	}
	if status, _ := laptop.call(http.MethodPost, "/authsome/v1/mfa/challenge", map[string]string{"mfa_ticket": ticket, "code": "000000"}, nil); status != http.StatusUnauthorized {
		t.Fatalf("a wrong code: %d, want 401", status)
	}
	laptop.ok(http.MethodPost, "/authsome/v1/mfa/challenge", map[string]string{"mfa_ticket": ticket, "code": app.code()}, nil)
	if !laptop.signedIn() {
		t.Fatal("the code didn't sign the tab in")
	}

	// A recovery code stands in for the authenticator, once.
	phone := newTab(t, base, key)
	phone.ok(http.MethodPost, "/authsome/v1/mfa/challenge",
		map[string]string{"mfa_ticket": phone.signIn(), "code": verified.RecoveryCodes[0]}, nil)
	if !phone.signedIn() {
		t.Fatal("the recovery code didn't sign the tab in")
	}
	again := newTab(t, base, key)
	if status, _ := again.call(http.MethodPost, "/authsome/v1/mfa/challenge",
		map[string]string{"mfa_ticket": again.signIn(), "code": verified.RecoveryCodes[0]}, nil); status != http.StatusUnauthorized {
		t.Fatalf("a spent recovery code: %d, want 401", status)
	}

	// New recovery codes replace the old ones.
	var fresh struct {
		Codes []string `json:"codes"`
	}
	ada.ok(http.MethodPost, "/authsome/v1/mfa/recovery/regenerate", map[string]string{}, &fresh)
	if len(fresh.Codes) == 0 {
		t.Fatal("regenerating gave no codes")
	}
	old := newTab(t, base, key)
	if status, _ := old.call(http.MethodPost, "/authsome/v1/mfa/challenge",
		map[string]string{"mfa_ticket": old.signIn(), "code": verified.RecoveryCodes[1]}, nil); status != http.StatusUnauthorized {
		t.Fatalf("a replaced recovery code: %d, want 401", status)
	}

	// A passkey: added from the signed-in account, then used to sign in
	// with no password and, being two factors already, no code either.
	device := passkeytest.NewAuthenticator(passkeyOrigin, "localhost")
	var creation struct {
		Options json.RawMessage `json:"options"`
	}
	// With two-step on, adding one takes a code: a passkey that verifies
	// its user signs in without one.
	if status, _ := ada.call(http.MethodPost, "/authsome/v1/passkeys/register/begin", map[string]string{"display_name": "Laptop"}, nil); status != http.StatusUnauthorized {
		t.Fatalf("adding a passkey without a code: %d, want 401", status)
	}
	ada.ok(http.MethodPost, "/authsome/v1/passkeys/register/begin", map[string]string{"display_name": "Laptop", "code": app.code()}, &creation)
	attestation, err := device.Create(creation.Options)
	if err != nil {
		t.Fatal(err)
	}
	ada.ok(http.MethodPost, "/authsome/v1/passkeys/register/finish", attestation, nil)
	var listed struct {
		Credentials []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"credentials"`
	}
	ada.ok(http.MethodGet, "/authsome/v1/passkeys", nil, &listed)
	if len(listed.Credentials) != 1 || listed.Credentials[0].DisplayName != "Laptop" {
		t.Fatalf("passkeys after adding one: %+v", listed.Credentials)
	}

	kiosk := newTab(t, base, key)
	passkeySignIn := func(b *tab) (int, string) {
		t.Helper()
		var request struct {
			Options json.RawMessage `json:"options"`
		}
		b.ok(http.MethodPost, "/authsome/v1/passkeys/login/begin", map[string]string{}, &request)
		assertion, aerr := device.Get(request.Options)
		if aerr != nil {
			t.Fatal(aerr)
		}
		return b.call(http.MethodPost, "/authsome/v1/passkeys/login/finish", assertion, nil)
	}
	if status, raw := passkeySignIn(kiosk); status != http.StatusOK {
		t.Fatalf("passkey sign-in: %d %s", status, raw)
	}
	if !kiosk.signedIn() {
		t.Fatal("the passkey didn't sign the tab in")
	}

	// Removing it ends sign-in with it.
	ada.ok(http.MethodDelete, "/authsome/v1/passkeys/"+listed.Credentials[0].ID, nil, nil)
	ada.ok(http.MethodGet, "/authsome/v1/passkeys", nil, &listed)
	if len(listed.Credentials) != 0 {
		t.Fatalf("passkeys after removing it: %+v", listed.Credentials)
	}
	if status, _ := passkeySignIn(newTab(t, base, key)); status == http.StatusOK {
		t.Fatal("a removed passkey still signs in")
	}

	// Turning two-step off takes a code, and the password alone signs in
	// again afterwards.
	if status, _ := ada.call(http.MethodDelete, "/authsome/v1/mfa/enrollment", map[string]string{}, nil); status == http.StatusOK {
		t.Fatal("two-step turned off without a code")
	}
	// A recovery code proves the factor here too, which is how someone who
	// lost their phone turns it off.
	ada.ok(http.MethodDelete, "/authsome/v1/mfa/enrollment", map[string]string{"code": fresh.Codes[0]}, nil)
	if ada.twoStep() {
		t.Fatal("two-step is still on after turning it off")
	}
	if newTab(t, base, key).signIn() != "" {
		t.Fatal("a code is still asked for with two-step off")
	}
}
