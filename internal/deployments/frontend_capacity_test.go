package deployments

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestFrontendCapacityVercelAbsence(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusServiceUnavailable, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v9/projects/exact-project" || r.URL.Query().Get("teamId") != "exact-team" {
					t.Errorf("incorrect absence scope: %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"id":"exact-project","error":{"code":"not_found"}}`))
			}))
			defer server.Close()
			v := &Vercel{Client: vercel.New(server.URL)}
			absent, err := v.FrontendAbsent(t.Context(), &Job{Connection: &connections.Connection{Details: map[string]string{"team_id": "exact-team"}}, Credential: []byte("token")}, &planning.Binding{ExternalID: "exact-project"})
			if absent != (status == http.StatusNotFound) {
				t.Fatalf("status %d absent=%v error=%v", status, absent, err)
			}
		})
	}
}
