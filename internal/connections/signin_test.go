package connections

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

func (f *fakeAppStore) PutSignIn(_ context.Context, p *PendingSignIn) error {
	if f.signIns == nil {
		f.signIns = map[string]*PendingSignIn{}
	}
	f.signIns[p.State] = p
	return nil
}

func (f *fakeAppStore) TakeSignIn(_ context.Context, state string) (*PendingSignIn, error) {
	p, ok := f.signIns[state]
	if !ok {
		return nil, ErrNotFound
	}
	delete(f.signIns, state)
	return p, nil
}

// fakeProvider records what it was given and hands back a grant, or an
// error, per code.
type fakeProvider struct {
	grants map[string]oauth.Grant
	errs   map[string]error
	asked  oauth.AuthorizeRequest
	code   oauth.CodeRequest
}

func (f *fakeProvider) AuthorizeURL(app oauth.App, r oauth.AuthorizeRequest) string {
	f.asked = r
	return "https://provider.example/authorize?" + url.Values{"client_id": {app.ClientID}, "redirect_uri": {r.Redirect}, "state": {r.State}}.Encode()
}

func (f *fakeProvider) Exchange(_ context.Context, _ oauth.App, r oauth.CodeRequest) (oauth.Grant, error) {
	f.code = r
	if err, ok := f.errs[r.Code]; ok {
		return oauth.Grant{}, err
	}
	g, ok := f.grants[r.Code]
	if !ok {
		return oauth.Grant{}, oauth.ErrRefused
	}
	return g, nil
}

func (f *fakeProvider) Refresh(context.Context, oauth.App, oauth.Grant) (oauth.Grant, error) {
	return oauth.Grant{}, errors.New("not used")
}

func (f *fakeProvider) Probe(context.Context, oauth.App, oauth.Grant) error { return nil }

type memStore struct {
	Store
	rows map[string]Connection
}

func (m *memStore) CreateConnection(_ context.Context, c *Connection) error {
	m.rows[c.ID] = *c
	return nil
}

func (m *memStore) GetConnection(_ context.Context, org, id string) (*Connection, error) {
	c, ok := m.rows[id]
	if !ok || c.OrgID != org {
		return nil, ErrNotFound
	}
	c.Details = maps.Clone(c.Details)
	return &c, nil
}

func (m *memStore) UpdateConnection(_ context.Context, c *Connection) error {
	m.rows[c.ID] = *c
	return nil
}

type memSecrets struct {
	kept map[string][]byte
	puts int
}

func (m *memSecrets) Put(_ context.Context, org, id string, v []byte) error {
	m.kept[org+"/"+id] = append([]byte(nil), v...)
	m.puts++
	return nil
}

