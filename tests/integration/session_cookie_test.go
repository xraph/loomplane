//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

// browser makes a request the way the dashboard does: with whatever cookies
// it's holding and no Authorization header. The dashboard sends key, the
// publishable key, to authsome only.
func browser(t *testing.T, base, key, method, path string, body any, cookies ...*http.Cookie) answer {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, base+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Publishable-Key", key)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close() // only the status and cookies are read
	return answer{status: res.StatusCode, cookies: res.Cookies()}
}

// answer is what the test needs of a response.
type answer struct {
	status  int
	cookies []*http.Cookie
}

func sessionCookie(a answer) *http.Cookie {
	for _, c := range a.cookies {
		if c.Name == "loomplane_session" {
			return c
		}
	}
	return nil
}

func TestSession_theDashboardSignsInWithAnHttpOnlyCookie(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	key := cfg.Identity.PublishableKey
	c := &client{t: t, base: r.srv.URL, key: key}
	firstAccount(t, c, "ada@example.com", "Correct-horse-battery-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)

	// Signing in sets a cookie of Loomplane's own name, which scripts can't
	// read and other sites' requests don't carry. A shared name would let
	// another authsome app on the same host replace it.
	res := browser(t, r.srv.URL, key, http.MethodPost, "/authsome/v1/signin",
		map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"})
	if res.status != http.StatusOK {
		t.Fatalf("sign-in: %d", res.status)
	}
	cookie := sessionCookie(res)
	if cookie == nil {
		t.Fatalf("no loomplane_session cookie among %v", res.cookies)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Value == "" {
		t.Fatalf("cookie %+v", cookie)
	}
	// Other local apps share localhost's cookies across ports. Authsome's
	// legacy auth_token must not shadow Loomplane's configured cookie.
	legacy := &http.Cookie{Name: "auth_token", Value: "another-apps-session"}
	if got := browser(t, r.srv.URL, key, http.MethodGet, "/authsome/v1/me", nil, legacy, cookie); got.status != http.StatusOK {
		t.Fatalf("another app's cookie shadowed Loomplane's: %d", got.status)
	}

	// The cookie alone is the credential, for authsome and for Loomplane's
	// API alike, which gets no publishable key to name the app by.
	if got := browser(t, r.srv.URL, key, http.MethodGet, "/authsome/v1/me", nil, cookie); got.status != http.StatusOK {
		t.Fatalf("me with the cookie: %d", got.status)
	}
	if got := browser(t, r.srv.URL, key, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, cookie); got.status != http.StatusOK {
		t.Fatalf("switching organization with the cookie: %d", got.status)
	}
	if got := browser(t, r.srv.URL, "", http.MethodGet, "/api/v1/session", nil, cookie); got.status != http.StatusOK {
		t.Fatalf("Loomplane's session with the cookie: %d", got.status)
	}

	// Signing out clears the cookie and ends the session behind it.
	out := browser(t, r.srv.URL, key, http.MethodPost, "/authsome/v1/signout", nil, cookie)
	if out.status != http.StatusOK {
		t.Fatalf("sign-out: %d", out.status)
	}
	if cleared := sessionCookie(out); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("sign-out left the cookie: %+v", cleared)
	}
	if got := browser(t, r.srv.URL, key, http.MethodGet, "/authsome/v1/me", nil, cookie); got.status != http.StatusUnauthorized {
		t.Fatalf("me after sign-out: %d", got.status)
	}
}
