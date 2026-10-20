package ctl

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
)

func TestCatalogKubernetesCLISelectsTargetProfileBeforeWrites(t *testing.T) {
	for _, test := range []struct {
		provider, kind string
		valid          bool
	}{{"kubernetes", "postgres", true}, {"kubernetes", "mysql", false}, {"kubernetes", "mariadb", false}, {"foreign", "postgres", false}} {
		t.Run(test.provider+"-"+test.kind, func(t *testing.T) {
			requests := 0
			app, _ := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch r.URL.Path {
				case "/api/v1/projects":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"p","status":"ready"}`))
				case "/api/v1/projects/p":
					_, _ = w.Write([]byte(`{"id":"p","status":"ready"}`))
				case "/api/v1/projects/p/manifest":
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					parsed, err := manifest.Parse([]byte(body["yaml"]))
					if err != nil || parsed.Resources["database"].Provider != "kubernetes" || parsed.Resources["database"].Target != "selected" {
						t.Fatalf("incorrect cluster manifest: %s %v", body["yaml"], err)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"manifest"}`))
				case "/api/v1/projects/p/plans":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"plan","status":"ready"}`))
				case "/api/v1/plans/plan":
					_, _ = w.Write([]byte(`{"id":"plan","status":"ready"}`))
				default:
					t.Errorf("unexpected API request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := app.Run([]string{"services", "create", "Cluster database", "--kind", test.kind, "--provider", test.provider, "--target", "selected", "--json"})
			if test.valid {
				if err != nil || requests == 0 {
					t.Fatalf("cluster CLI unavailable: %v", err)
				}
			} else if err == nil || requests != 0 {
				t.Fatalf("unqualified target wrote a project: %v %d", err, requests)
			}
		})
	}
}