func (m *memSecrets) Reveal(_ context.Context, org, id string) ([]byte, error) {
	v, ok := m.kept[org+"/"+id]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (m *memSecrets) Delete(_ context.Context, org, id string) error {
	delete(m.kept, org+"/"+id)
	return nil
}

type connAudit struct{ actions []string }

func (a *connAudit) Record(_ context.Context, action string, _ security.Principal, _ *Connection) {
	a.actions = append(a.actions, action)
}

type signInRig struct {
	s        *Service
	store    *memStore
	secrets  *memSecrets
	apps     *fakeAppStore
	provider *fakeProvider
	audit    *connAudit
	clock    time.Time
}

const callback = "https://loomplane.example/oauth/callback"

// newSignInRig is a Service with a DigitalOcean sign-in. The fake API
// answers access tokens at-team-1 and dop_v1_team for team-1, and
// at-team-2 for team-2.
func newSignInRig(t *testing.T) *signInRig {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer at-team-1", "Bearer dop_v1_team":
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-1","email":"ada@example.com","status":"active","team":{"uuid":"team-1","name":"Acme"}}}`))
		case "Bearer at-team-2":
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-1","email":"ada@example.com","status":"active","team":{"uuid":"team-2","name":"Other"}}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"id":"Unauthorized","message":"Unable to authenticate you"}`))
		}
	}))
	t.Cleanup(api.Close)
	grant := func(token, team, name string) oauth.Grant {
		return oauth.Grant{AccessToken: token, RefreshToken: "rt-" + token, Expiry: time.Now().Add(time.Hour),
			Account: map[string]string{"account_uuid": "acct-1", "team_uuid": team, "team_name": name, "email": "ada@example.com"}}
	}
	r := &signInRig{
		store:   &memStore{rows: map[string]Connection{}},
		secrets: &memSecrets{kept: map[string][]byte{}},
		apps:    &fakeAppStore{apps: map[string]*StoredApp{}},
		provider: &fakeProvider{grants: map[string]oauth.Grant{
			"code-team-1": grant("at-team-1", "team-1", "Acme"),
			"code-team-2": grant("at-team-2", "team-2", "Other"),
		}},
		audit: &connAudit{},
		clock: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
	}
	r.s = NewService(r.store, r.secrets, map[string]Checker{DigitalOcean: DigitalOceanChecker{Client: digitalocean.New(api.URL)}},
		inventoryAuthorization{}, r.audit, func(string, error) {})
	r.s.now = func() time.Time { return r.clock }
	r.s.Providers = map[string]Provider{DigitalOcean: r.provider}
	r.s.ConfigApps = map[string]oauth.App{DigitalOcean: {ClientID: "cfg-id", ClientSecret: "cfg-secret"}}
	r.s.CallbackURL = callback
	r.s.Apps = r.apps
	r.s.AppSecrets = &fakeAppSecrets{kept: map[string]string{}}
	return r
}

func (r *signInRig) start(t *testing.T, p security.Principal, in SignInInput) string {
	t.Helper()
	got, err := r.s.StartSignIn(t.Context(), p, in)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	u, err := url.Parse(got.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

var starter = security.Principal{OrgID: "org", UserID: "ada"}

func TestStartSignInBuildsTheProvidersURL(t *testing.T) {
	r := newSignInRig(t)
	got, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	if len(state) < 43 || u.Query().Get("redirect_uri") != callback || u.Query().Get("client_id") != "cfg-id" {
		t.Fatalf("url: %s", got.AuthorizeURL)
	}
	p, ok := r.apps.signIns[state]
	if !ok {
		t.Fatalf("state %q wasn't stored: %v", state, r.apps.signIns)
	}
	if p.OrgID != "org" || p.UserID != "ada" || p.Cloud != DigitalOcean || p.Name != "Acme" || !p.ExpiresAt.Equal(r.clock.Add(10*time.Minute)) {
		t.Fatalf("pending: %+v", p)
	}
	if again := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"}); again == state {
		t.Fatal("two sign-ins share a state")
	}

	// Replacing names a connection of the same cloud in the same org.
	r.store.rows["conn_gcp"] = Connection{ID: "conn_gcp", OrgID: "org", Cloud: GCP}
	if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean, ConnectionID: "conn_gcp"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other cloud: %v", err)
	}
	r.store.rows["conn_far"] = Connection{ID: "conn_far", OrgID: "elsewhere", Cloud: DigitalOcean}
	if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean, ConnectionID: "conn_far"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org: %v", err)
	}
	if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no name: %v", err)
	}
}

func TestStartSignInNeedsAnApp(t *testing.T) {
	r := newSignInRig(t)
	r.s.ConfigApps = nil
	if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"}); !errors.Is(err, ErrNoApp) {
		t.Fatalf("got %v", err)
	}
	if len(r.apps.signIns) != 0 {
		t.Fatalf("stored a sign-in without an app: %v", r.apps.signIns)
	}
	r.s.authz = inventoryAuthorization{denied: security.CloudAccountManage}
	if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("forbidden: %v", err)
	}
}

// A Loomplane without a callback URL can't send anyone to sign in, so it
// says so before recording anything.
func TestStartSignInNeedsACallbackURL(t *testing.T) {
	r := newSignInRig(t)
	r.s.CallbackURL = ""
	if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(r.apps.signIns) != 0 {
		t.Fatalf("stored a sign-in without a callback URL: %v", r.apps.signIns)
	}
}

