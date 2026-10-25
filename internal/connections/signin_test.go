package connections

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeProvider records what it was given and hands back a grant per code.
type fakeProvider struct {
	grants   map[string]oauth.Grant
	code     string
	redirect string
}

func (f *fakeProvider) AuthorizeURL(app oauth.App, redirect, state string) string {
	return "https://provider.example/authorize?" + url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirect}, "state": {state}}.Encode()
}

func (f *fakeProvider) Exchange(_ context.Context, _ oauth.App, redirect, code string) (oauth.Grant, error) {
	f.code, f.redirect = code, redirect
	g, ok := f.grants[code]
	if !ok {
		return oauth.Grant{}, oauth.ErrRefused
	}
	return g, nil
}

func (f *fakeProvider) Refresh(context.Context, oauth.App, string) (oauth.Grant, error) {
	return oauth.Grant{}, errors.New("not used")
}

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

func TestCompleteSignInCreatesAnOAuthConnection(t *testing.T) {
	r := newSignInRig(t)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.provider.code != "code-team-1" || r.provider.redirect != callback {
		t.Fatalf("exchange got code=%q redirect=%q", r.provider.code, r.provider.redirect)
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
		state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme " + name})
		if _, err := r.s.CompleteSignIn(t.Context(), p, CompleteInput{State: state, Code: "code-team-1"}); !errors.Is(err, ErrInvalid) {
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
	if parsed, err := checker.Parse(solo, nil); err != nil || parsed.Account != "bob@example.com" || parsed.Details["team_uuid"] != "" {
		t.Fatalf("solo parse: %+v %v", parsed, err)
	}
	for _, bad := range []string{`{"access_token":""}`, `{"access_token":`, `{}`} {
		if _, err := checker.Parse([]byte(bad), nil); !errors.Is(err, ErrInvalid) {
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
