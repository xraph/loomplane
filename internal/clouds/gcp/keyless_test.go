package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/keyless"
)

const (
	testProvider = "projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane"
	testAudience = "//iam.googleapis.com/" + testProvider
	testAccount  = "loomplane@shop-prod.iam.gserviceaccount.com"
	testTicket   = "ticket-only-the-issuer-makes"
	testSubject  = "org:org_1:connection:conn_1"
)

// keylessSecrets are what a keyless session must never show: the ticket,
// minted tokens and the tokens Google trades them for.
var keylessSecrets = []string{testTicket, "minted-", "sts-access-", "ya29.impersonated-"}

// fakeGoogle is Google's STS, IAM Credentials and Resource Manager. STS
// trades a token for sts-access-N, IAM Credentials trades that for
// ya29.impersonated-N, and Resource Manager grants every permission to
// the latest impersonated token. Each remembers what it was sent.
type fakeGoogle struct {
	srv *httptest.Server
	mu  sync.Mutex
	// expires is how long each token lasts, in seconds.
	expires    int
	exchanged  []url.Values
	bearers    []string
	asked      []string
	userProj   []string
	stsAnswer  func(w http.ResponseWriter) bool
	iamAnswer  func(w http.ResponseWriter) bool
	impersonee []string
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{expires: 3600}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/v1/token":
		_ = r.ParseForm()
		f.exchanged = append(f.exchanged, r.PostForm)
		if f.stsAnswer != nil && f.stsAnswer(w) {
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"sts-access-%d","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":%d}`, len(f.exchanged), f.expires)
	case strings.HasPrefix(r.URL.Path, "/v1/projects/-/serviceAccounts/") && strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
		f.bearers = append(f.bearers, r.Header.Get("Authorization"))
		f.impersonee = append(f.impersonee, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/projects/-/serviceAccounts/"), ":generateAccessToken"))
		if f.iamAnswer != nil && f.iamAnswer(w) {
			return
		}
		expiry := time.Now().Add(time.Duration(f.expires) * time.Second).UTC().Format(time.RFC3339)
		_, _ = fmt.Fprintf(w, `{"accessToken":"ya29.impersonated-%d","expireTime":%q}`, len(f.bearers), expiry)
	case r.URL.Path == "/v3/projects/shop-prod:testIamPermissions":
		f.asked = append(f.asked, r.Header.Get("Authorization"))
		f.userProj = append(f.userProj, r.Header.Get("X-Goog-User-Project"))
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer ya29.impersonated-%d", len(f.bearers)) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Request had invalid authentication credentials."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"permissions": Permissions})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"unknown"}}`))
	}
}

// answer makes a fake's endpoint answer status and body.
func answer(status int, body string) func(http.ResponseWriter) bool {
	return func(w http.ResponseWriter) bool {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
		return true
	}
}

// countingMinter mints minted-1, minted-2 and so on, for the test's
// handout only.
type countingMinter struct {
	mu       sync.Mutex
	minted   int
	deadline bool
	fail     error
}

func (m *countingMinter) Mint(ctx context.Context, h keyless.Handout) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.Ticket != testTicket || h.Subject != testSubject || h.Audience != testAudience {
		return "", errors.New("not a handout this test made")
	}
	if m.fail != nil {
		return "", m.fail
	}
	_, m.deadline = ctx.Deadline()
	m.minted++
	return fmt.Sprintf("minted-%d", m.minted), nil
}

var testKeylessSettings = map[string]string{"project_id": "shop-prod", "workload_provider": testProvider, "service_account": testAccount}

