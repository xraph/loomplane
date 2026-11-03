package repositories_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/repositories"
)

const (
	oauthClientID     = "consumer-key"
	oauthClientSecret = "consumer-secret"
	oauthAccessToken  = "at-secret-value"
	oauthRefreshToken = "rt-secret-value"
)

// fakeOAuth plays Bitbucket's token endpoint and the few API calls the
// connect flow makes. handler runs for anything it doesn't know.
type fakeOAuth struct {
	mu      sync.Mutex
	forms   []url.Values
	users   []string // basic auth user:pass of each token call
	queries []string // raw query of each API call
	auth    []string // Authorization header of each API call
	token   func(w http.ResponseWriter, r *http.Request)
	api     http.HandlerFunc
}

func (f *fakeOAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/site/oauth2/access_token" {
		_ = r.ParseForm()
		u, p, _ := r.BasicAuth()
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		f.users = append(f.users, u+":"+p)
		f.mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			http.Error(w, "bad", http.StatusMethodNotAllowed)
			return
		}
		f.token(w, r)
		return
	}
	f.mu.Lock()
	f.queries = append(f.queries, r.URL.Path+"?"+r.URL.RawQuery)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	f.api(w, r)
}

func newFakeOAuth(t *testing.T) (*fakeOAuth, *repositories.BitbucketOAuth, string) {
	t.Helper()
	f := &fakeOAuth{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	o := repositories.NewBitbucketOAuth(config.BitbucketConfig{
		APIURL: srv.URL, WebURL: srv.URL, ClientID: oauthClientID, ClientSecret: oauthClientSecret,
	})
	return f, o, srv.URL
}

func TestBitbucketOAuth_authorizeURL(t *testing.T) {
	o := repositories.NewBitbucketOAuth(config.BitbucketConfig{
		APIURL: "https://api.example.test/2.0", WebURL: "https://bb.example.test/", ClientID: "k&1", ClientSecret: "s",
	})
	got := o.AuthorizeURL("a b&c=d/e")
	want := "https://bb.example.test/site/oauth2/authorize?client_id=k%261&response_type=code&state=a+b%26c%3Dd%2Fe"
	if got != want {
		t.Fatalf("AuthorizeURL = %s\nwant %s", got, want)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "=s&") {
		t.Fatalf("the secret leaked into the authorize URL: %s", got)
	}
}

func TestBitbucketOAuth_exchange(t *testing.T) {
	f, o, _ := newFakeOAuth(t)
	f.token = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + oauthAccessToken + `","refresh_token":"` + oauthRefreshToken + `","expires_in":7200,"token_type":"bearer","scopes":"account repository"}`))
	}
	before := time.Now()
	g, err := o.Exchange(context.Background(), "the-code")
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if g.AccessToken != oauthAccessToken || g.RefreshToken != oauthRefreshToken {
		t.Fatalf("grant = %+v", struct{ A, R string }{g.AccessToken, g.RefreshToken})
	}
	if g.Expires.Before(before.Add(7200*time.Second)) || g.Expires.After(after.Add(7200*time.Second)) {
		t.Fatalf("Expires = %s, want now+7200s", g.Expires)
	}
	if f.users[0] != oauthClientID+":"+oauthClientSecret {
		t.Fatalf("basic auth = %q", f.users[0])
	}
	if f.forms[0].Get("grant_type") != "authorization_code" || f.forms[0].Get("code") != "the-code" {
		t.Fatalf("form = %v", f.forms[0])
	}
	if f.forms[0].Get("client_secret") != "" {
		t.Fatal("the secret must travel in basic auth, not the form")
	}
}