func TestCompleteSignInCreatesAnOAuthConnection(t *testing.T) {
	r := newSignInRig(t)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.provider.code.Code != "code-team-1" || r.provider.code.Redirect != callback {
		t.Fatalf("exchange got %+v", r.provider.code)
	}
	if c.Status != StatusReady || c.Method != MethodOAuth || c.Details["team_uuid"] != "team-1" || c.Account != "Acme" || c.Name != "Acme" || c.CreatedBy != "ada" {
		t.Fatalf("connection: %+v", c)
	}
	if c.AccountIdentity != AccountIdentity(DigitalOcean, "", map[string]string{"team_uuid": "team-1"}) {
		t.Fatal("a signed-in connection's identity differs from a token's")
	}
	var kept oauth.Grant
	if err := json.Unmarshal(r.secrets.kept["org/"+c.ID], &kept); err != nil || kept.AccessToken != "at-team-1" || kept.RefreshToken != "rt-at-team-1" {
		t.Fatalf("vault holds %q: %v", r.secrets.kept["org/"+c.ID], err)
	}
	shown, _ := json.Marshal(c)
	if strings.Contains(string(shown), "at-team-1") {
		t.Fatalf("the returned connection carries the access token: %s", shown)
	}
	if row := r.store.rows[c.ID]; row.Method != MethodOAuth {
		t.Fatalf("stored method %q", row.Method)
	}
	if len(r.audit.actions) != 1 || r.audit.actions[0] != "cloud_connection.create" {
		t.Fatalf("audit: %v", r.audit.actions)
	}
}

func TestCompleteSignInIsSingleUseAndBoundToItsStarter(t *testing.T) {
	r := newSignInRig(t)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	if _, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"}); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), state) {
		t.Fatalf("replay: %v", err)
	}

	for name, p := range map[string]security.Principal{
		"another user": {OrgID: "org", UserID: "bob"},
		"another org":  {OrgID: "elsewhere", UserID: "ada"},
	} {
		theirs := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme " + name})
		if _, err := r.s.CompleteSignIn(t.Context(), p, CompleteInput{State: theirs, Code: "code-team-1"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	state = r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Late"})
	r.clock = r.clock.Add(11 * time.Minute)
	_, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "code-team-1") {
		t.Fatalf("expired: %v", err)
	}
	if len(r.store.rows) != 1 {
		t.Fatalf("a refused sign-in made a connection: %v", r.store.rows)
	}
	if _, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: "", Code: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty state: %v", err)
	}
}

func tokenConnection(r *signInRig) {
	r.store.rows["conn_do"] = Connection{ID: "conn_do", OrgID: "org", Cloud: DigitalOcean, Name: "Acme", Method: MethodToken,
		Account: "Acme", Details: map[string]string{"team_uuid": "team-1", "account_uuid": "acct-1"}, Status: StatusReady, Generation: 1}
	r.secrets.kept["org/conn_do"] = []byte("dop_v1_team")
}

func TestCompleteSignInReplacesATokenConnectionKeepingItsID(t *testing.T) {
	r := newSignInRig(t)
	tokenConnection(r)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, ConnectionID: "conn_do"})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "conn_do" || c.Method != MethodOAuth || c.Generation != 2 || c.Status != StatusReady {
		t.Fatalf("connection: %+v", c)
	}
	if row := r.store.rows["conn_do"]; row.Method != MethodOAuth || row.Generation != 2 {
		t.Fatalf("stored: %+v", row)
	}
	var kept oauth.Grant
	if err := json.Unmarshal(r.secrets.kept["org/conn_do"], &kept); err != nil || kept.AccessToken != "at-team-1" {
		t.Fatalf("vault holds %q: %v", r.secrets.kept["org/conn_do"], err)
	}
	if len(r.store.rows) != 1 || len(r.audit.actions) != 1 || r.audit.actions[0] != "cloud_connection.replace" {
		t.Fatalf("rows=%d audit=%v", len(r.store.rows), r.audit.actions)
	}
}

