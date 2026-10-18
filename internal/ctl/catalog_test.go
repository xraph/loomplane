package ctl

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
)

func TestCatalogCommandPreparesRepositoryFreePostgresPlan(t *testing.T) {
	approved := false
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/approve"):
			approved = true
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "Orders" || body["repository_url"] != nil {
				t.Errorf("standalone project: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"p","status":"initializing"}`))
		case r.URL.Path == "/api/v1/projects/p":
			_, _ = w.Write([]byte(`{"id":"p","status":"ready"}`))
		case r.URL.Path == "/api/v1/projects/p/manifest":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			parsed, parseErr := manifest.Parse([]byte(body["yaml"]))
			if parseErr != nil || len(parsed.Services) != 0 || parsed.Resources["database"].Kind != "postgres" || parsed.Resources["database"].Target != "conn_1" {
				t.Errorf("standalone manifest: %s", body["yaml"])
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"manifest_1","revision":1}`))
		case r.URL.Path == "/api/v1/projects/p/plans":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["catalog"] != true || body["environment"] != "production" {
				t.Errorf("catalog plan: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"plan_1","status":"planning"}`))
		case r.URL.Path == "/api/v1/plans/plan_1":
			_, _ = w.Write([]byte(`{"id":"plan_1","project_id":"p","status":"ready","executable":true,"environment":"production","revision":"immutable"}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := app.Run([]string{"services", "create", "Orders", "--target", "conn_1", "--json"}); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["id"] != "plan_1" || approved {
		t.Fatalf("reviewable plan: %s, %v, approved %v", out, err, approved)
	}
}
