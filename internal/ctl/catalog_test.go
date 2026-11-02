package ctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
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

func TestCatalogCommandSelectsDatabaseKindBeforeCreatingProject(t *testing.T) {
	for _, kind := range []string{"mysql", "mariadb", "redis", "valkey", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
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
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					parsed, err := manifest.Parse([]byte(body["yaml"]))
					if err != nil || parsed.Resources["database"].Kind != kind {
						t.Fatalf("wrong recipe manifest: %s %v", body["yaml"], err)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"manifest"}`))
				case "/api/v1/projects/p/plans":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"plan","status":"ready"}`))
				case "/api/v1/plans/plan":
					_, _ = w.Write([]byte(`{"id":"plan","status":"ready"}`))
				default:
					http.NotFound(w, r)
				}
			})
			err := app.Run([]string{"services", "create", "Orders", "--kind", kind, "--target", "conn_1", "--json"})
			if kind == "unsupported" {
				if err == nil || requests != 0 {
					t.Fatalf("unsupported recipe made writes: %v %d", err, requests)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func recipesCLI(t *testing.T, reg *recipes.Registry, handler http.HandlerFunc) (*App, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := New(Options{Out: &out, Recipes: reg, Getenv: func(key string) string {
		switch key {
		case EnvURL:
			return srv.URL
		case EnvKey:
			return "lp_test"
		}
		return ""
	}, ConfigPath: t.TempDir() + "/none.json"})
	return app, &out
}

func TestServicesAddEditsManifestAndPlans(t *testing.T) {
	var calls []string
	approved := false
	app, out := recipesCLI(t, recipestest.Registry(t), func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/approve"):
			approved = true
		case r.URL.Path == "/api/v1/projects/shop/manifest/edits" && r.Method == http.MethodPost:
			calls = append(calls, "manifests.edit")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			want := map[string]any{"add_resource": map[string]any{
				"name": "gateway", "kind": "sample", "provider": "server", "target": "conn_1", "version": "1",
			}}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("manifests.edit body: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"manifest_2","revision":2}`))
		case r.URL.Path == "/api/v1/projects/shop/plans" && r.Method == http.MethodPost:
			calls = append(calls, "plans.create")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !reflect.DeepEqual(body, map[string]any{"environment": "staging"}) {
				t.Errorf("plans.create body: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"plan_1","status":"planning"}`))
		case r.URL.Path == "/api/v1/plans/plan_1":
			_, _ = w.Write([]byte(`{"id":"plan_1","project_id":"shop","status":"ready","executable":true,"environment":"staging","revision":"immutable"}`))
		default:
			http.NotFound(w, r)
		}
	})
	args := []string{"services", "add", "shop", "--kind", "sample", "--target", "conn_1", "--name", "gateway", "--env", "staging", "--json"}
	if err := app.Run(args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"manifests.edit", "plans.create"}) || approved {
		t.Fatalf("calls %v, approved %v", calls, approved)
	}
	var plan map[string]any
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan["id"] != "plan_1" {
		t.Fatalf("plan output: %s %v", out, err)
	}
}

func TestServicesAddRefusesBeforeWriting(t *testing.T) {
	tests := map[string][]string{
		"unknown kind":     {"services", "add", "shop", "--kind", "nothing", "--target", "conn_1", "--name", "x"},
		"missing target":   {"services", "add", "shop", "--kind", "sample", "--name", "x"},
		"missing name":     {"services", "add", "shop", "--kind", "sample", "--target", "conn_1"},
		"missing project":  {"services", "add", "--kind", "sample", "--target", "conn_1", "--name", "x"},
		"managed provider": {"services", "add", "shop", "--kind", "sample", "--target", "conn_1", "--name", "x", "--provider", "rds"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			requests := 0
			app, _ := recipesCLI(t, recipestest.Registry(t), func(w http.ResponseWriter, _ *http.Request) {
				requests++
				http.NotFound(w, nil)
			})
			if err := app.Run(args); err == nil || requests != 0 {
				t.Fatalf("err %v, %d requests", err, requests)
			}
		})
	}
}

func TestServicesAddPlansResourceOnlyConfigurationWithoutRepositories(t *testing.T) {
	app, _ := recipesCLI(t, recipestest.Registry(t), func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifest/edits"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "rev_1", "yaml": "version: 1\nresources: {cache: {kind: sample}}\n"})
		case strings.HasSuffix(r.URL.Path, "/plans"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["catalog"] != true {
				t.Errorf("repository-free manifest lost catalog mode: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"plan_1","status":"planning"}`))
		case r.URL.Path == "/api/v1/plans/plan_1":
			_, _ = w.Write([]byte(`{"id":"plan_1","status":"ready","executable":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := app.Run([]string{"services", "add", "shop", "--kind", "sample", "--name", "cache", "--target", "conn_1", "--json"}); err != nil {
		t.Fatal(err)
	}
}

func TestServicesCreateTakesServiceProjectKindsOnly(t *testing.T) {
	reg := recipestest.Registry(t)
	for kind, want := range map[string]string{"postgres": "17", "valkey": "8.1", "sample": "", "nothing": ""} {
		if got := serviceProjectVersion(reg, kind); got != want {
			t.Errorf("%s: version %q, want %q", kind, got, want)
		}
	}
	requests := 0
	app, _ := recipesCLI(t, reg, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.NotFound(w, nil)
	})
	err := app.Run([]string{"services", "create", "Orders", "--kind", "sample", "--target", "conn_1"})
	if err == nil || requests != 0 || !strings.Contains(err.Error(), "services add") {
		t.Fatalf("app-project kind: %v, %d requests", err, requests)
	}
}