func TestBitbucketOAuth_exchangeRefused(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"leaks ` + oauthRefreshToken + `"}`, repositories.ErrBitbucketRefused},
		{"unauthorized", http.StatusUnauthorized, `{"error":"invalid_client"}`, repositories.ErrBitbucketRefused},
		{"rate limited", http.StatusTooManyRequests, ``, repositories.ErrRateLimited},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, o, _ := newFakeOAuth(t)
			f.token = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}
			_, err := o.Exchange(context.Background(), "x")
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if strings.Contains(err.Error(), oauthRefreshToken) {
				t.Fatalf("the response body leaked into the error: %v", err)
			}
		})
	}
}

func TestBitbucketOAuth_otherFailuresNeverEchoTheBody(t *testing.T) {
	f, o, _ := newFakeOAuth(t)
	f.token = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request","access_token":"` + oauthAccessToken + `"}`))
	}
	_, err := o.Exchange(context.Background(), "x")
	if err == nil || errors.Is(err, repositories.ErrBitbucketRefused) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
	if strings.Contains(err.Error(), oauthAccessToken) {
		t.Fatalf("the body leaked into the error: %v", err)
	}

	f.token = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"refresh_token":"` + oauthRefreshToken + `","expires_in":7200}`)) // no access token
	}
	_, err = o.Exchange(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), oauthRefreshToken) {
		t.Fatalf("err = %v, want a failure without the token", err)
	}
}

func TestBitbucketOAuth_refresh(t *testing.T) {
	f, o, _ := newFakeOAuth(t)
	f.token = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}
	g, err := o.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if g.AccessToken != "new-access" {
		t.Fatalf("access token = %q", g.AccessToken)
	}
	if g.RefreshToken != "old-refresh" {
		t.Fatalf("a response without a refresh token must keep the old one, got %q", g.RefreshToken)
	}
	if f.forms[0].Get("grant_type") != "refresh_token" || f.forms[0].Get("refresh_token") != "old-refresh" {
		t.Fatalf("form = %v", f.forms[0])
	}
	if f.users[0] != oauthClientID+":"+oauthClientSecret {
		t.Fatalf("basic auth = %q", f.users[0])
	}

	f.token = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"newer-access","refresh_token":"new-refresh","expires_in":3600}`))
	}
	g, err = o.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if g.RefreshToken != "new-refresh" {
		t.Fatalf("refresh token = %q, want the rotated one", g.RefreshToken)
	}

	f.token = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}
	if _, err = o.Refresh(context.Background(), "revoked"); !errors.Is(err, repositories.ErrBitbucketRefused) {
		t.Fatalf("err = %v, want ErrBitbucketRefused", err)
	}
}

func TestBitbucketOAuth_currentUser(t *testing.T) {
	f, o, _ := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"uuid":"{u-1}","display_name":"Ada Lovelace","nickname":"ada"}`))
	}
	u, err := o.CurrentUser(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if u.UUID != "{u-1}" || u.Name != "Ada Lovelace" {
		t.Fatalf("user = %+v", u)
	}
	if f.auth[0] != "Bearer tok" {
		t.Fatalf("Authorization = %q", f.auth[0])
	}
}

func TestBitbucketOAuth_membership(t *testing.T) {
	f, o, _ := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/workspaces" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("q") != `workspace.slug="acme"` {
			_, _ = w.Write([]byte(`{"values":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"values":[{"type":"workspace_access","administrator":false,"workspace":{"uuid":"{w-1}","slug":"acme","name":"Acme Inc","links":{"avatar":{"href":"https://example.test/a.png"}}}}]}`))
	}
	w, err := o.Membership(context.Background(), "tok", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if w.UUID != "{w-1}" || w.Slug != "acme" || w.Name != "Acme Inc" || w.AvatarURL != "https://example.test/a.png" {
		t.Fatalf("workspace = %+v", w)
	}
	if _, err = o.Membership(context.Background(), "tok", "other"); !errors.Is(err, repositories.ErrNotAMember) {
		t.Fatalf("empty result: err = %v, want ErrNotAMember", err)
	}
	// A server that ignores the filter must not make someone a member of
	// a workspace they only share a neighbour of.
	f.api = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"workspace":{"uuid":"{w-2}","slug":"elsewhere"}}]}`))
	}
	if _, err = o.Membership(context.Background(), "tok", "acme"); !errors.Is(err, repositories.ErrNotAMember) {
		t.Fatalf("unfiltered result: err = %v, want ErrNotAMember", err)
	}
	for _, bad := range []string{"", `a"b`, "a b", "a/b"} {
		if _, err = o.Membership(context.Background(), "tok", bad); !errors.Is(err, repositories.ErrNotAMember) {
			t.Fatalf("slug %q: err = %v, want ErrNotAMember", bad, err)
		}
	}
}

func TestBitbucketOAuth_repositoriesPages(t *testing.T) {
	f, o, base := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repositories/acme" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"values":[{"full_name":"acme/web","is_private":false,"mainbranch":{"name":"main"}}]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"values":[{"full_name":"acme/api","is_private":true,"mainbranch":{"name":"trunk"}}],"next":"%s/repositories/acme?page=2&pagelen=100&role=member"}`, base)
	}
	repos, err := o.Repositories(context.Background(), "tok", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].FullName != "acme/api" || !repos[0].Private || repos[0].DefaultBranch != "trunk" ||
		repos[1].FullName != "acme/web" || repos[1].Private || repos[1].DefaultBranch != "main" {
		t.Fatalf("repos = %+v", repos)
	}
	q, _ := url.ParseQuery(strings.SplitN(f.queries[0], "?", 2)[1])
	if q.Get("role") != "member" || q.Get("pagelen") != "100" {
		t.Fatalf("first query = %s", f.queries[0])
	}
	if f.auth[1] != "Bearer tok" {
		t.Fatalf("page 2 Authorization = %q", f.auth[1])
	}
}