func testHandout(t *testing.T, settings map[string]string) []byte {
	t.Helper()
	out, err := json.Marshal(keyless.Handout{Type: keyless.Type, Subject: testSubject, Audience: testAudience, Ticket: testTicket, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testKeylessKey(t *testing.T) *Key {
	t.Helper()
	key, err := ParseKey(testHandout(t, testKeylessSettings))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return key
}

// noKeylessSecret fails if err shows a secret, without showing it.
func noKeylessSecret(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, secret := range keylessSecrets {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error shows a secret starting %.4q", what, secret)
		}
	}
}

func TestCheckWorkloadProvider(t *testing.T) {
	for _, ok := range []string{testProvider, "projects/1/locations/global/workloadIdentityPools/pool-1/providers/" + strings.Repeat("a", 32)} {
		if err := CheckWorkloadProvider(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "loomplane", "//iam.googleapis.com/" + testProvider, testProvider + "/",
		"projects/shop-prod/locations/global/workloadIdentityPools/loomplane/providers/loomplane",
		"projects/123456789012/locations/us-east1/workloadIdentityPools/loomplane/providers/loomplane",
		"projects/123456789012/locations/global/workloadIdentityPools/abc/providers/loomplane",
		"projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/Loomplane",
		"projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/" + strings.Repeat("a", 33),
		" " + testProvider, testProvider + "\n",
	} {
		if err := CheckWorkloadProvider(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestParseKeyReadsAKeylessHandout(t *testing.T) {
	key := testKeylessKey(t)
	if !key.Keyless || key.User || key.ProjectID != "shop-prod" || key.ClientEmail != testAccount || key.WorkloadProvider != testProvider {
		t.Fatalf("key: %+v", key)
	}
	plain, err := ParseKey([]byte(`{"type":"service_account","project_id":"shop-prod","client_email":"a@shop-prod.iam.gserviceaccount.com","private_key":"k"}`))
	if err != nil || plain.Keyless || plain.WorkloadProvider != "" {
		t.Fatalf("service account key: %+v %v", plain, err)
	}
	for name, settings := range map[string]map[string]string{
		"no project":     {"workload_provider": testProvider, "service_account": testAccount},
		"short project":  {"project_id": "shop", "workload_provider": testProvider, "service_account": testAccount},
		"no provider":    {"project_id": "shop-prod", "service_account": testAccount},
		"bad provider":   {"project_id": "shop-prod", "workload_provider": "loomplane", "service_account": testAccount},
		"no account":     {"project_id": "shop-prod", "workload_provider": testProvider},
		"a person":       {"project_id": "shop-prod", "workload_provider": testProvider, "service_account": "ada@example.com"},
		"path in email":  {"project_id": "shop-prod", "workload_provider": testProvider, "service_account": "a/b@shop-prod.iam.gserviceaccount.com"},
		"no settings":    nil,
		"query in email": {"project_id": "shop-prod", "workload_provider": testProvider, "service_account": "loomplane@shop-prod.iam.gserviceaccount.com?x=1"},
	} {
		_, err := ParseKey(testHandout(t, settings))
		if !errors.Is(err, ErrInvalidKey) || strings.Contains(err.Error(), testTicket) {
			t.Errorf("%s: invalid %v", name, errors.Is(err, ErrInvalidKey))
		}
	}
	broken := []byte(`{"type":"loomplane_keyless","subject":"s","audience":"a","ticket":"` + testTicket + `","settings":"nope"}`)
	if _, err := ParseKey(broken); !errors.Is(err, ErrInvalidKey) || strings.Contains(err.Error(), testTicket) {
		t.Errorf("broken handout: invalid %v", errors.Is(err, ErrInvalidKey))
	}
}

func TestKeylessSessionExchangesAMintedToken(t *testing.T) {
	g := newFakeGoogle(t)
	minter := &countingMinter{}
	base := New(g.srv.URL)
	s, err := base.WithMinter(minter).Session(t.Context(), testKeylessKey(t))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	missing, err := s.Missing(t.Context())
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing %v: %v", missing, err)
	}
	if minter.minted != 1 || !minter.deadline || len(g.exchanged) != 1 || len(g.bearers) != 1 {
		t.Fatalf("minted %d (deadline %v), exchanged %d, impersonated %d", minter.minted, minter.deadline, len(g.exchanged), len(g.bearers))
	}
	form := g.exchanged[0]
	if form.Get("subject_token") != "minted-1" {
		t.Fatal("STS wasn't sent the minted token")
	}
	if form.Get("audience") != testAudience || form.Get("subject_token_type") != "urn:ietf:params:oauth:token-type:jwt" ||
		form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
		t.Fatalf("exchange: audience %q, type %q, grant %q", form.Get("audience"), form.Get("subject_token_type"), form.Get("grant_type"))
	}
	if g.bearers[0] != "Bearer sts-access-1" || g.impersonee[0] != testAccount {
		t.Fatalf("impersonated %q with STS's token: %v", g.impersonee[0], g.bearers[0] == "Bearer sts-access-1")
	}
	if len(g.asked) != 1 || g.asked[0] != "Bearer ya29.impersonated-1" {
		t.Fatal("Resource Manager wasn't asked with the impersonated token")
	}
	if g.userProj[0] != "" {
		t.Fatalf("a service account's call billed to %q", g.userProj[0])
	}

	// The client WithMinter copied still has none, which is no refusal.
	s, err = base.Session(t.Context(), testKeylessKey(t))
	if err == nil {
		_, err = s.Missing(t.Context())
	}
	if !errors.Is(err, keyless.ErrNoMinter) || errors.Is(err, ErrRejected) || errors.Is(err, ErrInvalidKey) || len(g.exchanged) != 1 {
		t.Fatalf("no minter: %v, exchanged %d", err, len(g.exchanged))
	}
}

func TestKeylessSessionMintsAgainWhenTheTokenExpires(t *testing.T) {
	g := newFakeGoogle(t)
	g.expires = 1
	minter := &countingMinter{}
	s, err := New(g.srv.URL).WithMinter(minter).Session(t.Context(), testKeylessKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = s.Missing(t.Context()); err != nil {
			t.Fatalf("missing: %v", err)
		}
	}
	if minter.minted != 2 || len(g.exchanged) != 2 || len(g.bearers) != 2 {
		t.Fatalf("minted %d, exchanged %d, impersonated %d", minter.minted, len(g.exchanged), len(g.bearers))
	}
	if g.exchanged[1].Get("subject_token") != "minted-2" || g.bearers[1] != "Bearer sts-access-2" || g.asked[1] != "Bearer ya29.impersonated-2" {
		t.Fatal("the renewal didn't trade a fresh token")
	}

	// Tokens that last are kept, and nothing is minted for them.
	g.expires = 3600
	s, err = New(g.srv.URL).WithMinter(minter).Session(t.Context(), testKeylessKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = s.Missing(t.Context()); err != nil {
			t.Fatalf("missing: %v", err)
		}
	}
	if minter.minted != 3 || len(g.exchanged) != 3 {
		t.Fatalf("minted %d, exchanged %d", minter.minted, len(g.exchanged))
	}
}

// Only Google refusing the trust is ErrRejected: STS refusing the token,
// or IAM Credentials refusing the impersonation. Google failing, STS not
// reaching Loomplane's keys or a mint failing is something a retry may
// fix.
func TestKeylessRefusedTrustIsRejectedAndAFailureIsNot(t *testing.T) {
	for name, tc := range map[string]struct {
		sts, iam func(http.ResponseWriter) bool
		mint     error
		rejected bool
	}{
		"subject refused": {sts: answer(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"The given credential is rejected by the attribute condition."}`), rejected: true},
		"client refused":  {sts: answer(http.StatusBadRequest, `{"error":"unauthorized_client","error_description":"The workload identity pool provider is disabled."}`), rejected: true},
		"not impersonable": {iam: answer(http.StatusForbidden,
			`{"error":{"code":403,"message":"Permission 'iam.serviceAccounts.getAccessToken' denied on resource (or it may not exist).","status":"PERMISSION_DENIED"}}`), rejected: true},
		"sts down": {sts: answer(http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`)},
		"sts can't reach the issuer": {sts: answer(http.StatusBadRequest,
			`{"error":"invalid_grant","error_description":"Error connecting to the given credential's issuer."}`)},
		"iam down":    {iam: answer(http.StatusInternalServerError, `{"error":{"code":500,"message":"Internal error.","status":"INTERNAL"}}`)},
		"iam busy":    {iam: answer(http.StatusTooManyRequests, `{"error":{"code":429,"message":"Quota exceeded.","status":"RESOURCE_EXHAUSTED"}}`)},
		"mint failed": {mint: errors.New("the signing key is unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGoogle(t)
			g.stsAnswer, g.iamAnswer = tc.sts, tc.iam
			s, err := New(g.srv.URL).WithMinter(&countingMinter{fail: tc.mint}).Session(t.Context(), testKeylessKey(t))
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Missing(t.Context())
			if err == nil {
				t.Fatal("no error")
			}
			if errors.Is(err, ErrRejected) != tc.rejected || errors.Is(err, ErrInvalidKey) {
				t.Fatalf("rejected %v, invalid key %v: %v", errors.Is(err, ErrRejected), errors.Is(err, ErrInvalidKey), err)
			}
			noKeylessSecret(t, name, err)
			if tc.mint != nil && len(g.exchanged) != 0 {
				t.Fatal("STS was asked without a token")
			}
		})
	}
}

func TestKeylessSettingsTakeTheProviderAndServiceAccount(t *testing.T) {
	c := New("")
	in := map[string]string{
		"project_id": " shop-prod ", "workload_provider": " " + testProvider + " ", "service_account": testAccount,
		"signed_in_as": "ada@example.com", BillingExportSetting: "shop-prod.billing.gcp_billing_export_v1_0A1B2C",
		"cloud_sql_recovery_network": "projects/shop-prod/global/networks/recovery",
	}
	got, err := c.KeylessSettings(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"project_id": "shop-prod", "workload_provider": testProvider, "service_account": testAccount,
		BillingExportSetting: "shop-prod.billing.gcp_billing_export_v1_0A1B2C", "cloud_sql_recovery_network": "projects/shop-prod/global/networks/recovery",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("settings: %v", got)
	}
	for name, bad := range map[string]map[string]string{
		"no project":   {"workload_provider": testProvider, "service_account": testAccount},
		"no provider":  {"project_id": "shop-prod", "service_account": testAccount},
		"no account":   {"project_id": "shop-prod", "workload_provider": testProvider},
		"bad billing":  {"project_id": "shop-prod", "workload_provider": testProvider, "service_account": testAccount, BillingExportSetting: "x`; DROP"},
		"bad recovery": {"project_id": "shop-prod", "workload_provider": testProvider, "service_account": testAccount, "cloud_sql_recovery_network": "default"},
	} {
		if _, err := c.KeylessSettings(bad); err == nil {
			t.Errorf("%s passed", name)
		}
	}
	if got := c.KeylessAudience(want); got != testAudience {
		t.Fatal(got)
	}
}

func TestKeylessSetupNamesTheSubject(t *testing.T) {
	steps := New("").KeylessSetup("https://loomplane.example/oidc", testSubject)
	if len(steps) != 4 {
		t.Fatalf("steps: %d", len(steps))
	}
	want := []string{
		"gcloud iam workload-identity-pools create loomplane --location=global",
		"gcloud iam workload-identity-pools providers create-oidc loomplane --location=global --workload-identity-pool=loomplane " +
			"--issuer-uri=https://loomplane.example/oidc --attribute-mapping=google.subject=assertion.sub --attribute-condition=\"assertion.sub=='" + testSubject + "'\"",
		"gcloud iam service-accounts add-iam-policy-binding <service-account> --role=roles/iam.workloadIdentityUser " +
			"--member=principal://iam.googleapis.com/projects/<project-number>/locations/global/workloadIdentityPools/loomplane/subject/" + testSubject,
	}
	for i, command := range want {
		if steps[i].Command != command {
			t.Errorf("step %d:\n got %s\nwant %s", i+1, steps[i].Command, command)
		}
	}
	grant := steps[3].Command
	if !strings.Contains(grant, "--permissions="+strings.Join(Permissions, ",")) || !strings.Contains(grant, "--member=serviceAccount:<service-account>") {
		t.Errorf("grant: %s", grant)
	}
	for _, step := range steps {
		if step.Title == "" || step.Body == "" || strings.Contains(step.Title+step.Body+step.Command, "\u2014") {
			t.Errorf("step: %+v", step)
		}
	}
	if !slices.ContainsFunc(steps, func(s keyless.Step) bool {
		return strings.Contains(s.Body, "workload_provider") || strings.Contains(s.Body, "provider's full name")
	}) {
		t.Error("no step says what to paste")
	}
	// A trailing slash names the same issuer as the token's iss.
	if dev := New("").KeylessSetup("http://127.0.0.1:8080/", testSubject); !strings.Contains(dev[1].Command, "--issuer-uri=http://127.0.0.1:8080 ") {
		t.Fatalf("development: %s", dev[1].Command)
	}
}

func TestKeylessSQLConnectorUsesTheSessionToken(t *testing.T) {
	g := newFakeGoogle(t)
	s, err := New(g.srv.URL).WithMinter(&countingMinter{}).Session(t.Context(), testKeylessKey(t))
	if err != nil {
		t.Fatal(err)
	}
	connector, err := s.PostgresConnector(t.Context(), "us-central1", "recovery-copy")
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"changed-project", "changed-email", "no-token-source"} {
		candidate := *s.key
		changed := *s
		changed.key = &candidate
		switch scenario {
		case "changed-project":
			candidate.ProjectID = "shop-staging"
		case "changed-email":
			candidate.ClientEmail = "another@shop-prod.iam.gserviceaccount.com"
		case "no-token-source":
			changed.ts = nil
		}
		if c, err := changed.PostgresConnector(t.Context(), "us-central1", "recovery-copy"); err == nil {
			_ = c.Close()
			t.Errorf("%s: connector admitted", scenario)
		}
	}
}
