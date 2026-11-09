//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeGitLabProjects serves each tarball as the public project its path
// names, on a main branch at exampleSHA, the way GitLab's v4 API serves
// one. A project's path is its full path with slashes, and the API
// addresses it with them escaped (grp%2Fsub%2Fapi).
func fakeGitLabProjects(t *testing.T, projects map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath keeps %2F, which Path has already turned into a slash.
		got := r.URL.EscapedPath()
		for path, tgz := range projects {
			base := "/api/v4/projects/" + url.PathEscape(path)
			switch got {
			case base:
				_, _ = w.Write([]byte(`{"id":7,"path_with_namespace":"` + path + `","default_branch":"main"}`))
				return
			case base + "/repository/commits/main":
				_, _ = w.Write([]byte(`{"id":"` + exampleSHA + `"}`))
				return
			case base + "/repository/archive.tar.gz":
				if r.URL.Query().Get("sha") != exampleSHA {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(tgz)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The values the fake GitLab below hands out and expects.
const (
	fakeGitLabAccessToken  = "glpat-it-access-token"
	fakeGitLabRefreshToken = "glrt-it-refresh"
	fakeGitLabCode         = "from-gitlab"
	fakeGitLabClientID     = "it-gitlab-client"
	fakeGitLabSecret       = "it-gitlab-client-secret"
)

// gitLabFake is a self-managed GitLab with an OAuth application, one signed
// in account and one private project. It remembers what Loomplane asked it.
type gitLabFake struct {
	*httptest.Server

	mu sync.Mutex
	// calls are the requests that matter, in the order they arrived.
	calls []string
	// archiveTokens are the bearer tokens the private archive was asked with.
	archiveTokens []string
	// revoked are the tokens it was told to stop honoring, each as
	// "<token> <token_type_hint>".
	revoked []string
	// requests are the API requests it answered, as "<escaped path> bearer"
	// or "<escaped path> anonymous".
	requests []string
}

// requestsSeen returns a copy of the API requests the fake answered.
func (g *gitLabFake) requestsSeen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.requests...)
}

func (g *gitLabFake) note(call string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, call)
}

// seen returns a copy of what the fake recorded.
func (g *gitLabFake) seen() (calls, archiveTokens, revoked []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...), append([]string(nil), g.archiveTokens...), append([]string(nil), g.revoked...)
}

// fakeGitLab serves the private project grp/sub/api, readable only with the
// token the OAuth application hands out in exchange for fakeGitLabCode.
func fakeGitLab(t *testing.T, tgz []byte) *gitLabFake {
	t.Helper()
	g := &gitLabFake{}
	const path = "grp/sub/api"
	base := "/api/v4/projects/" + url.PathEscape(path)
	authed := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+fakeGitLabAccessToken }
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath keeps %2F, which Path has already turned into a slash.
		got := r.URL.EscapedPath()
		if strings.HasPrefix(got, "/api/v4/") {
			who := "anonymous"
			if r.Header.Get("Authorization") != "" {
				who = "bearer"
			}
			g.mu.Lock()
			g.requests = append(g.requests, got+" "+who)
			g.mu.Unlock()
		}
		switch got {
		case "/oauth/token":
			if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") != fakeGitLabCode ||
				r.FormValue("client_id") != fakeGitLabClientID || r.FormValue("client_secret") != fakeGitLabSecret ||
				r.FormValue("code_verifier") == "" || r.FormValue("redirect_uri") == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			g.note("token")
			_, _ = w.Write([]byte(`{"access_token":"` + fakeGitLabAccessToken + `","refresh_token":"` + fakeGitLabRefreshToken + `","expires_in":7200}`))
		case "/oauth/revoke":
			g.mu.Lock()
			g.calls = append(g.calls, "revoke")
			g.revoked = append(g.revoked, r.FormValue("token")+" "+r.FormValue("token_type_hint"))
			g.mu.Unlock()
		case "/api/v4/user":
			if !authed(r) {
				http.Error(w, "", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":11,"username":"ada","avatar_url":"https://gitlab.example/ada.png"}`))
		case "/api/v4/version":
			if !authed(r) {
				http.Error(w, "", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"version":"17.4.2-ee","revision":"abc"}`))
		case "/api/v4/projects":
			if !authed(r) {
				http.Error(w, "", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`[{"id":7,"path_with_namespace":"` + path + `","name":"api","visibility":"private","default_branch":"main","namespace":{"full_path":"grp/sub"}}]`))
		case base:
			if !authed(r) {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"id":7,"path_with_namespace":"` + path + `","name":"api","visibility":"private","default_branch":"main"}`))
		case base + "/repository/commits/main":
			if !authed(r) {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + exampleSHA + `"}`))
		case base + "/repository/archive.tar.gz":
			if !authed(r) || r.URL.Query().Get("sha") != exampleSHA {
				http.NotFound(w, r)
				return
			}
			g.mu.Lock()
			g.calls = append(g.calls, "archive")
			g.archiveTokens = append(g.archiveTokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			g.mu.Unlock()
			_, _ = w.Write(tgz)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}