func TestBitbucketOAuth_repositoriesStopsAtMaxPages(t *testing.T) {
	f, o, base := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"values":[{"full_name":"acme/r"}],"next":"%s/repositories/acme?again=1"}`, base)
	}
	repos, err := o.Repositories(context.Background(), "tok", "acme")
	if err != nil {
		t.Fatal(err)
	}
	const maxPages = 10 // internal/repositories/app.go
	if len(f.queries) != maxPages || len(repos) != maxPages {
		t.Fatalf("%d requests and %d repositories, want %d of each", len(f.queries), len(repos), maxPages)
	}
}

func TestBitbucketOAuth_repositoriesNeverSendsTheTokenElsewhere(t *testing.T) {
	var stolen []string
	evil := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		stolen = append(stolen, r.Header.Get("Authorization"))
	}))
	t.Cleanup(evil.Close)
	f, o, _ := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"values":[],"next":"%s/steal"}`, evil.URL)
	}
	if _, err := o.Repositories(context.Background(), "tok", "acme"); err == nil {
		t.Fatal("a next link to another host should fail")
	}
	if len(stolen) != 0 {
		t.Fatalf("the token went to another host: %v", stolen)
	}
}

func TestBitbucketOAuth_apiStatuses(t *testing.T) {
	f, o, _ := newFakeOAuth(t)
	status := http.StatusTooManyRequests
	f.api = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
	if _, err := o.Repositories(context.Background(), "tok", "acme"); !errors.Is(err, repositories.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	status = http.StatusNotFound
	if _, err := o.CurrentUser(context.Background(), "tok"); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBitbucketGrant_neverPrintsTokens(t *testing.T) {
	g := repositories.BitbucketGrant{AccessToken: oauthAccessToken, RefreshToken: oauthRefreshToken, Expires: time.Now()}
	for name, got := range map[string]string{
		"%v/%+v":     fmt.Sprintf("%v %+v", g, g),
		"%s":         fmt.Sprintf("%s", g),
		"%#v":        fmt.Sprintf("%#v", g),
		"%v pointer": fmt.Sprintf("%v %+v %#v", &g, &g, &g),
		"in a slice": fmt.Sprintf("%v %+v", []repositories.BitbucketGrant{g}, map[string]repositories.BitbucketGrant{"k": g}),
	} {
		if strings.Contains(got, oauthAccessToken) || strings.Contains(got, oauthRefreshToken) {
			t.Fatalf("%s printed a token: %s", name, got)
		}
	}
	if g.String() != "BitbucketGrant{redacted}" {
		t.Fatalf("String() = %q", g.String())
	}
}

// Go resends a POST body on a 307 or 308, so following one would hand the
// code or the refresh token to wherever the token endpoint pointed.
func TestBitbucketOAuth_tokenEndpointNeverFollowsRedirects(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var mu sync.Mutex
			var got []string
			elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				mu.Lock()
				got = append(got, r.PostForm.Encode())
				mu.Unlock()
				_, _ = w.Write([]byte(`{"access_token":"` + oauthAccessToken + `","expires_in":7200}`))
			}))
			t.Cleanup(elsewhere.Close)
			f, o, _ := newFakeOAuth(t)
			f.token = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", elsewhere.URL+"/site/oauth2/access_token")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`moved, see ` + oauthRefreshToken))
			}
			_, err := o.Refresh(context.Background(), oauthRefreshToken)
			if err == nil {
				t.Fatal("a redirect from the token endpoint should fail")
			}
			if strings.Contains(err.Error(), oauthRefreshToken) || strings.Contains(err.Error(), "moved") {
				t.Fatalf("the redirect's body leaked into the error: %v", err)
			}
			if _, err = o.Exchange(context.Background(), "the-code"); err == nil {
				t.Fatal("a redirect from the token endpoint should fail the exchange too")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 0 {
				t.Fatalf("the redirect target received %v", got)
			}
		})
	}
}

func TestBitbucketGrant_neverMarshalsOrFormatsTokens(t *testing.T) {
	g := repositories.BitbucketGrant{AccessToken: oauthAccessToken, RefreshToken: oauthRefreshToken, Expires: time.Now()}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := json.Marshal(struct {
		G  repositories.BitbucketGrant
		P  *repositories.BitbucketGrant
		Gs []repositories.BitbucketGrant
	}{g, &g, []repositories.BitbucketGrant{g}})
	if err != nil {
		t.Fatal(err)
	}
	// The verbs are in variables so vet lets the test use the wrong ones,
	// which is the point.
	d, x, q := "%d", "%x %X", "%q"
	for name, got := range map[string]string{
		"json":        string(raw),
		"nested json": string(nested),
		"%d":          fmt.Sprintf(d, g),
		"%x":          fmt.Sprintf(x, g, g),
		"%q":          fmt.Sprintf(q, g),
		"%d pointer":  fmt.Sprintf(d+" "+x, &g, &g, &g),
		"%d in slice": fmt.Sprintf(d+" "+x, []repositories.BitbucketGrant{g}, map[string]repositories.BitbucketGrant{"k": g}, g),
	} {
		if strings.Contains(got, oauthAccessToken) || strings.Contains(got, oauthRefreshToken) {
			t.Fatalf("%s printed a token: %s", name, got)
		}
		// %x of the token would be hex, so look for that too.
		if strings.Contains(got, fmt.Sprintf("%x", oauthAccessToken)) || strings.Contains(got, fmt.Sprintf("%x", oauthRefreshToken)) {
			t.Fatalf("%s printed a token in hex: %s", name, got)
		}
	}
}

