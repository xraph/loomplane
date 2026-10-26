package connections

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/clouds/vercel"
)

// newVercelSignInRig is a sign-in rig for Vercel. The fake API knows the
// team token vcp-tok (team_one) and the sign-in grants' tokens vcp-team-1
// (team_one) and vcp-team-2 (team_two).
func newVercelSignInRig(t *testing.T) *signInRig {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team := map[string]string{"Bearer vcp-tok": "team_one", "Bearer vcp-team-1": "team_one", "Bearer vcp-team-2": "team_two"}[r.Header.Get("Authorization")]
		if team == "" || r.URL.Path != "/v2/teams/"+team {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"not allowed"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + team + `","slug":"slug-` + team + `"}`))
	}))
	t.Cleanup(api.Close)
	r := newSignInRig(t)
	grant := func(token, team string) oauth.Grant {
		return oauth.Grant{AccessToken: token, Account: map[string]string{"team_id": team, "user_id": "user_1", "installation_id": "icfg_1"}}
	}
	r.provider.grants = map[string]oauth.Grant{
		"code-team-1": grant("vcp-team-1", "team_one"),
		"code-team-2": grant("vcp-team-2", "team_two"),
	}
	r.s.checkers = map[string]Checker{Vercel: VercelChecker{Client: vercel.New(api.URL)}}
	r.s.Providers = map[string]Provider{Vercel: r.provider}
	r.s.ConfigApps = map[string]oauth.App{Vercel: {ClientID: "cfg-id", ClientSecret: "cfg-secret", Slug: "loomplane"}}
	return r
}

func vercelTokenConnection(r *signInRig) {
	r.store.rows["conn_v"] = Connection{ID: "conn_v", OrgID: "org", Cloud: Vercel, Name: "Acme", Method: MethodToken,
		Account: "slug-team_one", Details: map[string]string{"team_id": "team_one"}, Status: StatusReady, Generation: 1}
	r.secrets.kept["org/conn_v"] = []byte("vcp-tok")
}

func TestCompleteVercelSignInLandsOnTheSameIdentityAsAToken(t *testing.T) {
	r := newVercelSignInRig(t)
	vercelTokenConnection(r)
	want := AccountIdentity(Vercel, "", map[string]string{"team_id": "team_one"})
	r.store.rows["conn_v"] = func() Connection { c := r.store.rows["conn_v"]; c.AccountIdentity = want; return c }()

	state := r.start(t, starter, SignInInput{Cloud: Vercel, ConnectionID: "conn_v"})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "conn_v" || c.Method != MethodOAuth || c.Details["team_id"] != "team_one" || c.Account != "slug-team_one" || c.AccountIdentity != want {
		t.Fatalf("connection: %+v", c)
	}
	var kept oauth.Grant
	if err = json.Unmarshal(r.secrets.kept["org/conn_v"], &kept); err != nil || kept.AccessToken != "vcp-team-1" || !kept.Expiry.IsZero() {
		t.Fatalf("vault holds %q: %v", r.secrets.kept["org/conn_v"], err)
	}

	vercelTokenConnection(r)
	state = r.start(t, starter, SignInInput{Cloud: Vercel, ConnectionID: "conn_v"})
	_, err = r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-2"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "verified account changed") {
		t.Fatalf("different team: %v", err)
	}
	if string(r.secrets.kept["org/conn_v"]) != "vcp-tok" {
		t.Fatalf("vault changed: %q", r.secrets.kept["org/conn_v"])
	}
}

func TestVercelCheckerReadsAGrant(t *testing.T) {
	r := newVercelSignInRig(t)
	checker := r.s.checkers[Vercel]
	g, _ := json.Marshal(oauth.Grant{AccessToken: "vcp-team-1", Account: map[string]string{"team_id": "team_one", "user_id": "user_1"}})
	if !checker.(GrantChecker).IsGrant(g) || checker.(GrantChecker).IsGrant([]byte("vcp-tok")) {
		t.Fatal("IsGrant")
	}
	parsed, err := checker.Parse(g, nil)
	if err != nil || parsed.Details["team_id"] != "team_one" {
		t.Fatalf("parse: %+v %v", parsed, err)
	}
	viaGrant, err := checker.Check(t.Context(), g, nil)
	if err != nil {
		t.Fatal(err)
	}
	viaToken, err := checker.Check(t.Context(), []byte("vcp-tok"), map[string]string{"team_id": "team_one"})
	if err != nil {
		t.Fatal(err)
	}
	if viaGrant.Account != viaToken.Account || viaGrant.Details["team_id"] != viaToken.Details["team_id"] {
		t.Fatalf("grant %+v, token %+v", viaGrant, viaToken)
	}
	for _, bad := range []string{`{"access_token":""}`, `{"access_token":`} {
		if _, err = checker.Parse([]byte(bad), nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, err = checker.Parse([]byte(`{"access_token":"x","account":{"team_id":"nope"}}`), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad team: %v", err)
	}
	personal, _ := json.Marshal(oauth.Grant{AccessToken: "vcp-me", Account: map[string]string{"user_id": "user_1"}})
	if parsed, err = checker.Parse(personal, nil); err != nil || parsed.Details["user_id"] != "user_1" || parsed.Details["team_id"] != "" {
		t.Errorf("personal: %+v %v", parsed, err)
	}
}

func TestPastedVercelGrantIsRefused(t *testing.T) {
	r := newVercelSignInRig(t)
	vercelTokenConnection(r)
	r.secrets.puts = 0
	grant := ` {"access_token":"vcp-tok","account":{"team_id":"team_one"}}`
	if _, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: Vercel, Name: "Pasted", Credential: grant}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.s.Replace(t.Context(), starter, "conn_v", ReplaceInput{Credential: grant}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("replace: %v", err)
	}
	if r.secrets.puts != 0 || string(r.secrets.kept["org/conn_v"]) != "vcp-tok" || len(r.store.rows) != 1 {
		t.Fatalf("vault=%q puts=%d rows=%d", r.secrets.kept["org/conn_v"], r.secrets.puts, len(r.store.rows))
	}
}
