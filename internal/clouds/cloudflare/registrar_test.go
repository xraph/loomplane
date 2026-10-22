package cloudflare_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.DomainRegistrar = (*cloudflare.Client)(nil)

const (
	registrarToken = "secret-token"
	contactEmail   = "ada@contact.invalid"
	contactPhone   = "+1.5550100"
)

// registrarFixture is a Cloudflare registrar. Requests run one at a time,
// so the fields need no lock. It fails the test on any path outside the
// account's registrar (or sandbox) API and on any path naming a transfer.
type registrarFixture struct {
	t      *testing.T
	mu     sync.Mutex // held while serving; register waits on it so a dropped connection's handler is done
	client *cloudflare.Client
	cred   provider.InfrastructureCredential
	url    string
	prefix string

	selfLink    string // overrides links.self in a registration answer
	noLink      bool
	postStatus  int    // answer to POST /registrations, default 202
	postState   string // default in_progress
	postRaw     string // replaces the whole POST answer
	postDrop    bool   // closes the connection without answering
	redirectTo  string // Location header on a failed POST answer
	statusCode  int    // answer to registration-status, default 200
	statusName  string // domain_name in registration-status, default the requested one
	statusState string // default succeeded
	patchStatus int    // answer to PATCH /registrations/{name}, default 202
	updateSteps []string
	autoRenew   bool // what the registration reports
	checkRaw    string
	extPages    func(cursor string) (tlds []string, next string)

	posts, patches, polls, extCalls int
	lastBody                        map[string]any
	lastQuery                       map[string][]string
	paths                           []string
}

func newRegistrarFixture(t *testing.T) *registrarFixture {
	t.Helper()
	return newRegistrarFixtureMode(t, false)
}

func newRegistrarFixtureMode(t *testing.T, sandbox bool) *registrarFixture {
	t.Helper()
	f := &registrarFixture{t: t, prefix: "/accounts/acct/registrar"}
	if sandbox {
		f.prefix = "/accounts/acct/registrar-sandbox"
	}
	api := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(api.Close)
	f.url = api.URL
	f.client = cloudflare.New(api.URL, sandbox)
	f.cred = provider.InfrastructureCredential{Provider: "cloudflare", AccountID: "acct", Token: registrarToken}
	return f
}

func (f *registrarFixture) self(name string) string {
	switch {
	case f.noLink:
		return ""
	case f.selfLink != "":
		return f.selfLink
	}
	return f.prefix + "/registrations/" + name + "/registration-status"
}

func (f *registrarFixture) workflow(name, state string, completed bool) map[string]any {
	out := map[string]any{"domain_name": name, "state": state, "completed": completed}
	if state == "failed" {
		out["error"] = map[string]any{"code": "domain_unavailable", "message": "taken"}
	}
	if completed {
		out["context"] = map[string]any{"registration": map[string]any{"domain_name": name, "status": "active", "expires_at": "2027-10-27T10:00:00Z", "auto_renew": f.autoRenew, "privacy_mode": "redaction", "locked": true}}
	}
	if link := f.self(name); link != "" {
		out["links"] = map[string]any{"self": link, "resource": f.prefix + "/registrations/" + name}
	}
	return out
}

