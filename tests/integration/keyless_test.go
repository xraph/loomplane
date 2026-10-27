//go:build integration

package integration

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/issuer"
)

// keylessApp is a Loomplane app whose public URL is where it really
// listens, on loopback, so a fake cloud fetches its discovery and keys
// the way a real one would.
type keylessApp struct {
	*running
	url string
	c   *client
	// bodies is every response the test got, to check for secrets.
	mu     sync.Mutex
	bodies []string
}

// startKeylessApp listens first, so cfg.PublicURL can name the address
// before the app is built.
func startKeylessApp(t *testing.T, cfg config.Config) *keylessApp {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	appURL := "http://" + ln.Addr().String()
	cfg.PublicURL = appURL
	r := start(t, cfg, func(s *httptest.Server) {
		_ = s.Listener.Close()
		s.Listener = ln
	})
	return &keylessApp{running: r, url: appURL, c: &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}}
}

// send makes a request with the test's session, keeps the body, and
// decodes it into out when the status is want. Any other status fails.
func (a *keylessApp) send(want int, method, path string, body, out any) string {
	a.t.Helper()
	got, text := a.request(method, path, body, true)
	if got != want {
		a.t.Fatalf("%s %s: status %d, want %d: %s", method, path, got, want, text)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			a.t.Fatalf("%s %s: decode %q: %v", method, path, text, err)
		}
	}
	return text
}

