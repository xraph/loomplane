package connections

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/clouds/keyless"
)

// keylessApp is the app registration whose federated credential trusts
// Loomplane. It isn't the fake's service principal.
const keylessApp = "55555555-5555-5555-5555-555555555555"

// fakeAzureTrust is the azuretest fake, whose token endpoint also takes a
// minted token as keylessApp's client assertion and answers with the
// fake's own access token, so Resource Manager takes it as a
// Contributor's. Entra is down while down is set, and finds no matching
// federated credential while refuse is.
type fakeAzureTrust struct {
	mu         sync.Mutex
	assertions []string
	down       bool
	refuse     bool
}

func (f *fakeAzureTrust) assertionsSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.assertions...)
}

func (f *fakeAzureTrust) set(down, refuse bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down, f.refuse = down, refuse
}

func newAzureKeylessRig(t *testing.T) (*keylessRig, *fakeAzureTrust) {
	t.Helper()
	fake := azuretest.New(t)
	trust := &fakeAzureTrust{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+azuretest.Tenant+"/oauth2/v2.0/token" {
			fake.Server.Config.Handler.ServeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form := r.PostForm
		if !form.Has("client_assertion") {
			fake.Server.Config.Handler.ServeHTTP(w, r)
			return
		}
		trust.mu.Lock()
		defer trust.mu.Unlock()
		trust.assertions = append(trust.assertions, form.Get("client_assertion"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case trust.down:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable","error_description":"AADSTS50000: There was an error issuing a token.","error_codes":[50000]}`))
		case trust.refuse || form.Get("client_id") != keylessApp ||
			form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" ||
			!strings.HasPrefix(form.Get("client_assertion"), "minted-for org:"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"AADSTS70021: No matching federated identity record found for presented assertion.","error_codes":[70021]}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"fake-token","expires_in":3600}`))
		}
	}))
	t.Cleanup(srv.Close)
	client := azure.New(srv.URL).WithMinter(ticketMinter{})
	r := newKeylessRig(t)
	r.s.checkers[Azure] = AzureChecker{Client: client}
	r.s.KeylessClouds[Azure] = client
	return r, trust
}

// A service principal and an app's federated credential reach the same
// tenant, subscription and resource group, so a connection moves to
// keyless and keeps its ID, and every setting a session needs is in the
// handout a deployment gets.
func TestAzureKeylessLandsOnTheSameIdentityAsAPrincipal(t *testing.T) {
	r, trust := newAzureKeylessRig(t)
	settings := azuretest.Settings()
	settings["resource_group"] = "shop-prod"
	c, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: Azure, Name: "Acme", Credential: azuretest.Secret, Settings: settings})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Account != "Acme production" || c.Details["client_id"] != azuretest.Client {
		t.Fatalf("principal connection: %+v", c)
	}

	moving := maps.Clone(settings)
	moving["client_id"] = keylessApp
	got, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, moving)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got.ID != c.ID || got.Method != MethodKeyless || got.Status != StatusReady || got.Account != c.Account || len(got.Missing) != 0 ||
		AccountIdentity(Azure, got.Account, got.Details) != AccountIdentity(Azure, c.Account, c.Details) {
		t.Fatalf("keyless connection: %+v", got)
	}
	for _, k := range []string{"tenant_id", "client_id", "subscription_id", "resource_group"} {
		if got.Details[k] != moving[k] {
			t.Errorf("details lost %s: %q", k, got.Details[k])
		}
	}
	subject := keyless.Subject("org", c.ID)
	if sent := trust.assertionsSent(); len(sent) != 1 || sent[0] != "minted-for "+subject {
		t.Fatalf("sent %d assertions, the minted one: %v", len(sent), len(sent) == 1 && sent[0] == "minted-for "+subject)
	}

	// A deployment's handout carries every setting a session needs, and
	// parses as the app's credential.
	conn, cred, err := r.s.Credential(t.Context(), "org", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := keyless.Parse(cred)
	if h.Audience != "api://AzureADTokenExchange" || h.Subject != subject {
		t.Fatalf("handout: audience %q, subject %q", h.Audience, h.Subject)
	}
	for _, k := range []string{"tenant_id", "client_id", "subscription_id", "resource_group"} {
		if h.Settings[k] != moving[k] {
			t.Errorf("handout lost %s: %q", k, h.Settings[k])
		}
	}
	parsed, err := azure.ParseCredential(cred, conn.Details)
	if err != nil || !parsed.Keyless() || parsed.ClientID != keylessApp || parsed.TenantID != azuretest.Tenant ||
		parsed.SubscriptionID != azuretest.Subscription || parsed.ResourceGroup != "shop-prod" {
		t.Fatalf("handout credential: %v", err)
	}
	if again, err := r.s.Recheck(t.Context(), starter, c.ID); err != nil || again.Status != StatusReady || again.Details["client_id"] != keylessApp {
		t.Fatalf("recheck: %+v %v", again, err)
	}
	if setup, err := r.s.KeylessSetupFor(t.Context(), starter, c.ID); err != nil || setup.Audience != "api://AzureADTokenExchange" ||
		len(setup.Steps) != 2 || !strings.Contains(setup.Steps[0].Command, `"subject":"`+subject+`"`) {
		t.Fatalf("setup: %+v %v", setup, err)
	}

	// Another resource group isn't the same identity.
	elsewhere := maps.Clone(moving)
	elsewhere["resource_group"] = "shop-staging"
	if _, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, elsewhere); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other group: %v", err)
	}
	if !(AzureChecker{}).IsGrant(cred) {
		t.Fatal("a keyless handout isn't a grant")
	}
}

// Entra finding no federated credential that trusts Loomplane is a
// refusal, which fails the connection, and Entra being down is not.
func TestAzureKeylessRefusalIsRejectedAndAnOutageIsNot(t *testing.T) {
	r, trust := newAzureKeylessRig(t)
	settings := azuretest.Settings()
	settings["client_id"] = keylessApp
	handout, err := r.s.keylessHandout(t.Context(), &Connection{ID: "conn_1", OrgID: "org", Cloud: Azure, Details: settings})
	if err != nil {
		t.Fatal(err)
	}
	checker := r.s.checkers[Azure]
	if _, err = checker.Check(t.Context(), handout, settings); err != nil {
		t.Fatalf("trusted: %v", err)
	}
	trust.set(true, false)
	_, err = checker.Check(t.Context(), handout, settings)
	if err == nil || rejected(err) {
		t.Fatalf("outage: rejected %v", err != nil && rejected(err))
	}
	trust.set(false, true)
	_, err = checker.Check(t.Context(), handout, settings)
	if err == nil || !rejected(err) || refusedSignIn(err) || !strings.Contains(err.Error(), "AADSTS70021") {
		t.Fatalf("refusal: rejected %v", err != nil && rejected(err))
	}
	if strings.Contains(err.Error(), "minted-for") || strings.Contains(err.Error(), "ticket-for") || strings.Contains(err.Error(), "No matching") {
		t.Fatal("the refusal shows a secret or Entra's description")
	}

	// Without a minter nothing is asked, and it isn't a refusal.
	trust.set(false, false)
	before := len(trust.assertionsSent())
	_, err = AzureChecker{Client: azure.New("")}.Check(t.Context(), handout, settings)
	if !errors.Is(err, keyless.ErrNoMinter) || rejected(err) || len(trust.assertionsSent()) != before {
		t.Fatalf("no minter: %v", err)
	}
}