func TestCompleteSignInRefusesADifferentTeam(t *testing.T) {
	r := newSignInRig(t)
	tokenConnection(r)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, ConnectionID: "conn_do"})
	_, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-2"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "verified account changed") {
		t.Fatalf("got %v", err)
	}
	if string(r.secrets.kept["org/conn_do"]) != "dop_v1_team" || r.secrets.puts != 0 {
		t.Fatalf("vault changed: %q puts=%d", r.secrets.kept["org/conn_do"], r.secrets.puts)
	}
	if row := r.store.rows["conn_do"]; row.Method != MethodToken || row.Generation != 1 {
		t.Fatalf("row changed: %+v", row)
	}
}

func TestReplaceWithATokenTurnsSignInOff(t *testing.T) {
	r := newSignInRig(t)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.s.Replace(t.Context(), starter, c.ID, ReplaceInput{Credential: " dop_v1_team "})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.Method != MethodToken || r.store.rows[c.ID].Method != MethodToken {
		t.Fatalf("connection: %+v", got)
	}
	if string(r.secrets.kept["org/"+c.ID]) != "dop_v1_team" {
		t.Fatalf("vault holds %q", r.secrets.kept["org/"+c.ID])
	}
}

func TestDigitalOceanCheckerReadsAGrant(t *testing.T) {
	r := newSignInRig(t)
	checker := r.s.checkers[DigitalOcean]
	g, _ := json.Marshal(oauth.Grant{AccessToken: "at-team-1", Account: map[string]string{"account_uuid": "acct-1", "team_uuid": "team-1", "team_name": "Acme", "email": "ada@example.com"}})
	parsed, err := checker.Parse(g, nil)
	if err != nil || parsed.Account != "Acme" || parsed.Details["team_uuid"] != "team-1" || parsed.Details["account_uuid"] != "acct-1" {
		t.Fatalf("parse: %+v %v", parsed, err)
	}
	solo, _ := json.Marshal(oauth.Grant{AccessToken: "at-solo", Account: map[string]string{"account_uuid": "acct-2", "email": "bob@example.com"}})
	if parsed, err = checker.Parse(solo, nil); err != nil || parsed.Account != "bob@example.com" || parsed.Details["team_uuid"] != "" {
		t.Fatalf("solo parse: %+v %v", parsed, err)
	}
	for _, bad := range []string{`{"access_token":""}`, `{"access_token":`, `{}`} {
		if _, err = checker.Parse([]byte(bad), nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	viaGrant, err := checker.Check(t.Context(), g, nil)
	if err != nil {
		t.Fatal(err)
	}
	viaToken, err := checker.Check(t.Context(), []byte("dop_v1_team"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if viaGrant.Account != viaToken.Account || !maps.Equal(viaGrant.Details, viaToken.Details) {
		t.Fatalf("grant %+v, token %+v", viaGrant, viaToken)
	}
	if _, err := checker.Check(t.Context(), []byte(`{"access_token":"revoked"}`), nil); !rejected(err) {
		t.Fatalf("revoked grant: %v", err)
	}
}

func TestPastingAGrantIsRefused(t *testing.T) {
	r := newSignInRig(t)
	tokenConnection(r)
	r.secrets.puts = 0
	grant := ` {"access_token":"dop_v1_team","account":{"team_uuid":"team-1"}}`
	if _, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: DigitalOcean, Name: "Pasted", Credential: grant}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.s.Replace(t.Context(), starter, "conn_do", ReplaceInput{Credential: grant}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("replace: %v", err)
	}
	if r.secrets.puts != 0 || string(r.secrets.kept["org/conn_do"]) != "dop_v1_team" || len(r.store.rows) != 1 {
		t.Fatalf("vault=%q puts=%d rows=%d", r.secrets.kept["org/conn_do"], r.secrets.puts, len(r.store.rows))
	}
}

// gcpSignIn lets r sign in to GCP with a fake provider and a checker
// that accepts any credential for project shop-prod.
func gcpSignIn(r *signInRig) {
	r.s.Providers[GCP] = r.provider
	r.s.ConfigApps[GCP] = oauth.App{ClientID: "g-id", ClientSecret: "g-secret"}
	r.s.checkers[GCP] = identityChecker{Check{Account: "shop-prod", Details: map[string]string{"project_id": "shop-prod"}}}
	r.provider.grants["code-gcp"] = oauth.Grant{AccessToken: "ya29.g", RefreshToken: "1//g", Expiry: time.Now().Add(time.Hour)}
}

func TestStartSignInStoresSettingsAndAChallenge(t *testing.T) {
	r := newSignInRig(t)
	gcpSignIn(r)
	got, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: GCP, Name: "Shop",
		Settings: map[string]string{"project_id": "shop-prod", "region": "us-central1"}})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.apps.signIns[got.State]
	if !ok || got.State == "" {
		t.Fatalf("state %q wasn't stored: %v", got.State, r.apps.signIns)
	}
	if !maps.Equal(p.Settings, map[string]string{"project_id": "shop-prod"}) {
		t.Fatalf("stored settings %v", p.Settings)
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	if p.Verifier == "" || r.provider.asked.Challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("verifier %q, challenge %q", p.Verifier, r.provider.asked.Challenge)
	}
	if r.provider.asked.State != got.State || r.provider.asked.Redirect != callback || !maps.Equal(r.provider.asked.Settings, p.Settings) {
		t.Fatalf("authorize got %+v", r.provider.asked)
	}
	if shown, _ := json.Marshal(got); strings.Contains(string(shown), p.Verifier) {
		t.Fatalf("the verifier was handed out: %s", shown)
	}

	// Finishing hands the cloud the verifier and the same settings.
	if _, err = r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: got.State, Code: "code-gcp"}); err != nil {
		t.Fatal(err)
	}
	if r.provider.code.Verifier != p.Verifier || !maps.Equal(r.provider.code.Settings, p.Settings) {
		t.Fatalf("exchange got %+v", r.provider.code)
	}

	// DigitalOcean takes no settings and no PKCE.
	got, err = r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: DigitalOcean, Name: "Acme", Settings: map[string]string{"project_id": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if p := r.apps.signIns[got.State]; len(p.Settings) != 0 || p.Verifier != "" || r.provider.asked.Challenge != "" {
		t.Fatalf("pending %+v, challenge %q", p, r.provider.asked.Challenge)
	}
}