// request sends a request, with the session or with nothing at all, and
// keeps the body.
func (a *keylessApp) request(method, path string, body any, session bool) (int, string) {
	a.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.url+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	if session {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Publishable-Key", a.c.key)
		req.Header.Set("Authorization", "Bearer "+a.c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	a.mu.Lock()
	a.bodies = append(a.bodies, string(data))
	a.mu.Unlock()
	return res.StatusCode, string(data)
}

// unauthenticated GETs path with no session and no publishable key, the
// way a cloud fetches discovery and keys, and checks it's cacheable JSON.
func (a *keylessApp) unauthenticated(path string, out any) {
	a.t.Helper()
	req, err := http.NewRequestWithContext(a.t.Context(), http.MethodGet, a.url+path, http.NoBody)
	if err != nil {
		a.t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	a.mu.Lock()
	a.bodies = append(a.bodies, string(data))
	a.mu.Unlock()
	if res.StatusCode != http.StatusOK {
		a.t.Fatalf("GET %s without a session: %d %s", path, res.StatusCode, data)
	}
	if got := res.Header.Get("Cache-Control"); got != "public, max-age=300" {
		a.t.Fatalf("GET %s: Cache-Control %q", path, got)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		a.t.Fatalf("GET %s: Content-Type %q", path, got)
	}
	if err := json.Unmarshal(data, out); err != nil {
		a.t.Fatalf("GET %s: decode %q: %v", path, data, err)
	}
}

// noSecrets fails if any response the test got holds one of secrets, or
// anything shaped like a private key.
func (a *keylessApp) noSecrets(secrets ...string) {
	a.t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, body := range a.bodies {
		for _, s := range append(secrets, "PRIVATE KEY", `"d":`, `"p":`, `"q":`, `"ticket"`) {
			if s != "" && strings.Contains(body, s) {
				a.t.Fatalf("a response contains %q: %s", s, body)
			}
		}
	}
}

// keylessService is the app's connection service.
func (a *keylessApp) service() *connections.Service {
	a.t.Helper()
	svc, err := forge.InjectType[*connections.Service](a.app.(forge.App).Container())
	if err != nil {
		a.t.Fatal(err)
	}
	return svc
}

// trustingCloud is a cloud's side of a keyless trust. It fetches the
// issuer's discovery and keys from the issuer itself, as a cloud does,
// and takes a token only when its RS256 signature checks against those
// keys and its iss, aud and sub are the trust's.
type trustingCloud struct {
	t      *testing.T
	issuer string

	mu      sync.Mutex
	subject string
	// tokens are the tokens the cloud took, which no response may show.
	tokens []string
}

func (tc *trustingCloud) expect(subject string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.subject = subject
}

func (tc *trustingCloud) taken() []string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return slices.Clone(tc.tokens)
}

// verify checks token for audience, and keeps it if it passes.
func (tc *trustingCloud) verify(ctx context.Context, token, audience string) error {
	tc.mu.Lock()
	subject := tc.subject
	tc.mu.Unlock()
	if subject == "" {
		return errors.New("this cloud trusts no subject yet")
	}
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := fetchJSON(ctx, tc.issuer+"/.well-known/openid-configuration", &discovery); err != nil {
		return err
	}
	if discovery.Issuer != tc.issuer || discovery.JWKSURI != tc.issuer+"/.well-known/jwks.json" {
		return fmt.Errorf("discovery names issuer %q and keys %q", discovery.Issuer, discovery.JWKSURI)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			KID string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := fetchJSON(ctx, discovery.JWKSURI, &set); err != nil {
		return err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		n, nerr := base64.RawURLEncoding.DecodeString(k.N)
		e, eerr := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty != "RSA" || k.Alg != "RS256" || nerr != nil || eerr != nil {
			return fmt.Errorf("the JWKS has a key that isn't RS256: %+v", k)
		}
		keys[k.KID] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	_, err := jwt.Parse(token, func(tok *jwt.Token) (any, error) {
		kid, _ := tok.Header["kid"].(string)
		if k, ok := keys[kid]; ok {
			return k, nil
		}
		return nil, fmt.Errorf("no key %q in the JWKS", kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(tc.issuer), jwt.WithAudience(audience), jwt.WithSubject(subject),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return err
	}
	tc.mu.Lock()
	tc.tokens = append(tc.tokens, token)
	tc.mu.Unlock()
	return nil
}

func fetchJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d", u, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// keylessForm is a cloud's form as GET /api/v1/connection-clouds gives it.
type keylessForm struct {
	Cloud           string   `json:"cloud"`
	Methods         []string `json:"methods"`
	KeylessSettings []struct {
		Key      string `json:"key"`
		Label    string `json:"label"`
		Required bool   `json:"required"`
	} `json:"keyless_settings"`
}

// keylessForms are the connectable clouds' forms, by cloud.
func keylessForms(a *keylessApp) map[string]keylessForm {
	a.t.Helper()
	var list struct {
		Clouds []keylessForm `json:"clouds"`
	}
	a.send(http.StatusOK, http.MethodGet, "/api/v1/connection-clouds", nil, &list)
	out := map[string]keylessForm{}
	for _, f := range list.Clouds {
		out[f.Cloud] = f
	}
	return out
}

// keylessState is the keyless part of GET /api/v1/cloud-sign-in/apps.
func keylessState(a *keylessApp) (enabled bool, iss, reason string) {
	a.t.Helper()
	var list struct {
		Keyless *struct {
			Enabled bool   `json:"enabled"`
			Issuer  string `json:"issuer"`
			Reason  string `json:"reason"`
		} `json:"keyless"`
	}
	a.send(http.StatusOK, http.MethodGet, "/api/v1/cloud-sign-in/apps", nil, &list)
	if list.Keyless == nil {
		a.t.Fatal("the apps list has no keyless state")
	}
	return list.Keyless.Enabled, list.Keyless.Issuer, list.Keyless.Reason
}

// A Google Cloud project trusts Loomplane's issuer. You prepare a keyless
// connection, set up the trust for its reserved ID, and connect it; a
// deployment's session then trades fresh tokens at Google's STS, which
// checks each one against the keys Loomplane publishes. No private key,
// token or ticket ever comes back over the API.
func TestKeyless_googleEndToEnd(t *testing.T) {
	const (
		provider = "projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane"
		account  = "loomplane@shop-prod.iam.gserviceaccount.com"
		aud      = "//iam.googleapis.com/" + provider
	)
	cfg := config.Defaults()
	if cfg.Environment != config.EnvDevelopment {
		t.Fatalf("defaults are %q, want development", cfg.Environment)
	}
	trust := &trustingCloud{t: t}
	var mu sync.Mutex
	exchanged, impersonated := 0, 0
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/token":
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f := r.PostForm
			if f.Get("audience") != aud || f.Get("subject_token_type") != "urn:ietf:params:oauth:token-type:jwt" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_target","error_description":"wrong audience"}`))
				return
			}
			if err := trust.verify(r.Context(), f.Get("subject_token"), aud); err != nil {
				t.Logf("STS refused a token: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"The subject token failed verification."}`))
				return
			}
			exchanged++
			_, _ = fmt.Fprintf(w, `{"access_token":"sts-access-%d","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`, exchanged)
		case "/v1/projects/-/serviceAccounts/" + account + ":generateAccessToken":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sts-access-") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`))
				return
			}
			impersonated++
			_, _ = fmt.Fprintf(w, `{"accessToken":"ya29.keyless-%d","expireTime":%q}`, impersonated, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/v3/projects/shop-prod:testIamPermissions":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ya29.keyless-") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"Request had invalid authentication credentials."}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": gcp.Permissions})
		default:
			t.Errorf("asked Google %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer google.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.GoogleEndpoint = google.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	a := startKeylessApp(t, cfg)
	defer a.stop()
	trust.issuer = a.url

	// Discovery and keys answer anyone, and the keys are there from the
	// start.
	var discovery struct {
		Issuer  string   `json:"issuer"`
		JWKSURI string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}
	a.unauthenticated("/.well-known/openid-configuration", &discovery)
	if discovery.Issuer != a.url || discovery.JWKSURI != a.url+"/.well-known/jwks.json" || !slices.Equal(discovery.Algs, []string{"RS256"}) {
		t.Fatalf("discovery %+v", discovery)
	}
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	for deadline := time.Now().Add(readyTimeout); ; time.Sleep(50 * time.Millisecond) {
		a.unauthenticated("/.well-known/jwks.json", &jwks)
		if len(jwks.Keys) > 0 || time.Now().After(deadline) {
			break
		}
	}
	if len(jwks.Keys) == 0 {
		t.Fatal("the JWKS stayed empty after startup")
	}
	if got, _ := a.request(http.MethodPost, "/api/v1/connections/keyless/prepare", map[string]string{"cloud": "gcp"}, false); got != http.StatusUnauthorized {
		t.Fatalf("prepare without a session: %d", got)
	}

	signedIn(t, a.c)
	if path := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); path != "" {
		var contract json.RawMessage
		a.c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &contract)
		if err := os.WriteFile(path, contract, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Keyless is offered for AWS, Google Cloud and Azure, with what each
	// asks for, and nowhere else.
	forms := keylessForms(a)
	for _, cloud := range []string{connections.AWS, connections.GCP, connections.Azure} {
		if got := strings.Join(forms[cloud].Methods, ","); got != "keyless,token" {
			t.Fatalf("%s methods %q", cloud, got)
		}
	}
	if got := strings.Join(forms[connections.Vercel].Methods, ","); got != "token" {
		t.Fatalf("vercel methods %q", got)
	}
	settings := func(cloud string) string {
		parts := make([]string, 0, len(forms[cloud].KeylessSettings))
		for _, s := range forms[cloud].KeylessSettings {
			parts = append(parts, fmt.Sprintf("%s=%s/%t", s.Key, s.Label, s.Required))
		}
		return strings.Join(parts, ";")
	}
	for cloud, want := range map[string]string{
		connections.AWS: "role_arn=Role ARN/true;region=Region/true",
		connections.GCP: "project_id=Project ID/true;workload_provider=Workload identity provider/true;service_account=Service account email/true",
		connections.Azure: "tenant_id=Directory (tenant) ID/true;client_id=Application (client) ID/true;subscription_id=Subscription ID/true;" +
			"resource_group=Resource group (default loomplane)/false",
	} {
		if got := settings(cloud); got != want {
			t.Fatalf("%s keyless settings %q, want %q", cloud, got, want)
		}
	}
	if enabled, iss, reason := keylessState(a); !enabled || iss != a.url || reason != "development only: no cloud can reach this issuer" {
		t.Fatalf("keyless state: enabled %t, issuer %q, reason %q", enabled, iss, reason)
	}

	// Preparing reserves the connection's ID and says how to set up the
	// trust. Google's audience waits for the provider you make.
	var setup connections.KeylessSetup
	a.send(http.StatusOK, http.MethodPost, "/api/v1/connections/keyless/prepare", map[string]string{"cloud": "gcp"}, &setup)
	if setup.ConnectionID == "" || setup.Issuer != a.url || setup.Audience != "" || len(setup.Steps) == 0 || setup.ExpiresAt == nil ||
		!strings.HasPrefix(setup.Subject, "org:") || !strings.HasSuffix(setup.Subject, ":connection:"+setup.ConnectionID) {
		t.Fatalf("setup %+v", setup)
	}
	a.send(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections/keyless/prepare", map[string]string{"cloud": "vercel"}, nil)
	a.send(http.StatusBadRequest, http.MethodPost, "/api/v1/connections/keyless/prepare", "not an object", nil)
	trust.expect(setup.Subject)

	good := map[string]string{"project_id": "shop-prod", "workload_provider": provider, "service_account": account}
	a.send(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections/keyless",
		connections.KeylessInput{ConnectionID: "conn_nobody_reserved", Name: "Shop", Settings: good}, nil)
	// A typo leaves the reservation for another try.
	a.send(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections/keyless",
		connections.KeylessInput{ConnectionID: setup.ConnectionID, Name: "Shop", Settings: map[string]string{"project_id": "shop-prod", "workload_provider": "nope", "service_account": account}}, nil)

	var conn connections.Connection
	a.send(http.StatusCreated, http.MethodPost, "/api/v1/connections/keyless",
		connections.KeylessInput{ConnectionID: setup.ConnectionID, Name: "Shop", Settings: good}, &conn)
	if conn.ID != setup.ConnectionID || conn.Method != connections.MethodKeyless || conn.Status != connections.StatusReady || conn.Account != "shop-prod" {
		t.Fatalf("connection %+v", conn)
	}
	if len(trust.taken()) == 0 {
		t.Fatal("the connection was made without STS taking a token")
	}
	// A reservation works once.
	a.send(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections/keyless",
		connections.KeylessInput{ConnectionID: setup.ConnectionID, Name: "Shop again", Settings: good}, nil)

	// An existing connection's setup names it, with Google's audience now.
	var again connections.KeylessSetup
	a.send(http.StatusOK, http.MethodGet, "/api/v1/connections/"+conn.ID+"/keyless", nil, &again)
	if again.ConnectionID != conn.ID || again.Subject != setup.Subject || again.Audience != aud || again.ExpiresAt != nil || len(again.Steps) == 0 {
		t.Fatalf("setup of the connection %+v", again)
	}
	a.send(http.StatusNotFound, http.MethodGet, "/api/v1/connections/conn_missing/keyless", nil, nil)
	var replaced connections.Connection
	a.send(http.StatusOK, http.MethodPut, "/api/v1/connections/"+conn.ID+"/keyless", map[string]any{"settings": good}, &replaced)
	if replaced.ID != conn.ID || replaced.Method != connections.MethodKeyless || replaced.Status != connections.StatusReady {
		t.Fatalf("replaced %+v", replaced)
	}
	a.send(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/connections/"+conn.ID+"/keyless",
		map[string]any{"settings": map[string]string{"project_id": "shop-prod"}}, nil)

	// A deployment recalls the credential: a handout, which the app's own
	// Google client turns into a session that renews itself at STS.
	svc := a.service()
	_, handout, err := svc.Credential(t.Context(), conn.OrgID, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	h, ok, err := keyless.Parse(handout)
	if !ok || err != nil || h.Subject != setup.Subject || h.Audience != aud || h.Ticket == "" {
		t.Fatalf("handout ok %t, err %v, subject %q, audience %q", ok, err, h.Subject, h.Audience)
	}
	appGoogle, ok := svc.KeylessClouds[connections.GCP].(*gcp.Client)
	if !ok {
		t.Fatalf("gcp's keyless cloud is %T", svc.KeylessClouds[connections.GCP])
	}
	key, err := gcp.ParseKey(handout)
	if err != nil {
		t.Fatal(err)
	}
	before := len(trust.taken())
	session, err := appGoogle.Session(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := session.Missing(t.Context())
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing %v, err %v", missing, err)
	}
	if len(trust.taken()) <= before {
		t.Fatal("the session never traded a token at STS")
	}

	// A trust that names another connection refuses this one's tokens.
	trust.expect(keyless.Subject(conn.OrgID, "conn_someone_else"))
	refusal := error(nil)
	if s, serr := appGoogle.Session(t.Context(), key); serr != nil {
		refusal = serr
	} else {
		_, refusal = s.Missing(t.Context())
	}
	if !errors.Is(refusal, gcp.ErrRejected) {
		t.Fatalf("a token for a subject the trust doesn't name: %v", refusal)
	}

	secrets := append(trust.taken(), h.Ticket, "sts-access-", "ya29.keyless-")
	a.send(http.StatusOK, http.MethodGet, "/api/v1/connections/"+conn.ID, nil, nil)
	a.send(http.StatusOK, http.MethodGet, "/api/v1/connections", nil, nil)
	a.noSecrets(secrets...)
}

// Keyless needs an issuer a cloud can reach. A non-development app on an
// HTTP public URL offers no keyless method, says why, and refuses to
// prepare one.
func TestKeyless_offWithoutAUsableIssuer(t *testing.T) {
	cfg := config.Defaults()
	cfg.Environment = config.EnvStaging
	cfg.PublicURL = "http://loomplane.example.com"
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	a := &keylessApp{running: r, url: r.srv.URL, c: &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}}
	signedIn(t, a.c)

	for cloud, f := range keylessForms(a) {
		if slices.Contains(f.Methods, connections.MethodKeyless) {
			t.Fatalf("%s offers keyless: %v", cloud, f.Methods)
		}
	}
	if enabled, iss, reason := keylessState(a); enabled || iss != "http://loomplane.example.com" || reason != "public_url isn't HTTPS" {
		t.Fatalf("keyless state: enabled %t, issuer %q, reason %q", enabled, iss, reason)
	}
	got, body := a.request(http.MethodPost, "/api/v1/connections/keyless/prepare", map[string]string{"cloud": "gcp"}, true)
	if got != http.StatusServiceUnavailable || !strings.Contains(body, "public_url isn't HTTPS") {
		t.Fatalf("prepare without a usable issuer: %d %s", got, body)
	}
}

// The connections extension wires keyless together: the issuer, its
// daily rotation, the clouds that connect keylessly, and a minter in each
// of their clients, so AWS's STS and Entra ID each take a token Loomplane
// minted and they checked against its keys.
func TestKeyless_everyCloudClientMints(t *testing.T) {
	cfg := config.Defaults()
	trust := &trustingCloud{t: t}
	clouds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			w.Header().Set("Content-Type", "application/json")
			if err := trust.verify(r.Context(), r.PostForm.Get("client_assertion"), "api://AzureADTokenExchange"); err != nil {
				t.Logf("Entra refused a token: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_client","error_codes":[700213]}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"azure-keyless-access","expires_in":3600}`))
		case r.PostForm.Get("Action") == "AssumeRoleWithWebIdentity":
			w.Header().Set("Content-Type", "text/xml")
			if err := trust.verify(r.Context(), r.PostForm.Get("WebIdentityToken"), "sts.amazonaws.com"); err != nil {
				t.Logf("AWS STS refused a token: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`<ErrorResponse><Error><Type>Sender</Type><Code>InvalidIdentityToken</Code><Message>no</Message></Error></ErrorResponse>`))
				return
			}
			_, _ = w.Write([]byte(`<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult>` +
				`<Credentials><AccessKeyId>ASIAKEYLESS</AccessKeyId><SecretAccessKey>aws-keyless-secret</SecretAccessKey><SessionToken>aws-keyless-session</SessionToken>` +
				`<Expiration>` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`))
		default:
			t.Errorf("asked a cloud %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer clouds.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.AWSEndpoint = clouds.URL
	cfg.Clouds.AzureEndpoint = clouds.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	a := startKeylessApp(t, cfg)
	defer a.stop()
	trust.issuer = a.url

	svc := a.service()
	if svc.Keyless == nil || svc.Keyless.URL() != a.url {
		t.Fatalf("the service's issuer is %v", svc.Keyless)
	}
	if svc.Rotate == nil {
		t.Fatal("the service has no Rotate")
	}
	if err := svc.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(func(yield func(string) bool) {
		for k := range svc.KeylessClouds {
			if !yield(k) {
				return
			}
		}
	}); !slices.Equal(got, []string{connections.AWS, connections.Azure, connections.GCP}) {
		t.Fatalf("keyless clouds %v", got)
	}
	iss, err := forge.InjectType[*issuer.Service](a.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	subject := keyless.Subject("org_wiring", "conn_wiring")
	trust.expect(subject)
	handout := func(audience string, settings map[string]string) []byte {
		t.Helper()
		ticket, terr := iss.Ticket(t.Context(), subject, audience)
		if terr != nil {
			t.Fatal(terr)
		}
		out, merr := json.Marshal(keyless.Handout{Type: keyless.Type, Subject: subject, Audience: audience, Ticket: ticket, Settings: settings})
		if merr != nil {
			t.Fatal(merr)
		}
		return out
	}

	awsClient, ok := svc.KeylessClouds[connections.AWS].(*aws.Client)
	if !ok {
		t.Fatalf("aws's keyless cloud is %T", svc.KeylessClouds[connections.AWS])
	}
	awsKey, err := aws.ParseKey(handout("sts.amazonaws.com", map[string]string{"role_arn": "arn:aws:iam::123456789012:role/loomplane", "region": "us-east-1"}))
	if err != nil {
		t.Fatal(err)
	}
	creds, err := awsClient.Config(awsKey, "us-east-1").Credentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "ASIAKEYLESS" {
		t.Fatalf("aws keyless credentials %q, err %v", creds.AccessKeyID, err)
	}

	azureClient, ok := svc.KeylessClouds[connections.Azure].(*azure.Client)
	if !ok {
		t.Fatalf("azure's keyless cloud is %T", svc.KeylessClouds[connections.Azure])
	}
	placement := map[string]string{"tenant_id": "11111111-1111-1111-1111-111111111111", "client_id": "22222222-2222-2222-2222-222222222222",
		"subscription_id": "33333333-3333-3333-3333-333333333333"}
	azureCred, err := azure.ParseCredential(handout("api://AzureADTokenExchange", placement), placement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = azureClient.Session(t.Context(), azureCred); err != nil {
		t.Fatalf("azure keyless session: %v", err)
	}
	if got := len(trust.taken()); got != 2 {
		t.Fatalf("the clouds took %d tokens, want one each", got)
	}
}