// Someone in more than one page of workspaces is still a member if
// Bitbucket ignores the filter.
func TestBitbucketOAuth_membershipPages(t *testing.T) {
	f, o, base := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/workspaces" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"values":[{"workspace":{"uuid":"{w-1}","slug":"acme","name":"Acme Inc"}}]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"values":[{"workspace":{"uuid":"{w-9}","slug":"elsewhere"}}],"next":"%s/user/workspaces?page=2&pagelen=100"}`, base)
	}
	w, err := o.Membership(context.Background(), "tok", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if w.UUID != "{w-1}" || w.Slug != "acme" {
		t.Fatalf("workspace = %+v", w)
	}
	q, _ := url.ParseQuery(strings.SplitN(f.queries[0], "?", 2)[1])
	if q.Get("pagelen") != "100" {
		t.Fatalf("first query = %s, want pagelen=100", f.queries[0])
	}
	if f.auth[1] != "Bearer tok" {
		t.Fatalf("page 2 Authorization = %q", f.auth[1])
	}

	// Pages stop at maxPages, and an endless list is no membership.
	f.api = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"values":[{"workspace":{"uuid":"{w-9}","slug":"elsewhere"}}],"next":"%s/user/workspaces?again=1"}`, base)
	}
	f.mu.Lock()
	f.queries = nil
	f.mu.Unlock()
	if _, err = o.Membership(context.Background(), "tok", "acme"); !errors.Is(err, repositories.ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
	if len(f.queries) != 10 {
		t.Fatalf("%d requests, want maxPages (10)", len(f.queries))
	}
}

func TestBitbucketOAuth_membershipNeverSendsTheTokenElsewhere(t *testing.T) {
	var mu sync.Mutex
	var stolen []string
	evil := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stolen = append(stolen, r.Header.Get("Authorization"))
		mu.Unlock()
	}))
	t.Cleanup(evil.Close)
	f, o, _ := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"values":[],"next":"%s/steal"}`, evil.URL)
	}
	if _, err := o.Membership(context.Background(), "tok", "acme"); err == nil || errors.Is(err, repositories.ErrNotAMember) {
		t.Fatalf("err = %v, want a refusal of the next link", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stolen) != 0 {
		t.Fatalf("the token went to another host: %v", stolen)
	}
}

func TestBitbucketOAuth_workspaceByUUID(t *testing.T) {
	f, o, _ := newFakeOAuth(t)
	f.api = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/workspaces" {
			http.NotFound(w, r)
			return
		}
		// Whatever the filter says, the answer is checked on our side.
		_, _ = w.Write([]byte(`{"values":[{"workspace":{"uuid":"{w-2}","slug":"other"}},{"workspace":{"uuid":"{0F2B4A2C-0000-4000-8000-00000000000A}","slug":"acme-new","name":"Acme"}}]}`))
	}
	w, err := o.Workspace(context.Background(), "tok", "{0f2b4a2c-0000-4000-8000-00000000000a}")
	if err != nil {
		t.Fatal(err)
	}
	if w.Slug != "acme-new" || w.Name != "Acme" {
		t.Fatalf("workspace = %+v", w)
	}
	q, _ := url.ParseQuery(strings.SplitN(f.queries[0], "?", 2)[1])
	if q.Get("q") != `workspace.uuid="{0f2b4a2c-0000-4000-8000-00000000000a}"` || q.Get("pagelen") != "100" {
		t.Fatalf("query = %s", f.queries[0])
	}
	if _, err = o.Workspace(context.Background(), "tok", "{0f2b4a2c-0000-4000-8000-00000000000c}"); !errors.Is(err, repositories.ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
	for _, bad := range []string{"", `{a"b}`, "0f2b4a2c", "{a b}", "{w-1}"} {
		if _, err = o.Workspace(context.Background(), "tok", bad); !errors.Is(err, repositories.ErrNotAMember) {
			t.Fatalf("uuid %q: err = %v, want ErrNotAMember", bad, err)
		}
	}
}
