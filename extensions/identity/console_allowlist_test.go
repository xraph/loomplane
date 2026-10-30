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

// consoleTestRegistry stands in for the dashboard's contract registry: a
// few intents of every contributor Loomplane registers, under the kind each
// one's manifest gives it.
func consoleTestRegistry(t *testing.T) contract.Registry {
	t.Helper()
	q, cmd, sub := contract.IntentKindQuery, contract.IntentKindCommand, contract.IntentKindSubscription
	intents := map[string]map[string]contract.IntentKind{
		"core-contract": {"overview": q, "metrics.summary": sub},
		"authsome": {
			"users.list": q, "users.detail": q, "webhooks.detail": q, "credentials.detail": q,
			"auth.setupStatus": q, "auth.dynamicConfig": q,
			"users.ban": cmd, "users.unban": cmd, "sessions.revoke": cmd, "apps.delete": cmd,
			"auth.setup": cmd, "roles.assign": cmd, "settings.update": cmd,
			"auth.toggleFeature": cmd, "users.delete": cmd, "auth.login": cmd,
		},
		"waitlist":     {"waitlist.list": q, "waitlist.approve": cmd},
		"organization": {"orgs.list": q, "orgs.delete": cmd},
		"notification": {"notification.templates.list": q},
		"password":     {"password.policy": q},
		"ctrlplane":    {"system.stats": q, "secrets.list": q, "instances.delete": cmd},
		"dispatch":     {"jobs.list": q, "overview.summary": q, "artifacts.presign": q, "jobs.cancel": cmd},
		"trove":        {"buckets.list": q, "objects.list": q, "objects.contentUrl": q, "objects.delete": cmd, "objects.presign": cmd},
		"warden":       {"roles.list": q, "overview.stats": q, "roles.create": cmd, "schema.apply": cmd},
		"chronicle":    {"overview.stats": q, "events.list": q, "erasures.request": cmd, "retention.enforce": cmd},
		"herald": {
			"engine.info": q, "providers.list": q, "providers.detail": q, "messages.detail": q,
			"providers.create": cmd, "providers.update": cmd, "send.test": cmd,
		},
		"relay":    {"endpoints.list": q, "endpoints.detail": q, "endpoints.create": cmd, "endpoints.rotateSecret": cmd},
		"keysmith": {"settings": q, "keys.list": q, "keys.detail": q, "keys.create": cmd, "keys.rotate": cmd, "keys.revoke": cmd},
		"vault": {
			"overview.stats": q, "secrets.list": q, "secrets.detail": q, "secrets.versions": q, "config.resolve": q,
			"secrets.create": cmd, "secrets.update": cmd, "rotation.rotateNow": cmd,
		},
	}
	reg := contract.NewRegistry()
	for contributor, byName := range intents {
		m := &contract.ContractManifest{SchemaVersion: 1, Contributor: contract.Contributor{Name: contributor}}
		for name, kind := range byName {
			capability := contract.CapRead
			if kind == cmd {
				capability = contract.CapWrite
			}
			m.Intents = append(m.Intents, contract.Intent{Name: name, Kind: kind, Version: 1, Capability: capability})
		}
		if err := reg.Register(m); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func TestConsoleAllowed(t *testing.T) {
	reg := consoleTestRegistry(t)
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
		{"", "users.list", q, false},
		{"Authsome", "users.list", q, false},
		{"authsome/users.list", "", q, false},
		{"authsome", "metrics.summary", sub, false},
		// Every registered contributor's reads are open: an authsome read
		// that was never on a list, and the authsome plugins'.
		{"authsome", "webhooks.detail", q, true},
		{"organization", "orgs.list", q, true},
		{"notification", "notification.templates.list", q, true},
		{"password", "password.policy", q, true},
		{"organization", "orgs.delete", cmd, false},
		// authsome's own sign-up and setup pages read these; Loomplane serves
		// its own, so the console never does.
		{"authsome", "auth.dynamicConfig", q, false},
		// An intent the registry doesn't have is no read, whatever it's
		// called, and nor is one it has as a command.
		{"authsome", "users.nothing", q, false},
		{"dispatch", "jobs.cancel", q, false},
		{"nobody", "overview", q, false},
		// The library contributors: reads open, commands blocked.
		{"ctrlplane", "system.stats", q, true},
		{"ctrlplane", "secrets.list", q, true},
		{"ctrlplane", "instances.delete", cmd, false},
		{"dispatch", "jobs.list", q, true},
		{"dispatch", "overview.summary", q, true},
		{"dispatch", "jobs.cancel", cmd, false},
		{"trove", "buckets.list", q, true},
		{"trove", "objects.list", q, true},
		{"trove", "objects.delete", cmd, false},
		{"trove", "objects.presign", cmd, false},
		{"warden", "roles.list", q, true},
		{"warden", "overview.stats", q, true},
		{"warden", "roles.create", cmd, false},
		{"warden", "schema.apply", cmd, false},
		{"chronicle", "overview.stats", q, true},
		{"chronicle", "events.list", q, true},
		{"chronicle", "erasures.request", cmd, false},
		{"chronicle", "retention.enforce", cmd, false},
		{"herald", "engine.info", q, true},
		{"herald", "providers.list", q, true},
		{"herald", "providers.detail", q, true},
		{"herald", "messages.detail", q, true},
		{"herald", "providers.create", cmd, false},
		{"herald", "providers.update", cmd, false},
		{"herald", "send.test", cmd, false},
		{"relay", "endpoints.list", q, true},
		{"relay", "endpoints.detail", q, true},
		{"relay", "endpoints.create", cmd, false},
		{"relay", "endpoints.rotateSecret", cmd, false},
		{"keysmith", "settings", q, true},
		{"keysmith", "keys.list", q, true},
		{"keysmith", "keys.detail", q, true},
		{"keysmith", "keys.create", cmd, false},
		{"keysmith", "keys.rotate", cmd, false},
		{"keysmith", "keys.revoke", cmd, false},
		{"vault", "overview.stats", q, true},
		{"vault", "secrets.list", q, true},
		{"vault", "secrets.detail", q, true},
		{"vault", "secrets.versions", q, true},
		{"vault", "config.resolve", q, true},
		{"vault", "secrets.create", cmd, false},
		{"vault", "secrets.update", cmd, false},
		{"vault", "rotation.rotateNow", cmd, false},
		// Reads that hand out a link to stored content stay blocked.
		{"trove", "objects.contentUrl", q, false},
		{"dispatch", "artifacts.presign", q, false},
		// Subscriptions stay the core contributor's alone.
		{"dispatch", "jobs.list", sub, false},
		{"warden", "overview.stats", sub, false},
	} {
		req := contract.Request{Contributor: tc.contributor, Intent: tc.intent, Kind: tc.kind}
		if got := consoleAllowed(reg, req); got != tc.want {
			t.Errorf("consoleAllowed(%s/%s %s) = %v, want %v", tc.contributor, tc.intent, tc.kind, got, tc.want)
		}
	}
}

func TestConsoleAllowedVersions(t *testing.T) {
	reg := consoleTestRegistry(t)
	// users.list is a query at version 2 and a command at version 3, and
	// the console reads the version it asks for, or the highest.
	if err := reg.Register(&contract.ContractManifest{SchemaVersion: 1, Contributor: contract.Contributor{Name: "versioned"}, Intents: []contract.Intent{
		{Name: "users.list", Kind: contract.IntentKindQuery, Version: 2, Capability: contract.CapRead},
		{Name: "users.list", Kind: contract.IntentKindCommand, Version: 3, Capability: contract.CapWrite},
	}}); err != nil {
		t.Fatal(err)
	}
	for version, want := range map[int]bool{0: false, 1: false, 2: true, 3: false} {
		req := contract.Request{Contributor: "versioned", Intent: "users.list", IntentVersion: version, Kind: contract.KindQuery}
		if got := consoleAllowed(reg, req); got != want {
			t.Errorf("users.list@%d as a query: allowed = %v, want %v", version, got, want)
		}
	}
	// Before the dashboard has handed over its registry, no read is known.
	if consoleAllowed(nil, contract.Request{Contributor: "authsome", Intent: "users.list", Kind: contract.KindQuery}) {
		t.Error("a read is allowed with no registry")
	}
	// Commands don't need the registry: they're listed.
	if !consoleAllowed(nil, contract.Request{Contributor: "authsome", Intent: "users.ban", Kind: contract.KindCommand}) {
		t.Error("an allowed command is refused with no registry")
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
	reg := consoleTestRegistry(t)
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
		"library read":         {http.MethodPost, contractPath, envelope("keysmith", "keys.list", contract.KindQuery), true},
		"library command":      {http.MethodPost, contractPath, envelope("keysmith", "keys.create", contract.KindCommand), false},
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
		if got := consoleRequestAllowed(reg, r, segments(tc.rest)); got != tc.want {
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
