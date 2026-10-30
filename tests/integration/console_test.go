//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	heraldid "github.com/xraph/herald/id"

	"github.com/xraph/loomplane/internal/config"
)

const consoleAPI = "/api/_console/api/dashboard/v1"

// consoleReply is one console response.
type consoleReply struct {
	status      int
	contentType string
	body        string
}

// consoleCall sends one request to the console API as c, with its cookie
// if it has one. A stream is cut off once its headers arrive.
func consoleCall(c *setupClient, method, path, body string) consoleReply {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	out := consoleReply{status: res.StatusCode, contentType: res.Header.Get("Content-Type")}
	if strings.HasPrefix(out.contentType, "text/event-stream") {
		return out
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	out.body = string(raw)
	return out
}

// envelope is a contract request for the console API.
func envelope(t *testing.T, kind, contributor, intent string, payload any, csrf string) string {
	t.Helper()
	req := map[string]any{"envelope": "v1", "kind": kind, "contributor": contributor, "intent": intent, "payload": payload}
	if kind == "command" {
		req["csrf"] = csrf
		req["idempotencyKey"] = contributor + "/" + intent + "/" + time.Now().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// consoleRoutes is a request to every route the dashboard mounts under the
// console, and to paths it doesn't.
func consoleRoutes(t *testing.T) [][3]string {
	query := envelope(t, "query", "authsome", "users.list", map[string]any{}, "")
	return [][3]string{
		{http.MethodGet, consoleAPI + "/principal", ""},
		{http.MethodGet, consoleAPI + "/capabilities", ""},
		{http.MethodGet, consoleAPI + "/csrf", ""},
		{http.MethodGet, consoleAPI + "/stream", ""},
		{http.MethodPost, consoleAPI + "/stream/control", `{"streamID":"x","op":"unsubscribe","subscriptionID":"1"}`},
		{http.MethodPost, consoleAPI, query},
		{http.MethodPost, consoleAPI + "/", query},
		{http.MethodPost, "//api//_console/api/dashboard/v1", query},
		{http.MethodGet, "/api/_console/export/json", ""},
		{http.MethodGet, "/api/_console/export/csv", ""},
		{http.MethodGet, "/api/_console/export/prometheus", ""},
		{http.MethodGet, "/api/_console", ""},
		{http.MethodGet, "/api/_console/", ""},
		{http.MethodGet, "/api/_console/index.html", ""},
		{http.MethodGet, "/api/_console/nothing/here", ""},
		{http.MethodHead, consoleAPI + "/principal", ""},
		{http.MethodOptions, consoleAPI, ""},
		{http.MethodPut, consoleAPI, query},
		{http.MethodDelete, consoleAPI + "/stream", ""},
	}
}

// requireHidden fails unless every console route answers c exactly as an
// unknown API path answers the same request.
func requireHidden(t *testing.T, c *setupClient, who string) {
	t.Helper()
	for _, rt := range consoleRoutes(t) {
		unknown := consoleCall(c, rt[0], "/api/_nothing_here", rt[2])
		if unknown.status != http.StatusNotFound {
			t.Fatalf("%s %s on an unknown API path answers %+v, want 404", who, rt[0], unknown)
		}
		if got := consoleCall(c, rt[0], rt[1], rt[2]); got != unknown {
			t.Errorf("%s: %s %s = %+v, want the unknown-path answer %+v", who, rt[0], rt[1], got, unknown)
		}
	}
}

func TestConsole(t *testing.T) {
	cfg := config.Defaults()
	r, owner := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := owner.setup(ada); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	anon := &setupClient{t: t, base: owner.base, key: owner.key}
	unknown := consoleCall(anon, http.MethodGet, "/api/_nothing_here", "")
	if unknown.status != http.StatusNotFound {
		t.Fatalf("an unknown API path answers %+v, want 404", unknown)
	}

	t.Run("hidden from anonymous visitors", func(t *testing.T) {
		requireHidden(t, anon, "anonymous")
		// A cookie that names no session is nobody too.
		forged := &setupClient{t: t, base: owner.base, key: owner.key, cookie: &http.Cookie{Name: "loomplane_session", Value: "forged"}}
		requireHidden(t, forged, "a forged cookie")
	})

	t.Run("not in the OpenAPI document", func(t *testing.T) {
		if got := consoleCall(anon, http.MethodGet, "/openapi.json", ""); got.status != http.StatusOK || strings.Contains(got.body, "_console") {
			t.Fatalf("/openapi.json: %d, mentions the console: %v", got.status, strings.Contains(got.body, "_console"))
		}
	})

	var csrf string
	t.Run("a platform owner gets in", func(t *testing.T) {
		got := consoleCall(owner, http.MethodGet, consoleAPI+"/principal", "")
		var principal struct {
			Authenticated bool     `json:"authenticated"`
			Email         string   `json:"email"`
			Roles         []string `json:"roles"`
		}
		if got.status != http.StatusOK || json.Unmarshal([]byte(got.body), &principal) != nil {
			t.Fatalf("principal: %+v", got)
		}
		if !principal.Authenticated || principal.Email != "ada@example.com" || !contains(principal.Roles, "platform-owner") {
			t.Fatalf("principal = %+v, want Ada as platform-owner", principal)
		}
		for _, path := range []string{"/capabilities", "/csrf"} {
			if res := consoleCall(owner, http.MethodGet, consoleAPI+path, ""); res.status != http.StatusOK {
				t.Fatalf("GET %s: %+v", path, res)
			}
		}
		if res := consoleCall(owner, http.MethodGet, consoleAPI+"/stream", ""); res.status != http.StatusOK || !strings.HasPrefix(res.contentType, "text/event-stream") {
			t.Fatalf("stream: %+v", res)
		}
		var token struct {
			Token string `json:"token"`
		}
		got = consoleCall(owner, http.MethodGet, consoleAPI+"/csrf", "")
		if err := json.Unmarshal([]byte(got.body), &token); err != nil || token.Token == "" {
			t.Fatalf("csrf: %+v", got)
		}
		csrf = token.Token
	})

	t.Run("allowed intents run", func(t *testing.T) {
		got := consoleCall(owner, http.MethodPost, consoleAPI, envelope(t, "query", "authsome", "users.list", map[string]any{}, ""))
		var res struct {
			OK   bool `json:"ok"`
			Data struct {
				Users []struct {
					Email string `json:"email"`
				} `json:"users"`
			} `json:"data"`
		}
		if got.status != http.StatusOK || json.Unmarshal([]byte(got.body), &res) != nil || !res.OK {
			t.Fatalf("users.list: %+v", got)
		}
		if len(res.Data.Users) != 1 || res.Data.Users[0].Email != "ada@example.com" {
			t.Fatalf("users.list = %+v, want Ada alone", res.Data.Users)
		}
		if got := consoleCall(owner, http.MethodPost, consoleAPI, envelope(t, "query", "core-contract", "overview", map[string]any{}, "")); got.status != http.StatusOK {
			t.Fatalf("core overview: %+v", got)
		}
	})

	t.Run("blocked intents are unknown", func(t *testing.T) {
		appID := cfg.Identity.AppID
		for _, req := range []string{
			envelope(t, "command", "authsome", "apps.delete", map[string]string{"id": appID}, csrf),
			envelope(t, "command", "authsome", "auth.setup", map[string]string{"email": "eve@example.com", "password": "Correct-horse-battery-9"}, csrf),
			envelope(t, "query", "authsome", "auth.setupStatus", map[string]any{}, ""),
			envelope(t, "command", "authsome", "settings.update", map[string]any{}, csrf),
			envelope(t, "command", "authsome", "users.delete", map[string]any{}, csrf),
			envelope(t, "command", "authsome", "roles.assign", map[string]any{}, csrf),
			envelope(t, "command", "authsome", "auth.toggleFeature", map[string]any{}, csrf),
			// The field names are matched without regard to case, as the
			// dashboard's own decoder matches them.
			`{"envelope":"v1","KIND":"command","Contributor":"authsome","INTENT":"apps.delete","payload":{"id":"` + appID + `"},"csrf":"` + csrf + `","idempotencyKey":"caps"}`,
		} {
			if got := consoleCall(owner, http.MethodPost, consoleAPI, req); got != unknown {
				t.Errorf("POST %s = %+v, want the unknown-path answer", req, got)
			}
		}
		// A subscription outside the core contributor is refused before the
		// stream is even looked up, a library's as much as authsome's.
		for _, sub := range []string{
			`{"streamID":"x","op":"subscribe","subscriptionID":"1","contributor":"authsome","intent":"users.list"}`,
			`{"streamID":"x","op":"subscribe","subscriptionID":"2","contributor":"keysmith","intent":"keys.list"}`,
		} {
			if got := consoleCall(owner, http.MethodPost, consoleAPI+"/stream/control", sub); got != unknown {
				t.Errorf("subscribe %s = %+v, want the unknown-path answer", sub, got)
			}
		}
		// So is anything under the console that isn't a route it uses.
		for _, path := range []string{"/api/_console", "/api/_console/nothing/here", "/api/_console/index.html"} {
			if got := consoleCall(owner, http.MethodGet, path, ""); got != unknown {
				t.Errorf("GET %s = %+v, want the unknown-path answer", path, got)
			}
		}
		if n := countRows(t, cfg.Database.URL, `SELECT count(*) FROM authsome_apps WHERE id = '`+appID+`'`); n != 1 {
			t.Fatalf("Loomplane's app: %d rows after a blocked apps.delete, want 1", n)
		}
	})

	t.Run("every library's reads, none of its writes", func(t *testing.T) {
		got := consoleCall(owner, http.MethodGet, consoleAPI+"/capabilities", "")
		var caps struct {
			Contributors []struct {
				Name string `json:"name"`
			} `json:"contributors"`
		}
		if got.status != http.StatusOK || json.Unmarshal([]byte(got.body), &caps) != nil {
			t.Fatalf("capabilities: %+v", got)
		}
		registered := map[string]bool{}
		for _, c := range caps.Contributors {
			registered[c.Name] = true
		}
		for _, name := range []string{
			"core-contract", "authsome", "waitlist", "organization", "password", "notification",
			"ctrlplane", "dispatch", "trove", "warden", "chronicle", "herald", "relay", "keysmith",
		} {
			if !registered[name] {
				t.Errorf("capabilities don't list %s: %v", name, registered)
			}
		}
		for _, tc := range []struct {
			contributor, query, command string
			// readStatus is what the library itself answers a platform
			// owner, once the gate has let the read through.
			readStatus int
		}{
			// The platform roles hold Warden's read grants and nothing else.
			{"warden", "overview.stats", "roles.create", http.StatusOK},
			{"warden", "roles.list", "roles.delete", http.StatusOK},
			{"chronicle", "overview.stats", "checkpoints.take", http.StatusOK},
			{"herald", "engine.info", "send.test", http.StatusOK},
			{"relay", "endpoints.list", "endpoints.create", http.StatusOK},
			{"keysmith", "settings", "keys.create", http.StatusOK},
			// A console read reaches Ctrl Plane as a system admin.
			{"ctrlplane", "system.stats", "instances.delete", http.StatusOK},
			{"ctrlplane", "tenants.list", "tenants.delete", http.StatusOK},
			// Dispatch's operator boundary isn't configured: it needs
			// Dispatch's durable delivery audit, so it answers every operator
			// read as unavailable.
			{"dispatch", "overview.summary", "jobs.cancel", http.StatusServiceUnavailable},
			{"trove", "stores.list", "buckets.create", http.StatusOK},
			{"organization", "orgs.list", "orgs.delete", http.StatusOK},
		} {
			got := consoleCall(owner, http.MethodPost, consoleAPI, envelope(t, "query", tc.contributor, tc.query, map[string]any{}, ""))
			if got == unknown || got.status != tc.readStatus {
				t.Errorf("%s/%s: %+v, want %d", tc.contributor, tc.query, got, tc.readStatus)
			}
			cmd := envelope(t, "command", tc.contributor, tc.command, map[string]any{}, csrf)
			if got := consoleCall(owner, http.MethodPost, consoleAPI, cmd); got != unknown {
				t.Errorf("%s/%s = %+v, want the unknown-path answer", tc.contributor, tc.command, got)
			}
		}
		// Reads that hand out a download link stay blocked.
		for _, req := range []string{
			envelope(t, "query", "trove", "objects.contentUrl", map[string]string{"bucket": "backups", "key": "x"}, ""),
			envelope(t, "query", "dispatch", "artifacts.presign", map[string]string{"id": "x"}, ""),
			// A command sent as a query is no read.
			envelope(t, "query", "keysmith", "keys.create", map[string]any{}, ""),
		} {
			if got := consoleCall(owner, http.MethodPost, consoleAPI, req); got != unknown {
				t.Errorf("POST %s = %+v, want the unknown-path answer", req, got)
			}
		}
	})

	t.Run("library reads keep secrets out", func(t *testing.T) {
		// A webhook endpoint, whose signing secret Loomplane stores encrypted
		// and Relay's store hands back decrypted.
		status, raw, _ := owner.send(http.MethodPost, "/api/v1/notifications/webhooks", map[string]string{"url": "https://93.184.216.34/console-test"})
		var hook struct {
			Endpoint struct {
				ID string `json:"id"`
			} `json:"endpoint"`
			Secret string `json:"secret"`
		}
		if status != http.StatusOK || json.Unmarshal(raw, &hook) != nil || hook.Endpoint.ID == "" || hook.Secret == "" {
			t.Fatalf("create a webhook: %d %s", status, raw)
		}
		// A mail provider whose credential is stored as it is, so the stored
		// value is the secret itself.
		const credential = "console-test-provider-secret"
		providerID := heraldid.NewProviderID().String()
		execSQL(t, cfg.Database.URL, `INSERT INTO herald_providers (id, app_id, name, channel, driver, credentials, settings, priority, enabled, created_at, updated_at)
			VALUES ($1, $2, 'console-test', 'email', 'smtp', $3, '{"host":"smtp.example.com"}', 9, false, now(), now())`,
			providerID, cfg.Identity.AppID, `{"username":"console-test","password":"`+credential+`"}`)
		for _, tc := range []struct {
			contributor, intent string
			payload             any
			mustShow, secret    string
		}{
			{"relay", "endpoints.list", map[string]any{}, hook.Endpoint.ID, hook.Secret},
			{"relay", "endpoints.detail", map[string]string{"id": hook.Endpoint.ID}, hook.Endpoint.ID, hook.Secret},
			{"herald", "providers.list", map[string]any{}, providerID, credential},
			{"herald", "providers.detail", map[string]string{"id": providerID}, providerID, credential},
		} {
			got := consoleCall(owner, http.MethodPost, consoleAPI, envelope(t, "query", tc.contributor, tc.intent, tc.payload, ""))
			if got.status != http.StatusOK || !strings.Contains(got.body, tc.mustShow) {
				t.Fatalf("%s/%s: %+v, want 200 naming %s", tc.contributor, tc.intent, got, tc.mustShow)
			}
			if strings.Contains(got.body, tc.secret) {
				t.Errorf("%s/%s returns the stored secret: %s", tc.contributor, tc.intent, got.body)
			}
		}
	})

	t.Run("approving a waitlist entry", func(t *testing.T) {
		if status, raw, _ := anon.send(http.MethodPost, "/authsome/v1/waitlist/join", map[string]string{"email": "grace@example.com"}); status >= 300 {
			t.Fatalf("join the waitlist: %d %s", status, raw)
		}
		got := consoleCall(owner, http.MethodPost, consoleAPI, envelope(t, "query", "waitlist", "waitlist.list", map[string]string{"email": "grace@example.com"}, ""))
		var list struct {
			Data struct {
				Entries []struct {
					ID     string `json:"id"`
					Email  string `json:"email"`
					Status string `json:"status"`
				} `json:"entries"`
			} `json:"data"`
		}
		if got.status != http.StatusOK || json.Unmarshal([]byte(got.body), &list) != nil || len(list.Data.Entries) != 1 {
			t.Fatalf("waitlist.list: %+v", got)
		}
		entry := list.Data.Entries[0]
		if entry.Status != "pending" {
			t.Fatalf("Grace's entry = %+v, want pending", entry)
		}
		if got := consoleCall(owner, http.MethodPost, consoleAPI, envelope(t, "command", "waitlist", "waitlist.approve", map[string]string{"id": entry.ID}, csrf)); got.status != http.StatusOK {
			t.Fatalf("waitlist.approve: %+v", got)
		}
		if n := countRows(t, cfg.Database.URL, `SELECT count(*) FROM authsome_waitlist_entries WHERE email = 'grace@example.com' AND status = 'approved'`); n != 1 {
			t.Fatalf("%d approved entries for Grace, want 1", n)
		}
	})

	t.Run("hidden from someone without a platform role", func(t *testing.T) {
		// Approved, Grace signs up and signs in like anyone else.
		if status, why := signUp(anon, "grace@example.com"); status != http.StatusCreated {
			t.Fatalf("Grace's sign-up: %d %+v", status, why)
		}
		status, raw, cookies := anon.send(http.MethodPost, "/authsome/v1/signin", map[string]string{"email": "grace@example.com", "password": "Correct-horse-battery-9"})
		if status != http.StatusOK {
			t.Fatalf("Grace's sign-in: %d %s", status, raw)
		}
		grace := &setupClient{t: t, base: owner.base, key: owner.key}
		for _, ck := range cookies {
			if ck.Name == "loomplane_session" && ck.Value != "" {
				grace.cookie = ck
			}
		}
		if grace.cookie == nil {
			t.Fatalf("Grace's sign-in set no session cookie: %v", cookies)
		}
		// Her session works elsewhere, so the console's answer is about her role.
		if status, raw, _ := grace.send(http.MethodGet, "/authsome/v1/me", nil); status != http.StatusOK {
			t.Fatalf("Grace's session: %d %s", status, raw)
		}
		requireHidden(t, grace, "Grace")
		for _, contributor := range []string{"warden", "chronicle", "herald", "relay", "keysmith", "dispatch"} {
			req := envelope(t, "query", contributor, "overview.stats", map[string]any{}, "")
			if got := consoleCall(grace, http.MethodPost, consoleAPI, req); got != unknown {
				t.Errorf("Grace's %s read = %+v, want the unknown-path answer", contributor, got)
			}
		}
		// Signed in, she still isn't told where the console is.
		if got := grace.publicConfig(); got.ConsolePath != nil {
			t.Errorf("Grace's public config has the console path: %+v", got)
		}
		// Her session as a bearer token gets the same answer.
		bearer := consoleBearer(t, owner.base, grace.cookie.Value)
		if bearer != unknown {
			t.Errorf("Grace as a bearer token: %+v, want the unknown-path answer", bearer)
		}
	})
}

// consoleBearer reads the console's principal with token as a bearer
// token.
func consoleBearer(t *testing.T, base, token string) consoleReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+consoleAPI+"/principal", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	return consoleReply{status: res.StatusCode, contentType: res.Header.Get("Content-Type"), body: raw.String()}
}
