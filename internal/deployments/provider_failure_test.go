package deployments

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// dropConnection closes the connection without an answer, the way a lost
// response looks to the client.
func dropConnection(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	_ = conn.Close()
}

// A frontend's failed apply settles its attempt only when the provider
// itself says the deployment ended in failure. A lost answer while it
// builds leaves the outcome unknown.
func TestVercelFailureIsFinishedOnlyWhenVercelSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finished bool
	}{{"build failed", true}, {"answer lost", false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v9/projects/"):
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"missing"}}`)
				case r.Method == http.MethodPost && r.URL.Path == "/v10/projects":
					_, _ = io.WriteString(w, `{"id":"project","name":"web-production-project"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/v13/deployments":
					_, _ = io.WriteString(w, `{"id":"deployment"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/v13/deployments/deployment":
					if !tc.finished {
						dropConnection(t, w)
						return
					}
					_, _ = io.WriteString(w, `{"id":"deployment","readyState":"ERROR","errorMessage":"build failed"}`)
				default:
					_, _ = io.WriteString(w, `[]`)
				}
			}))
			defer server.Close()
			archive := buildArchive(t, map[string]string{})
			job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", Environment: "production"}, Plan: &planning.Plan{ID: "plan"}, Action: planning.Action{Name: "web", Service: &planning.ServiceConfig{Source: "."}}, Connection: &connections.Connection{ID: "conn"}, Credential: []byte("token"), Progress: func(string, string) {}, Source: func(context.Context) (io.ReadCloser, int64, error) {
				return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
			}, Checkpoint: func([]byte) error { return nil }}
			_, err := (&Vercel{Client: vercel.New(server.URL)}).Apply(t.Context(), job)
			if err == nil {
				t.Fatal("the failed deployment published an outcome")
			}
			if got := finishedByProvider(err); got != tc.finished {
				t.Fatalf("finished = %v, want %v: %v", got, tc.finished, err)
			}
		})
	}
}

func TestRenderFailureIsFinishedOnlyWhenRenderSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finished bool
	}{{"build failed", true}, {"answer lost", false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/services":
					_, _ = io.WriteString(w, `[]`)
				case r.Method == http.MethodPost && r.URL.Path == "/services":
					_, _ = io.WriteString(w, `{"service":{"id":"srv"}}`)
				case r.Method == http.MethodPost && r.URL.Path == "/services/srv/deploys":
					_, _ = io.WriteString(w, `{"id":"dep"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/services/srv/deploys/dep":
					if !tc.finished {
						dropConnection(t, w)
						return
					}
					_, _ = io.WriteString(w, `{"id":"dep","status":"build_failed"}`)
				default:
					_, _ = io.WriteString(w, `{}`)
				}
			}))
			defer server.Close()
			job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", Environment: "production", Sources: []SourceArtifact{{Repository: "acme/web"}}}, CommitSHA: "fedcba9876543210fedcba9876543210fedcba98", Action: planning.Action{Name: "web", Service: &planning.ServiceConfig{Source: ".", Ecosystem: "node", Workload: provider.WebApp, Execution: provider.Container}}, Connection: &connections.Connection{ID: "conn"}, Credential: []byte("key"), Progress: func(string, string) {}, Checkpoint: func([]byte) error { return nil }}
			_, err := (&Render{Client: render.New(server.URL)}).Apply(t.Context(), job)
			if err == nil {
				t.Fatal("the failed deploy published an outcome")
			}
			if got := finishedByProvider(err); got != tc.finished {
				t.Fatalf("finished = %v, want %v: %v", got, tc.finished, err)
			}
		})
	}
}
