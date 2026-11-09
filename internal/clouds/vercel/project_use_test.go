package vercel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectInUse(t *testing.T) {
	for name, c := range map[string]struct {
		envs, deployments string
		status            int
		want              bool
	}{
		"empty":                {envs: `{"environments":[]}`, deployments: `{"deployments":[]}`},
		"system environments":  {envs: `{"environments":[{"id":"env_p","slug":"production"},{"id":"env_v","slug":"preview"}]}`, deployments: `{"deployments":[]}`},
		"a custom environment": {envs: `{"environments":[{"id":"env_9","slug":"staging"}]}`, deployments: `{"deployments":[]}`, want: true},
		"a deployment":         {envs: `{"environments":[]}`, deployments: `{"deployments":[{"uid":"dpl_1"}]}`, want: true},
		"gone":                 {status: http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.status != 0 {
					w.WriteHeader(c.status)
					fmt.Fprint(w, `{"error":{"code":"not_found","message":"missing"}}`)
					return
				}
				switch {
				case r.URL.Path == "/v9/projects/prj_1/custom-environments":
					fmt.Fprint(w, c.envs)
				case r.URL.Path == "/v7/deployments" && r.URL.Query().Get("projectId") == "prj_1" && r.URL.Query().Get("target") == "":
					fmt.Fprint(w, c.deployments)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
				}
			}))
			t.Cleanup(srv.Close)
			got, err := New(srv.URL).ProjectInUse(t.Context(), Token{Value: "t"}, "prj_1")
			if err != nil || got != c.want {
				t.Fatalf("ProjectInUse = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}