func (f *registrarFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	if strings.Contains(r.URL.Path, "transfer") {
		f.t.Errorf("registrar touched a transfer path: %s", r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer "+registrarToken {
		f.t.Error("authorization missing")
	}
	rest, ok := strings.CutPrefix(r.URL.Path, f.prefix+"/")
	if !ok {
		f.t.Errorf("request outside %s: %s", f.prefix, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	f.lastQuery = r.URL.Query()
	reply := func(code int, result any, info map[string]any) {
		w.WriteHeader(code)
		body := map[string]any{"success": true, "errors": []any{}, "result": result}
		if info != nil {
			body["result_info"] = info
		}
		_ = json.NewEncoder(w).Encode(body)
	}
	fail := func(code int) {
		if f.redirectTo != "" {
			w.Header().Set("Location", f.redirectTo)
		}
		w.WriteHeader(code)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"secret-token `+contactEmail+`"}],"result":null}`)
	}
	switch {
	case r.Method == http.MethodGet && rest == "extensions":
		f.extCalls++
		cursor := r.URL.Query().Get("cursor")
		if r.URL.Query().Get("per_page") != "50" {
			f.t.Errorf("per_page %q", r.URL.Query().Get("per_page"))
		}
		tlds, next := f.extPages(cursor)
		items := []any{}
		for _, tld := range tlds {
			items = append(items, map[string]any{"metadata": map[string]any{"tld": tld}})
		}
		reply(200, items, map[string]any{"cursor": next})
	case r.Method == http.MethodGet && rest == "domain-search":
		reply(200, map[string]any{"domains": []any{map[string]any{"name": "acme.dev", "registrable": true, "tier": "standard", "pricing": map[string]any{"currency": "USD", "registration_cost": "10.11", "renewal_cost": "10.11"}}}}, nil)
	case r.Method == http.MethodPost && rest == "domain-check":
		_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
		w.WriteHeader(200)
		fmt.Fprint(w, f.checkRaw)
	case r.Method == http.MethodPost && rest == "registrations":
		f.posts++
		f.lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
		if f.postDrop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		if f.postRaw != "" {
			w.WriteHeader(f.postStatus)
			fmt.Fprint(w, f.postRaw)
			return
		}
		code := f.postStatus
		if code == 0 {
			code = 202
		}
		if code >= 300 {
			fail(code)
			return
		}
		state := f.postState
		if state == "" {
			state = "in_progress"
		}
		reply(code, f.workflow("acme.dev", state, state == "succeeded"), nil)
	case r.Method == http.MethodGet && strings.HasSuffix(rest, "/registration-status"):
		if f.statusCode >= 300 {
			fail(f.statusCode)
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(rest, "registrations/"), "/registration-status")
		state := f.statusState
		if state == "" {
			state = "succeeded"
		}
		if f.statusName != "" {
			name = f.statusName
		}
		reply(200, f.workflow(name, state, state == "succeeded"), nil)
	case r.Method == http.MethodGet && strings.HasSuffix(rest, "/update-status"):
		f.polls++
		state := "in_progress"
		if f.polls <= len(f.updateSteps) {
			state = f.updateSteps[f.polls-1]
		} else if len(f.updateSteps) > 0 {
			state = f.updateSteps[len(f.updateSteps)-1]
		}
		reply(200, map[string]any{"domain_name": "acme.dev", "state": state, "completed": state == "succeeded"}, nil)
	case r.Method == http.MethodPatch && strings.HasPrefix(rest, "registrations/"):
		f.patches++
		_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
		if v, ok := f.lastBody["auto_renew"].(bool); ok {
			f.autoRenew = v
		}
		code := f.patchStatus
		if code == 0 {
			code = 202
		}
		if code >= 300 {
			fail(code)
			return
		}
		reply(code, map[string]any{"domain_name": "acme.dev", "state": "in_progress", "completed": false}, nil)
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "registrations/"):
		name := strings.TrimPrefix(rest, "registrations/")
		if f.statusName != "" {
			name = f.statusName
		}
		reply(200, map[string]any{"domain_name": name, "status": "active", "expires_at": "2027-10-27T10:00:00Z", "auto_renew": f.autoRenew, "privacy_mode": "redaction", "locked": true}, nil)
	default:
		f.t.Errorf("unexpected API call: %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (f *registrarFixture) register(spec provider.DomainRegistrationSpec) (provider.DomainRegistrationResult, error) {
	res, err := f.client.RegisterDomain(f.t.Context(), f.cred, spec)
	f.settle()
	return res, err
}

// settle waits for the handler, which may outlive a dropped connection.
func (f *registrarFixture) settle() {
	f.mu.Lock()
	defer f.mu.Unlock()
}

func noLeak(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{registrarToken, contactEmail, contactPhone, "Ada Lovelace"} {
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
}

func TestRegisterSendsAutoRenewOnAndAcceptsAsync(t *testing.T) {
	f := newRegistrarFixture(t)
	res, err := f.register(provider.DomainRegistrationSpec{OperationID: "reg_1", Name: "acme.dev", AutoRenew: true, PrivacyMode: "redaction"})
	if err != nil || res.State != "in_progress" || res.Completed || f.lastBody["auto_renew"] != true || f.lastBody["privacy_mode"] != "redaction" || f.lastBody["domain_name"] != "acme.dev" {
		t.Fatalf("%+v %v %v", res, err, f.lastBody)
	}
	if _, ok := f.lastBody["contacts"]; ok {
		t.Fatal("default contact replaced")
	}
	if res.Reconciled || res.Name != "acme.dev" || f.posts != 1 {
		t.Fatalf("%+v posts=%d", res, f.posts)
	}
}

func TestRegisterAcceptsSynchronousCompletion(t *testing.T) {
	f := newRegistrarFixture(t)
	f.postStatus, f.postState, f.autoRenew = http.StatusCreated, "succeeded", true
	res, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev", AutoRenew: true})
	want := time.Date(2027, 10, 27, 10, 0, 0, 0, time.UTC)
	if err != nil || res.State != provider.RegistrationSucceeded || !res.Completed || !res.ExpiresAt.Equal(want) || !res.AutoRenew || res.PrivacyMode != "redaction" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRegisterSendsRegistrantOnlyWhenGiven(t *testing.T) {
	f := newRegistrarFixture(t)
	contact := &provider.RegistrantContact{Email: contactEmail, Phone: contactPhone, Name: "Ada Lovelace", Organization: "Example Inc", Street: "1 Main St", City: "Austin", State: "TX", PostalCode: "78701", CountryCode: "US"}
	if _, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev", Registrant: contact}); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(f.lastBody["contacts"])
	want := `{"registrant":{"email":"ada@contact.invalid","phone":"+1.5550100","postal_info":{"address":{"city":"Austin","country_code":"US","postal_code":"78701","state":"TX","street":"1 Main St"},"name":"Ada Lovelace","organization":"Example Inc"}}}`
	if string(data) != want {
		t.Fatalf("contacts %s", data)
	}
}

func TestRegisterRefusesForeignStatusLink(t *testing.T) {
	f := newRegistrarFixture(t)
	f.selfLink = "https://evil.example/status"
	_, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev"})
	if !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
	// The registration may exist, so a caller must reconcile, not post again.
	if !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatalf("a foreign link hid that the POST was sent: %v", err)
	}
	for _, link := range []string{
		"/accounts/acct/registrar/registrations/other.dev/registration-status",
		"/accounts/acct/registrar-sandbox/registrations/acme.dev/registration-status",
		"/accounts/acct/registrar/registrations/acme.dev/registration-status/",
		"/accounts/acct/registrar/registrations/acme.dev/update-status",
		"https://evil.example/accounts/acct/registrar/registrations/acme.dev/registration-status",
		f.url + "x/accounts/acct/registrar/registrations/acme.dev/registration-status",
		f.url + "/accounts/acct/registrar-sandbox/registrations/acme.dev/registration-status",
	} {
		f.selfLink = link
		if _, err = f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); !errors.Is(err, provider.ErrInfrastructureIdentity) {
			t.Fatalf("%s accepted: %v", link, err)
		}
	}
	f.noLink = true
	if _, err = f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("no link accepted: %v", err)
	}
}

func TestRegisterAcceptsOnlyTheClientsOwnAbsoluteStatusLink(t *testing.T) {
	f := newRegistrarFixture(t)
	f.selfLink = f.url + "/accounts/acct/registrar/registrations/acme.dev/registration-status"
	if res, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); err != nil || res.State != "in_progress" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSandboxRegistrationLinksUseTheSandboxPrefix(t *testing.T) {
	f := newRegistrarFixtureMode(t, true)
	if res, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); err != nil || res.State != "in_progress" {
		t.Fatalf("%+v %v", res, err)
	}
	f.selfLink = "/accounts/acct/registrar/registrations/acme.dev/registration-status"
	if _, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("production link accepted in sandbox: %v", err)
	}
}

func TestRegisterRefusesAnotherDomainAndUnknownStates(t *testing.T) {
	f := newRegistrarFixture(t)
	f.postRaw, f.postStatus = `{"success":true,"errors":[],"result":{"domain_name":"other.dev","state":"in_progress","links":{"self":"/accounts/acct/registrar/registrations/acme.dev/registration-status"}}}`, 202
	if _, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); !errors.Is(err, provider.ErrInfrastructureIdentity) || !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatal(err)
	}
	f.postRaw = `{"success":true,"errors":[],"result":{"domain_name":"acme.dev","state":"bogus","links":{"self":"/accounts/acct/registrar/registrations/acme.dev/registration-status"}}}`
	if _, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatal(err)
	}
}

func TestRegisterUncertainOutcomesAreAmbiguousAndLeakNothing(t *testing.T) {
	contact := &provider.RegistrantContact{Email: contactEmail, Phone: contactPhone, Name: "Ada Lovelace"}
	for name, set := range map[string]func(*registrarFixture){
		"server error":   func(f *registrarFixture) { f.postStatus = 500 },
		"bad gateway":    func(f *registrarFixture) { f.postStatus = 502 },
		"conflict":       func(f *registrarFixture) { f.postStatus = 409 },
		"see other":      func(f *registrarFixture) { f.postStatus, f.redirectTo = 303, f.url+"/elsewhere" },
		"temporary":      func(f *registrarFixture) { f.postStatus, f.redirectTo = 307, f.url+"/elsewhere" },
		"permanent":      func(f *registrarFixture) { f.postStatus, f.redirectTo = 308, "https://evil.example/registrations" },
		"dropped":        func(f *registrarFixture) { f.postDrop = true },
		"undecodable":    func(f *registrarFixture) { f.postStatus, f.postRaw = 202, `{"success":true,"result":"`+contactEmail },
		"result missing": func(f *registrarFixture) { f.postStatus, f.postRaw = 201, `{"success":true,"errors":[],"result":null}` },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRegistrarFixture(t)
			set(f)
			_, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev", Registrant: contact})
			if !errors.Is(err, provider.ErrInfrastructureAmbiguous) || f.posts != 1 {
				t.Fatalf("%v posts=%d", err, f.posts)
			}
			noLeak(t, err)
		})
	}
}