// Signing in again keeps the connection where it is: the settings come
// from the connection, whatever the request says.
func TestStartSignInAgainReusesTheConnectionsSettings(t *testing.T) {
	r := newSignInRig(t)
	gcpSignIn(r)
	r.store.rows["conn_gcp"] = Connection{ID: "conn_gcp", OrgID: "org", Cloud: GCP, Name: "Shop", Method: MethodOAuth,
		Details: map[string]string{"project_id": "shop-prod", "email": "ada@example.com"}, Status: StatusSignInNeeded}
	got, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: GCP, ConnectionID: "conn_gcp",
		Settings: map[string]string{"project_id": "elsewhere"}})
	if err != nil {
		t.Fatal(err)
	}
	if p := r.apps.signIns[got.State]; !maps.Equal(p.Settings, map[string]string{"project_id": "shop-prod"}) || p.ConnectionID != "conn_gcp" {
		t.Fatalf("pending %+v", p)
	}
}

// handingOut is a renewer for a cloud whose sessions renew themselves:
// it hands out a credential built from the grant.
type handingOut struct{ *renewer }

func (handingOut) Handout(app oauth.App, g oauth.Grant, settings map[string]string) ([]byte, error) {
	return []byte("handout:" + app.ClientID + ":" + g.RefreshToken + ":" + settings["project_id"]), nil
}

// seenChecker accepts any credential for project shop-prod and keeps the
// last one it checked.
type seenChecker struct{ seen []byte }

func (c *seenChecker) Parse([]byte, map[string]string) (Check, error) {
	return Check{Account: "shop-prod", Details: map[string]string{"project_id": "shop-prod"}}, nil
}

func (c *seenChecker) Check(_ context.Context, cred []byte, s map[string]string) (Check, error) {
	c.seen = append([]byte(nil), cred...)
	return c.Parse(cred, s)
}

