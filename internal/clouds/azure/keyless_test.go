package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/clouds/oauth"
)

const (
	testTicket  = "ticket-only-the-issuer-makes"
	testSubject = "org:org_1:connection:conn_1"
)

// requestKey marks the context a test's request is made with, so the
// minter can tell it was asked with that one.
type requestKey struct{}

// countingMinter mints token-1, token-2 and so on, for a handout with the
// test's ticket only, and fails with fail while it's set.
type countingMinter struct {
	mu       sync.Mutex
	minted   int
	deadline bool
	request  any
	fail     error
}

func (m *countingMinter) Mint(ctx context.Context, h keyless.Handout) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return "", m.fail
	}
	if h.Ticket != testTicket || h.Subject != testSubject || h.Audience != "api://AzureADTokenExchange" {
		return "", errors.New("not a handout this test made")
	}
	_, m.deadline = ctx.Deadline()
	m.request = ctx.Value(requestKey{})
	m.minted++
	return fmt.Sprintf("token-%d", m.minted), nil
}

// keylessSettings are what a keyless connection to the test's tenant
// keeps: its app and where it deploys.
func keylessSettings() map[string]string {
	return map[string]string{"tenant_id": tenant, "client_id": client, "subscription_id": sub, "resource_group": "shop-prod"}
}

// posted is every form the token server was sent, in order.
func (ts *tokenServer) posted() []url.Values {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return slices.Clone(ts.forms)
}

func testHandout(t *testing.T, audience string, settings map[string]string) []byte {
	t.Helper()
	out, err := json.Marshal(keyless.Handout{Type: keyless.Type, Subject: testSubject, Audience: audience, Ticket: testTicket, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testKeylessCredential(t *testing.T) Credential {
	t.Helper()
	cred, err := ParseCredential(testHandout(t, "api://AzureADTokenExchange", keylessSettings()), keylessSettings())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cred
}

// keylessSecrets are what a keyless session must never show.
var keylessSecrets = []string{testTicket, "token-", "at-"}

func noKeylessSecrets(t *testing.T, what, s string) {
	t.Helper()
	for _, secret := range keylessSecrets {
		if strings.Contains(s, secret) {
			t.Errorf("%s carries a secret", what)
		}
	}
}

func TestParseCredentialReadsAKeylessHandout(t *testing.T) {
	upper := keylessSettings()
	upper["client_id"] = " " + strings.ToUpper(client) + " "
	cred, err := ParseCredential(testHandout(t, "api://AzureADTokenExchange", upper), keylessSettings())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cred.Keyless() || cred.ClientID != client || cred.Secret != "" || cred.RefreshToken != "" ||
		cred.TenantID != tenant || cred.SubscriptionID != sub || cred.ResourceGroup != "shop-prod" {
		t.Fatalf("credential: keyless %v, client %q, tenant %q, subscription %q, group %q", cred.Keyless(), cred.ClientID, cred.TenantID, cred.SubscriptionID, cred.ResourceGroup)
	}
	if plain, err := ParseCredential([]byte("p~secret"), settings()); err != nil || plain.Keyless() {
		t.Fatalf("a principal: keyless %v, %v", plain.Keyless(), err)
	}
	if user, err := ParseCredential(mustKey(t, "rt-1"), placement()); err != nil || user.Keyless() {
		t.Fatalf("a sign-in: keyless %v, %v", user.Keyless(), err)
	}

	for name, tc := range map[string]struct {
		audience           string
		handout, placement map[string]string
	}{
		"no client":       {"api://AzureADTokenExchange", placement(), keylessSettings()},
		"bad client":      {"api://AzureADTokenExchange", map[string]string{"client_id": "my-app"}, keylessSettings()},
		"no settings":     {"api://AzureADTokenExchange", nil, keylessSettings()},
		"other audience":  {"sts.amazonaws.com", keylessSettings(), keylessSettings()},
		"no tenant":       {"api://AzureADTokenExchange", keylessSettings(), map[string]string{"subscription_id": sub}},
		"no subscription": {"api://AzureADTokenExchange", keylessSettings(), map[string]string{"tenant_id": tenant}},
	} {
		_, err := ParseCredential(testHandout(t, tc.audience, tc.handout), tc.placement)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		noKeylessSecrets(t, name, err.Error())
	}
	broken := []byte(`{"type":"loomplane_keyless","subject":"s","audience":"a","ticket":"` + testTicket + `","settings":"nope"}`)
	if _, err := ParseCredential(broken, keylessSettings()); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), testTicket) {
		t.Errorf("broken handout: %v", errors.Is(err, ErrInvalid))
	}
}

func TestBearerSendsAMintedAssertion(t *testing.T) {
	base, ts := newTokenServer(t, answering(200, `{"access_token":"at-1","expires_in":3600}`))
	minter := &countingMinter{}
	ctx := context.WithValue(t.Context(), requestKey{}, "the request's")
	s, err := base.WithMinter(minter).Session(ctx, testKeylessCredential(t))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	form := ts.last()
	want := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {client},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {"token-1"},
		"scope":                 {"https://management.azure.com/.default"},
	}
	if !maps.EqualFunc(form, want, slices.Equal) {
		keys := slices.Sorted(maps.Keys(form))
		t.Fatalf("form has %v, the minted assertion: %v", keys, form.Get("client_assertion") == "token-1")
	}
	if minter.minted != 1 || !minter.deadline || minter.request != "the request's" {
		t.Fatalf("minted %d, with a deadline %v, with the request's context %v", minter.minted, minter.deadline, minter.request == "the request's")
	}
	if tok, bearerErr := s.bearer(t.Context()); bearerErr != nil || tok != "at-1" {
		t.Fatalf("bearer: %v", bearerErr)
	}

	// The client WithMinter copied still has none, and asks nothing.
	before := len(ts.posted())
	_, err = base.Session(t.Context(), testKeylessCredential(t))
	if !errors.Is(err, keyless.ErrNoMinter) || errors.Is(err, ErrRejected) || len(ts.posted()) != before {
		t.Fatalf("no minter: %v, posted %d", err, len(ts.posted())-before)
	}

	// Nor does a mint that fails, which isn't a refusal.
	minter.fail = errors.New("issuer has no key")
	_, err = base.WithMinter(minter).Session(t.Context(), testKeylessCredential(t))
	if err == nil || errors.Is(err, ErrRejected) || len(ts.posted()) != before {
		t.Fatalf("mint failed: %v, posted %d", err, len(ts.posted())-before)
	}
}

