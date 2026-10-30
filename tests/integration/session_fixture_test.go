//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// firstOrg is the organization firstAccount's setup makes.
const firstOrg = "First organization"

// signedIn creates Ada as the instance's first account, through setup with
// the organization Acme, and signs c in as her. Setup's session already
// acts in Acme.
func signedIn(t *testing.T, c *client) {
	t.Helper()
	c.token = setUp(t, c, ada)
}

// firstAccount creates the instance's first account through setup, the
// only way a fresh instance takes one, and signs c in as it. Setup also
// makes it the organization firstOrg, which the session acts in until the
// test switches to another.
func firstAccount(t *testing.T, c *client, email, password string) {
	t.Helper()
	name, _, _ := strings.Cut(email, "@")
	c.token = setUp(t, c, map[string]string{
		"name": name, "email": email, "password": password, "organization_name": firstOrg,
	})
}

// testPassword is the password account gives everyone.
const testPassword = "Correct-horse-battery-9"

// account signs c in as a new account for email, named for the address's
// local part: the instance's first through setup, as a fresh instance
// requires, and anyone after it through authsome's own sign-up.
func account(t *testing.T, c *client, email string) {
	t.Helper()
	var setup struct {
		Pending bool `json:"pending"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/setup", nil, &setup)
	if setup.Pending {
		firstAccount(t, c, email, testPassword)
		return
	}
	name, _, _ := strings.Cut(email, "@")
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": email, "password": testPassword, "first_name": name}, &auth)
	c.token = auth.SessionToken
}

// setUp posts the setup form and returns the session token from the
// cookie it sets, for a client that sends it as a bearer token.
func setUp(t *testing.T, c *client, form map[string]string) string {
	t.Helper()
	status, raw, cookies := (&setupClient{t: t, base: c.base, key: c.key}).send(http.MethodPost, "/api/v1/setup", form)
	if status != http.StatusOK {
		t.Fatalf("setup as %s: %d %s", form["email"], status, raw)
	}
	for _, ck := range cookies {
		if ck.Name == "loomplane_session" && ck.Value != "" {
			return ck.Value
		}
	}
	t.Fatalf("setup set no session cookie: %v", cookies)
	return ""
}
