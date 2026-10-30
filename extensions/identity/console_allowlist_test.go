package identity

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"
)

func TestConsoleAllowed(t *testing.T) {
	q, cmd, sub := contract.KindQuery, contract.KindCommand, contract.KindSubscribe
	for _, tc := range []struct {
		contributor, intent string
		kind                contract.Kind
		want                bool
	}{
		{"authsome", "users.list", q, true},
		{"authsome", "users.ban", cmd, true},
		{"authsome", "sessions.revoke", cmd, true},
		{"authsome", "apps.delete", cmd, false},
		{"authsome", "auth.setup", cmd, false},
		{"authsome", "auth.setupStatus", q, false},
		{"authsome", "roles.assign", cmd, false},
		{"authsome", "settings.update", cmd, false},
		{"authsome", "auth.toggleFeature", cmd, false},
		{"authsome", "users.delete", cmd, false},
		{"authsome", "auth.login", cmd, false},
		// An allowed name under the wrong kind is refused too.
		{"authsome", "users.list", cmd, false},
		{"authsome", "users.ban", q, false},
		{"waitlist", "waitlist.approve", cmd, true},
		{"waitlist", "waitlist.list", q, true},
		{"waitlist", "users.list", q, false},
		{"core-contract", "overview", q, true},
		{"core-contract", "metrics.summary", sub, true},
		{"core-contract", "x", cmd, false},
		{"organization", "orgs.list", q, false},
		{"", "users.list", q, false},
		{"Authsome", "users.list", q, false},
		{"authsome/users.list", "", q, false},
		{"dispatch", "jobs.list", q, false},
		{"authsome", "metrics.summary", sub, false},
	} {
		req := contract.Request{Contributor: tc.contributor, Intent: tc.intent, Kind: tc.kind}
		if got := consoleAllowed(req); got != tc.want {
			t.Errorf("consoleAllowed(%s/%s %s) = %v, want %v", tc.contributor, tc.intent, tc.kind, got, tc.want)
		}
	}
}

func TestConsolePath(t *testing.T) {
	for _, tc := range []struct {
		path  string
		under bool
		rest  string
	}{
		{"/api/_console", true, ""},
		{"/api/_console/", true, ""},
		{"/api/_console/api/dashboard/v1", true, "api/dashboard/v1"},
		// Forge's router drops repeated and trailing slashes, so the gate does.
		{"//api//_console///api/dashboard/v1/", true, "api/dashboard/v1"},
		{"/api/_consoles", false, ""},
		{"/api/_Console/api/dashboard/v1", false, ""},
		{"/api/v1/session", false, ""},
		{"/api", false, ""},
		{"/", false, ""},
	} {
		rest, under := consolePath(ConsoleBasePath, tc.path)
		if under != tc.under || strings.Join(rest, "/") != tc.rest {
			t.Errorf("consolePath(%q) = %q, %v; want %q, %v", tc.path, rest, under, tc.rest, tc.under)
		}
	}
}

func TestConsoleRequestAllowed(t *testing.T) {
	envelope := func(contributor, intent string, kind contract.Kind) string {
		b, err := json.Marshal(contract.Request{Envelope: "v1", Kind: kind, Contributor: contributor, Intent: intent})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const contractPath, control = "api/dashboard/v1", "api/dashboard/v1/stream/control"
	for name, tc := range map[string]struct {
		method, rest, body string
		want               bool
	}{
		"allowed query":   {http.MethodPost, contractPath, envelope("authsome", "users.list", contract.KindQuery), true},
		"blocked command": {http.MethodPost, contractPath, envelope("authsome", "apps.delete", contract.KindCommand), false},
		// encoding/json matches field names without regard to case, and the
		// gate decodes as the handler does, so it sees what the handler sees.
		"field names in caps":  {http.MethodPost, contractPath, `{"envelope":"v1","KIND":"command","Contributor":"authsome","INTENT":"apps.delete"}`, false},
		"duplicate intent key": {http.MethodPost, contractPath, `{"envelope":"v1","kind":"query","contributor":"authsome","intent":"users.list","intent":"auth.setupStatus"}`, false},
		"not JSON":             {http.MethodPost, contractPath, `nope`, false},
		"too big":              {http.MethodPost, contractPath, envelope("authsome", "users.list", contract.KindQuery) + strings.Repeat(" ", consoleMaxBody), false},
		"contract by GET":      {http.MethodGet, contractPath, "", false},
		"subscribe to core":    {http.MethodPost, control, `{"streamID":"s","op":"subscribe","subscriptionID":"1","contributor":"core-contract","intent":"metrics.summary"}`, true},
		"subscribe elsewhere":  {http.MethodPost, control, `{"streamID":"s","op":"subscribe","subscriptionID":"1","contributor":"dispatch","intent":"jobs.tail"}`, false},
		"unsubscribe":          {http.MethodPost, control, `{"streamID":"s","op":"unsubscribe","subscriptionID":"1"}`, true},
		"unknown op":           {http.MethodPost, control, `{"streamID":"s","op":"other"}`, false},
		"principal":            {http.MethodGet, "api/dashboard/v1/principal", "", true},
		"capabilities":         {http.MethodGet, "api/dashboard/v1/capabilities", "", true},
		"csrf":                 {http.MethodGet, "api/dashboard/v1/csrf", "", true},
		"stream":               {http.MethodGet, "api/dashboard/v1/stream", "", true},
		"export":               {http.MethodGet, "export/json", "", true},
		"principal by POST":    {http.MethodPost, "api/dashboard/v1/principal", "", false},
		"a route not used":     {http.MethodPost, "api/dashboard/v1/batch", envelope("authsome", "users.list", contract.KindQuery), false},
		"the base itself":      {http.MethodGet, "", "", false},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, "/", strings.NewReader(tc.body))
		if got := consoleRequestAllowed(r, segments(tc.rest)); got != tc.want {
			t.Errorf("%s: allowed = %v, want %v", name, got, tc.want)
		}
		// What the handler reads next is what the gate read.
		if tc.want && tc.method == http.MethodPost {
			left, err := io.ReadAll(r.Body)
			if err != nil || string(left) != tc.body {
				t.Errorf("%s: body after the gate = %q (%v), want %q", name, left, err, tc.body)
			}
		}
	}
}