func TestBearerMintsAgainOnRenewal(t *testing.T) {
	// Each token expires inside the session's skew, so every bearer call
	// renews.
	var n int
	var mu sync.Mutex
	c, ts := newTokenServer(t, func(url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		n++
		return 200, fmt.Sprintf(`{"access_token":"at-%d","expires_in":60}`, n)
	})
	minter := &countingMinter{}
	s, err := c.WithMinter(minter).Session(t.Context(), testKeylessCredential(t))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.bearer(t.Context())
	if err != nil || tok != "at-2" {
		t.Fatalf("renewal: %v", err)
	}
	forms := ts.posted()
	if fresh := len(forms) == 2 && forms[0].Get("client_assertion") == "token-1" && forms[1].Get("client_assertion") == "token-2"; minter.minted != 2 || !fresh {
		t.Fatalf("minted %d; the renewal sent a fresh assertion: %v", minter.minted, fresh)
	}

	// A token that lasts is kept, and nothing is minted for it.
	c, _ = newTokenServer(t, answering(200, `{"access_token":"at-long","expires_in":3600}`))
	minter = &countingMinter{}
	if s, err = c.WithMinter(minter).Session(t.Context(), testKeylessCredential(t)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.bearer(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if minter.minted != 1 {
		t.Fatalf("minted %d", minter.minted)
	}
}

// Only Entra refusing the trust itself is ErrRejected. Entra failing, not
// reaching Loomplane's issuer, or finding the token out of its time range
// isn't, and neither is a mint that fails.
func TestBearerSaysTheTrustIsMissing(t *testing.T) {
	const echoed = "No matching federated identity record found for presented assertion"
	for name, tc := range map[string]struct {
		status   int
		body     string
		rejected bool
		code     string
	}{
		"no federated credential": {400, `{"error":"invalid_client","error_description":"AADSTS70021: ` + echoed + `. Assertion Issuer: 'https://loomplane.example'.\r\nTrace ID: x","error_codes":[70021]}`, true, "invalid_client AADSTS70021"},
		"code only in the text":   {401, `{"error":"invalid_client","error_description":"AADSTS70021: ` + echoed + `."}`, true, "invalid_client AADSTS70021"},
		"issuer doesn't match":    {400, `{"error":"invalid_client","error_description":"AADSTS700211: ` + echoed + ` issuer.","error_codes":[700211]}`, true, "invalid_client AADSTS700211"},
		"audience doesn't match":  {400, `{"error":"invalid_client","error_description":"AADSTS700212: ` + echoed + ` audience.","error_codes":[700212]}`, true, "invalid_client AADSTS700212"},
		"subject doesn't match":   {400, `{"error":"invalid_client","error_description":"AADSTS700213: ` + echoed + ` subject.","error_codes":[700213]}`, true, "invalid_client AADSTS700213"},
		"app not in the tenant":   {400, `{"error":"unauthorized_client","error_description":"AADSTS700016: Application with identifier '` + client + `' was not found.","error_codes":[700016]}`, true, "unauthorized_client AADSTS700016"},
		"app disabled":            {400, `{"error":"invalid_client","error_description":"AADSTS7000112: Application is disabled.","error_codes":[7000112]}`, true, "invalid_client AADSTS7000112"},
		"unauthorized, no code":   {400, `{"error":"unauthorized_client"}`, true, "unauthorized_client"},

		"out of its time range": {400, `{"error":"invalid_client","error_description":"AADSTS700024: Client assertion is not within its valid time range.","error_codes":[700024]}`, false, "invalid_client AADSTS700024"},
		"issuer unreachable":    {400, `{"error":"invalid_client","error_description":"AADSTS501661: Error fetching OIDC discovery document.","error_codes":[501661]}`, false, "invalid_client AADSTS501661"},
		"keys unreachable":      {400, `{"error":"unauthorized_client","error_description":"AADSTS50166: Request to External OIDC endpoint failed.","error_codes":[50166]}`, false, "unauthorized_client AADSTS50166"},
		"service error":         {400, `{"error":"unauthorized_client","error_description":"AADSTS50000: There was an error issuing a token.","error_codes":[50000]}`, false, "unauthorized_client AADSTS50000"},
		"stale signing key":     {401, `{"error":"invalid_client","error_description":"AADSTS700027: Client assertion contains an invalid signature.","error_codes":[700027]}`, false, "invalid_client AADSTS700027"},
		"invalid, no code":      {401, `{"error":"invalid_client"}`, false, "invalid_client"},
		"down":                  {503, `{"error":"temporarily_unavailable","error_codes":[70021]}`, false, "temporarily_unavailable"},
		"server error":          {500, `<html>oops</html>`, false, "no reason given"},
		"throttled":             {429, `{"error":"invalid_client","error_codes":[70021]}`, false, "invalid_client"},
	} {
		c, _ := newTokenServer(t, answering(tc.status, tc.body))
		_, err := c.WithMinter(&countingMinter{}).Session(t.Context(), testKeylessCredential(t))
		if err == nil || errors.Is(err, ErrRejected) != tc.rejected || errors.Is(err, oauth.ErrAppRejected) || errors.Is(err, oauth.ErrRefused) {
			t.Errorf("%s: rejected %v, want %v", name, err != nil && errors.Is(err, ErrRejected), tc.rejected)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, tc.code) {
			t.Errorf("%s: the error doesn't say %q", name, tc.code)
		}
		if tc.rejected && !strings.Contains(msg, "Azure doesn't trust Loomplane for this app; check its federated credential") {
			t.Errorf("%s: the refusal doesn't say what to check", name)
		}
		for _, echo := range []string{echoed, "Trace ID", "loomplane.example", client, "oops", "Client assertion"} {
			if strings.Contains(msg, echo) {
				t.Errorf("%s: the error repeats Entra's description", name)
			}
		}
		noKeylessSecrets(t, name, msg)
	}
}

func TestKeylessSettingsTakeTheAppAndPlacement(t *testing.T) {
	c := New("")
	in := keylessSettings()
	in["client_id"] = strings.ToUpper(client)
	in["tenant_id"] = " " + strings.ToUpper(tenant)
	in["signed_in_as"], in["registry_pull"] = "ada@contoso.com", "admin"
	got, err := c.KeylessSettings(in)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, keylessSettings()) {
		t.Fatalf("settings: %v", got)
	}
	noGroup := keylessSettings()
	delete(noGroup, "resource_group")
	if got, err = c.KeylessSettings(noGroup); err != nil || got["resource_group"] != DefaultResourceGroup {
		t.Fatalf("no group: %v, %v", got, err)
	}
	for name, mutate := range map[string]func(map[string]string){
		"no client":       func(m map[string]string) { delete(m, "client_id") },
		"bad client":      func(m map[string]string) { m["client_id"] = "my-app" },
		"no tenant":       func(m map[string]string) { delete(m, "tenant_id") },
		"no subscription": func(m map[string]string) { delete(m, "subscription_id") },
		"bad group":       func(m map[string]string) { m["resource_group"] = "ends-in." },
	} {
		m := keylessSettings()
		mutate(m)
		if _, err := c.KeylessSettings(m); err == nil {
			t.Errorf("%s passed", name)
		}
	}
	if c.KeylessAudience(nil) != "api://AzureADTokenExchange" {
		t.Fatal(c.KeylessAudience(nil))
	}
}

func TestKeylessSetupNamesTheSubject(t *testing.T) {
	steps := New("").KeylessSetup("https://loomplane.example/oidc", testSubject)
	if len(steps) != 2 {
		t.Fatalf("steps: %d", len(steps))
	}
	want := `az ad app federated-credential create --id <client-id> --parameters '{"name":"loomplane","issuer":"https://loomplane.example/oidc","subject":"` + testSubject + `","audiences":["api://AzureADTokenExchange"]}'`
	if steps[0].Command != want {
		t.Fatalf("federated credential:\n%s\nwant\n%s", steps[0].Command, want)
	}
	if steps[1].Command != "az role assignment create --assignee <client-id> --role Contributor --scope /subscriptions/<subscription-id>/resourceGroups/<resource-group>" {
		t.Fatalf("role: %s", steps[1].Command)
	}
	for _, step := range steps {
		if step.Title == "" || step.Body == "" || strings.Contains(step.Title+step.Body+step.Command, "\u2014") {
			t.Errorf("step: %+v", step)
		}
	}
	// A trailing slash names the same issuer the tokens carry.
	if dev := New("").KeylessSetup("http://127.0.0.1:8080/", testSubject); !strings.Contains(dev[0].Command, `"issuer":"http://127.0.0.1:8080",`) {
		t.Fatalf("development: %s", dev[0].Command)
	}
}
