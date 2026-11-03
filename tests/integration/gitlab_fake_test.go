//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