func TestCredentialHandsOutWithoutRenewing(t *testing.T) {
	r := newRefreshRig(t)
	checker := &seenChecker{}
	r.s.checkers[GCP] = checker
	r.s.Providers[GCP] = handingOut{r.renewer}
	r.s.ConfigApps[GCP] = oauth.App{ClientID: "g-id", ClientSecret: "g-secret"}
	r.store.rows["conn_gcp"] = Connection{ID: "conn_gcp", OrgID: "org", Cloud: GCP, Name: "Shop", Method: MethodOAuth, Account: "shop-prod",
		Details: map[string]string{"project_id": "shop-prod"}, Status: StatusReady, Generation: 2}
	r.keep(t, "conn_gcp", oauth.Grant{AccessToken: "ya29.old", RefreshToken: "1//rt", Expiry: r.clock.Add(time.Minute)})

	_, cred, err := r.s.Credential(t.Context(), "org", "conn_gcp")
	if err != nil {
		t.Fatal(err)
	}
	if string(cred) != "handout:g-id:1//rt:shop-prod" {
		t.Fatalf("credential %q", cred)
	}
	if locks, _ := r.locked.stats(); r.renewer.calls != 0 || locks != 0 {
		t.Fatalf("a self-renewing sign-in was renewed: calls=%d locks=%d", r.renewer.calls, locks)
	}

	// Checking it again checks the handout too.
	c, err := r.s.Recheck(t.Context(), starter, "conn_gcp")
	if err != nil || c.Status != StatusReady {
		t.Fatalf("recheck: %+v %v", c, err)
	}
	if string(checker.seen) != "handout:g-id:1//rt:shop-prod" || r.renewer.calls != 0 {
		t.Fatalf("checked %q, renewed %d times", checker.seen, r.renewer.calls)
	}
}

// The organization rotated its app's secret in the cloud but not in
// Loomplane. The app shows why, the connection asks for a sign-in, and
// saving the app again clears it.
func TestAppRejectedIsRecordedOnTheApp(t *testing.T) {
	r := newRefreshRig(t)
	r.s.ConfigApps = nil
	if _, err := r.s.SetSignInApp(t.Context(), starter, DigitalOcean, AppInput{ClientID: "org-id", ClientSecret: "old-secret"}); err != nil {
		t.Fatal(err)
	}
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.errs["rt-at-1"] = fmt.Errorf("%w: digitalocean answered 401: invalid_client", oauth.ErrAppRejected)
	const why = "digitalocean refused the sign-in app; check its client ID and secret"

	if _, _, err := r.s.Credential(t.Context(), "org", "conn_do"); !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), why) {
		t.Fatalf("credential: %v", err)
	}
	row := r.store.rows["conn_do"]
	if row.Status != StatusSignInNeeded || row.Message != why || row.Generation != 3 || len(r.lapsed) != 1 {
		t.Fatalf("connection %+v, lapsed %d", row, len(r.lapsed))
	}
	apps, err := r.s.SignInApps(t.Context(), starter)
	if err != nil || len(apps) != 1 || apps[0].Problem != why {
		t.Fatalf("apps %+v: %v", apps, err)
	}
	if shown, _ := json.Marshal(apps); strings.Contains(string(shown), "old-secret") {
		t.Fatalf("the secret was shown: %s", shown)
	}

	if _, err := r.s.SetSignInApp(t.Context(), starter, DigitalOcean, AppInput{ClientID: "org-id", ClientSecret: "new-secret"}); err != nil {
		t.Fatal(err)
	}
	if apps, _ = r.s.SignInApps(t.Context(), starter); apps[0].Problem != "" {
		t.Fatalf("saving the app kept its problem: %+v", apps[0])
	}

	// An exchange the cloud refuses for the app is recorded the same way.
	s := newSignInRig(t)
	s.provider.errs = map[string]error{"code-bad-app": oauth.ErrAppRejected}
	state := s.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	if _, err := s.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-bad-app"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), why) {
		t.Fatalf("complete: %v", err)
	}
	if s.apps.problems["org/"+DigitalOcean] != why || len(s.store.rows) != 0 {
		t.Fatalf("problems %v, rows %v", s.apps.problems, s.store.rows)
	}
}