func TestRegisterDefiniteRefusalsAreNotAmbiguous(t *testing.T) {
	for _, code := range []int{400, 401, 402, 403, 404, 422, 429} {
		f := newRegistrarFixture(t)
		f.postStatus = code
		_, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev", Registrant: &provider.RegistrantContact{Email: contactEmail}})
		if err == nil || errors.Is(err, provider.ErrInfrastructureAmbiguous) || f.posts != 1 {
			t.Fatalf("%d: %v posts=%d", code, err, f.posts)
		}
		noLeak(t, err)
		if code == 403 && !errors.Is(err, provider.ErrInfrastructureScope) {
			t.Fatalf("403: %v", err)
		}
	}
}

func TestRegisterSendsNothingForARequestThatCannotBeValid(t *testing.T) {
	f := newRegistrarFixture(t)
	for _, name := range []string{"", "dev", "Acme.dev", "acme..dev", "acme.dev/../x", "*.acme.dev", "acme.dev ", "-a.dev", strings.Repeat("a", 64) + ".dev"} {
		_, err := f.register(provider.DomainRegistrationSpec{Name: name})
		if !errors.Is(err, provider.ErrInfrastructureInvalid) || errors.Is(err, provider.ErrInfrastructureAmbiguous) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	bad := f.cred
	bad.AccountID = ""
	if _, err := f.client.RegisterDomain(t.Context(), bad, provider.DomainRegistrationSpec{Name: "acme.dev"}); !errors.Is(err, provider.ErrInfrastructureInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.client.RegisterDomain(ctx, f.cred, provider.DomainRegistrationSpec{Name: "acme.dev"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatal(err)
	}
	if len(f.paths) != 0 {
		t.Fatalf("requests were sent: %v", f.paths)
	}
}

func TestReconcileOnlyNeverPosts(t *testing.T) {
	f := newRegistrarFixture(t)
	f.statusCode = http.StatusNotFound
	_, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev", ReconcileOnly: true})
	if !errors.Is(err, provider.ErrInfrastructureAmbiguous) || f.posts != 0 {
		t.Fatalf("%v posts=%d", err, f.posts)
	}
	f.statusCode = 0
	res, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev", ReconcileOnly: true, AutoRenew: true})
	if err != nil || !res.Reconciled || res.State != provider.RegistrationSucceeded || !res.Completed || f.posts != 0 {
		t.Fatalf("%+v %v posts=%d", res, err, f.posts)
	}
	f.statusCode = 500
	if _, err = f.register(provider.DomainRegistrationSpec{Name: "acme.dev", ReconcileOnly: true}); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || f.posts != 0 {
		t.Fatalf("%v posts=%d", err, f.posts)
	}
	// A refused or revoked token, or a cancelled read, says nothing about
	// whether the earlier POST landed.
	f.statusCode = 403
	if _, err = f.register(provider.DomainRegistrationSpec{Name: "acme.dev", ReconcileOnly: true}); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || !errors.Is(err, provider.ErrInfrastructureScope) || f.posts != 0 {
		t.Fatalf("403: %v posts=%d", err, f.posts)
	}
	f.statusCode = 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = f.client.RegisterDomain(ctx, f.cred, provider.DomainRegistrationSpec{Name: "acme.dev", ReconcileOnly: true}); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || !errors.Is(err, context.Canceled) || f.posts != 0 {
		t.Fatalf("cancelled: %v posts=%d", err, f.posts)
	}
	// Validation before anything is sent stays a plain refusal.
	if _, err = f.register(provider.DomainRegistrationSpec{Name: "Acme", ReconcileOnly: true}); !errors.Is(err, provider.ErrInfrastructureInvalid) || errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatal(err)
	}
}

func TestRegistrationStatusReportsFailureAndRefusesAnotherName(t *testing.T) {
	f := newRegistrarFixture(t)
	f.statusState = "failed"
	res, err := f.client.RegistrationStatus(t.Context(), f.cred, "acme.dev")
	if err != nil || res.State != provider.RegistrationFailed || res.ErrorCode != "domain_unavailable" || res.ErrorMessage != "taken" || res.Completed {
		t.Fatalf("%+v %v", res, err)
	}
	f.statusName = "other.dev"
	if _, err = f.client.RegistrationStatus(t.Context(), f.cred, "acme.dev"); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
	f.statusName, f.statusCode = "", 404
	if _, err = f.client.RegistrationStatus(t.Context(), f.cred, "acme.dev"); !errors.Is(err, provider.ErrInfrastructureNotFound) {
		t.Fatal(err)
	}
}

func TestGetRegisteredDomain(t *testing.T) {
	f := newRegistrarFixture(t)
	f.autoRenew = true
	got, err := f.client.GetRegisteredDomain(t.Context(), f.cred, "acme.dev")
	want := provider.RegisteredDomain{Name: "acme.dev", Status: "active", ExpiresAt: time.Date(2027, 10, 27, 10, 0, 0, 0, time.UTC), AutoRenew: true, PrivacyMode: "redaction", Locked: true}
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	f.statusName = "other.dev"
	if _, err = f.client.GetRegisteredDomain(t.Context(), f.cred, "acme.dev"); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
}

func TestSetAutoRenewPollsUpdateStatus(t *testing.T) {
	f := newRegistrarFixture(t)
	cloudflare.SetRegistrarPollInterval(f.client, time.Millisecond)
	f.updateSteps = []string{"in_progress", "in_progress", "succeeded"}
	got, err := f.client.SetAutoRenew(t.Context(), f.cred, "acme.dev", true)
	if err != nil || !got.AutoRenew || f.polls != 3 || f.patches != 1 || f.lastBody["auto_renew"] != true {
		t.Fatalf("%+v %v polls=%d patches=%d body=%v", got, err, f.polls, f.patches, f.lastBody)
	}
}

func TestSetAutoRenewGivesUpAfterTenPolls(t *testing.T) {
	f := newRegistrarFixture(t)
	cloudflare.SetRegistrarPollInterval(f.client, time.Millisecond)
	f.updateSteps = []string{"in_progress"}
	if _, err := f.client.SetAutoRenew(t.Context(), f.cred, "acme.dev", false); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || f.polls != 10 {
		t.Fatalf("%v polls=%d", err, f.polls)
	}
}

func TestSetAutoRenewReportsAFailedUpdate(t *testing.T) {
	f := newRegistrarFixture(t)
	cloudflare.SetRegistrarPollInterval(f.client, time.Millisecond)
	for _, state := range []string{"failed", "blocked", "action_required"} {
		f.polls, f.updateSteps = 0, []string{"in_progress", state}
		if _, err := f.client.SetAutoRenew(t.Context(), f.cred, "acme.dev", true); err == nil || f.polls != 2 {
			t.Fatalf("%s: %v polls=%d", state, err, f.polls)
		}
	}
}

func TestSetAutoRenewStopsPollingWhenCancelled(t *testing.T) {
	f := newRegistrarFixture(t)
	cloudflare.SetRegistrarPollInterval(f.client, time.Hour)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.client.SetAutoRenew(ctx, f.cred, "acme.dev", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestSetAutoRenewWithoutAWorkflowReadsTheDomainBack(t *testing.T) {
	f := newRegistrarFixture(t)
	f.patchStatus = 200
	got, err := f.client.SetAutoRenew(t.Context(), f.cred, "acme.dev", true)
	if err != nil || !got.AutoRenew || f.polls != 0 {
		t.Fatalf("%+v %v polls=%d", got, err, f.polls)
	}
}

func TestCheckDomainsKeepsReasonsAndExactPrices(t *testing.T) {
	f := newRegistrarFixture(t)
	f.checkRaw = `{"success":true,"errors":[],"result":{"domains":[
		{"name":"a.dev","registrable":true,"tier":"standard","pricing":{"currency":"USD","registration_cost":10.11,"renewal_cost":"10.11"}},
		{"name":"b.dev","registrable":true,"tier":"premium","pricing":{"currency":"USD","registration_cost":"10.11","renewal_cost":12.50}},
		{"name":"c.uk","registrable":false,"reason":"extension_not_supported_via_api"}]}}`
	offers, err := f.client.CheckDomains(t.Context(), f.cred, []string{"a.dev", "b.dev", "c.uk"})
	if err != nil || len(offers) != 3 {
		t.Fatalf("%+v %v", offers, err)
	}
	if offers[0].Price == nil || *offers[0].Price != (provider.DomainPrice{Currency: "USD", RegistrationCost: "10.11", RenewalCost: "10.11"}) || offers[0].Tier != "standard" || !offers[0].Registrable {
		t.Fatalf("%+v", offers[0])
	}
	if offers[1].Price == nil || offers[1].Price.RegistrationCost != "10.11" || offers[1].Price.RenewalCost != "12.50" || offers[1].Tier != "premium" {
		t.Fatalf("%+v", offers[1])
	}
	if offers[2].Registrable || offers[2].Reason != "extension_not_supported_via_api" || offers[2].Price != nil || offers[2].Name != "c.uk" {
		t.Fatalf("%+v", offers[2])
	}
	if got, _ := json.Marshal(f.lastBody); string(got) != `{"domains":["a.dev","b.dev","c.uk"]}` {
		t.Fatalf("body %s", got)
	}
}

func TestCheckDomainsRefusesWhatItCannotTrust(t *testing.T) {
	f := newRegistrarFixture(t)
	for name, raw := range map[string]string{
		"exponent":        `{"success":true,"result":{"domains":[{"name":"a.dev","registrable":true,"pricing":{"currency":"USD","registration_cost":1e1}}]}}`,
		"negative":        `{"success":true,"result":{"domains":[{"name":"a.dev","registrable":true,"pricing":{"currency":"USD","registration_cost":"-1"}}]}}`,
		"words":           `{"success":true,"result":{"domains":[{"name":"a.dev","registrable":true,"pricing":{"currency":"USD","registration_cost":"free"}}]}}`,
		"another name":    `{"success":true,"result":{"domains":[{"name":"z.dev","registrable":true}]}}`,
		"empty name":      `{"success":true,"result":{"domains":[{"name":"","registrable":true}]}}`,
		"null cost value": `{"success":true,"result":{"domains":[{"name":"a.dev","registrable":true,"pricing":{"currency":"USD","registration_cost":{}}}]}}`,
	} {
		f.checkRaw = raw
		if offers, err := f.client.CheckDomains(t.Context(), f.cred, []string{"a.dev"}); err == nil {
			t.Fatalf("%s accepted: %+v", name, offers)
		}
	}
	// A quote without a registration cost is not a price.
	f.checkRaw = `{"success":true,"result":{"domains":[{"name":"a.dev","registrable":true,"pricing":{"currency":"USD"}}]}}`
	offers, err := f.client.CheckDomains(t.Context(), f.cred, []string{"a.dev"})
	if err != nil || len(offers) != 1 || offers[0].Price != nil {
		t.Fatalf("%+v %v", offers, err)
	}
}

func TestCheckDomainsCountsAndNames(t *testing.T) {
	f := newRegistrarFixture(t)
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("a%d.dev", i)
	}
	for _, names := range [][]string{nil, {}, many, {"Acme.dev"}, {"acme"}, {"a b.dev"}} {
		if _, err := f.client.CheckDomains(t.Context(), f.cred, names); !errors.Is(err, provider.ErrInfrastructureInvalid) {
			t.Fatalf("%v: %v", names, err)
		}
	}
	if len(f.paths) != 0 {
		t.Fatalf("requests were sent: %v", f.paths)
	}
}

func TestSearchDomainsClampsLimit(t *testing.T) {
	f := newRegistrarFixture(t)
	for limit, want := range map[int]string{-3: "1", 0: "1", 5: "5", 20: "20", 99: "20"} {
		offers, err := f.client.SearchDomains(t.Context(), f.cred, "acme corp", limit)
		if err != nil || len(offers) != 1 || offers[0].Name != "acme.dev" || offers[0].Price == nil {
			t.Fatalf("%+v %v", offers, err)
		}
		if got := f.lastQuery["limit"]; len(got) != 1 || got[0] != want || f.lastQuery["q"][0] != "acme corp" {
			t.Fatalf("limit %d sent %v", limit, f.lastQuery)
		}
	}
	for _, q := range []string{"", "   ", strings.Repeat("a", 300)} {
		if _, err := f.client.SearchDomains(t.Context(), f.cred, q, 5); !errors.Is(err, provider.ErrInfrastructureInvalid) {
			t.Fatalf("%q: %v", q, err)
		}
	}
}

func TestExtensionsFollowCursor(t *testing.T) {
	f := newRegistrarFixture(t)
	var cursors []string
	f.extPages = func(cursor string) ([]string, string) {
		cursors = append(cursors, cursor)
		if cursor == "" {
			return []string{"net", "com"}, "c2"
		}
		return []string{"dev", "com"}, ""
	}
	got, err := f.client.RegistrationExtensions(t.Context(), f.cred)
	if err != nil || strings.Join(got, ",") != "com,dev,net" || strings.Join(cursors, "|") != "|c2" {
		t.Fatalf("%v %v cursors=%q", got, err, cursors)
	}
}

func TestExtensionsRefuseARepeatedCursorAndRunawayPaging(t *testing.T) {
	f := newRegistrarFixture(t)
	f.extPages = func(string) ([]string, string) { return []string{"com"}, "c2" }
	if _, err := f.client.RegistrationExtensions(t.Context(), f.cred); !errors.Is(err, provider.ErrInfrastructurePagination) {
		t.Fatal(err)
	}
	f.extCalls = 0
	f.extPages = func(cursor string) ([]string, string) { return []string{"com"}, cursor + "x" }
	if _, err := f.client.RegistrationExtensions(t.Context(), f.cred); !errors.Is(err, provider.ErrInfrastructurePagination) || f.extCalls != 100 {
		t.Fatalf("%v calls=%d", err, f.extCalls)
	}
	f.extPages = func(string) ([]string, string) { return []string{"Not A TLD"}, "" }
	if _, err := f.client.RegistrationExtensions(t.Context(), f.cred); err == nil {
		t.Fatal("a malformed extension was accepted")
	}
}

func TestRegistrarSandboxPrefix(t *testing.T) {
	f := newRegistrarFixtureMode(t, true)
	f.extPages = func(string) ([]string, string) { return []string{"dev"}, "" }
	f.checkRaw = `{"success":true,"result":{"domains":[{"name":"a.dev","registrable":false,"reason":"domain_unavailable"}]}}`
	ctx := t.Context()
	if _, err := f.client.RegistrationExtensions(ctx, f.cred); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.SearchDomains(ctx, f.cred, "acme", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.CheckDomains(ctx, f.cred, []string{"a.dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.register(provider.DomainRegistrationSpec{Name: "acme.dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.RegistrationStatus(ctx, f.cred, "acme.dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.GetRegisteredDomain(ctx, f.cred, "acme.dev"); err != nil {
		t.Fatal(err)
	}
	cloudflare.SetRegistrarPollInterval(f.client, time.Millisecond)
	f.updateSteps = []string{"succeeded"}
	if _, err := f.client.SetAutoRenew(ctx, f.cred, "acme.dev", true); err != nil {
		t.Fatal(err)
	}
	if len(f.paths) != 9 {
		t.Fatalf("%v", f.paths)
	}
	for _, p := range f.paths {
		if !strings.Contains(p, " /accounts/acct/registrar-sandbox/") {
			t.Fatalf("not in the sandbox: %s", p)
		}
	}
}

func TestRegistrarNeverTouchesTransfers(t *testing.T) {
	// serve fails the test on any path containing "transfer".
	f := newRegistrarFixture(t)
	f.extPages = func(string) ([]string, string) { return []string{"dev"}, "" }
	f.checkRaw = `{"success":true,"result":{"domains":[]}}`
	cloudflare.SetRegistrarPollInterval(f.client, time.Millisecond)
	f.updateSteps = []string{"succeeded"}
	ctx := t.Context()
	_, _ = f.client.RegistrationExtensions(ctx, f.cred)
	_, _ = f.client.SearchDomains(ctx, f.cred, "acme", 3)
	_, _ = f.client.CheckDomains(ctx, f.cred, []string{"acme.dev"})
	_, _ = f.register(provider.DomainRegistrationSpec{Name: "acme.dev"})
	_, _ = f.register(provider.DomainRegistrationSpec{Name: "acme.dev", ReconcileOnly: true})
	_, _ = f.client.GetRegisteredDomain(ctx, f.cred, "acme.dev")
	_, _ = f.client.SetAutoRenew(ctx, f.cred, "acme.dev", true)
}

func TestRegistrarReadsRefuseAnotherProvidersCredential(t *testing.T) {
	f := newRegistrarFixture(t)
	other := f.cred
	other.Provider = "vercel"
	if _, err := f.client.RegistrationExtensions(t.Context(), other); !errors.Is(err, provider.ErrInfrastructureInvalid) {
		t.Fatal(err)
	}
	if _, err := f.client.RegistrationStatus(t.Context(), other, "acme.dev"); !errors.Is(err, provider.ErrInfrastructureInvalid) {
		t.Fatal(err)
	}
	if len(f.paths) != 0 {
		t.Fatalf("requests were sent: %v", f.paths)
	}
}
