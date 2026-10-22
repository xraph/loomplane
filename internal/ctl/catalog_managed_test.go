package ctl

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
)

func TestCatalogManagedPostgresSelectsAccountBeforePlanning(t *testing.T) {
	for provider, cloud := range map[string]string{"rds": "aws", "cloud-sql": "gcp"} {
		t.Run(provider, func(t *testing.T) {
			region := "us-east-1"
			if cloud == "gcp" {
				region = "us-central1"
			}
			var calls []string
			app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/projects":
					calls = append(calls, "project")
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["name"] != "Orders" || body["repository_url"] != nil {
						t.Errorf("not a standalone service project: %+v", body)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"p","status":"initializing"}`))
				case "/api/v1/projects/p":
					calls = append(calls, "ready")
					_, _ = w.Write([]byte(`{"id":"p","status":"ready"}`))
				case "/api/v1/projects/p/environments/production/connections/" + cloud:
					calls = append(calls, "connection")
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if r.Method != http.MethodPut || body["connection_id"] != "selected" {
						t.Errorf("wrong managed account selection: %s %+v", r.Method, body)
					}
					_, _ = w.Write([]byte(`{"environment":"production","connections":{}}`))
				case "/api/v1/projects/p/manifest":
					calls = append(calls, "manifest")
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					parsed, err := manifest.Parse([]byte(body["yaml"]))
					if err != nil {
						t.Errorf("invalid managed manifest: %v", err)
					} else if resource := parsed.Resources["database"]; resource.Provider != provider || resource.Target != "" || resource.Kind != "postgres" || resource.Version != "17" || resource.Region != region || len(parsed.Services) != 0 {
						t.Errorf("incorrect managed manifest: %s", body["yaml"])
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"manifest"}`))
				case "/api/v1/projects/p/plans":
					calls = append(calls, "plan")
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["catalog"] != true || body["environment"] != "production" {
						t.Errorf("incorrect managed plan: %+v", body)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"plan","status":"planning"}`))
				case "/api/v1/plans/plan":
					calls = append(calls, "ready-plan")
					_, _ = w.Write([]byte(`{"id":"plan","status":"ready","executable":true}`))
				default:
					t.Errorf("unexpected managed service request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			if err := app.Run([]string{"services", "create", "Orders", "--provider", provider, "--target", "selected", "--region", region, "--json"}); err != nil {
				t.Fatal(err)
			}
			want := []string{"project", "ready", "connection", "manifest", "plan", "ready-plan"}
			if !reflect.DeepEqual(calls, want) || !strings.Contains(out.String(), `"id": "plan"`) {
				t.Fatalf("managed plan was not prepared for review: calls=%v output=%s", calls, out)
			}
		})
	}
}

func TestCatalogManagedPostgresStopsWhenAccountSelectionFails(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			app, _ := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/projects":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"p","status":"ready"}`))
				case "/api/v1/projects/p":
					_, _ = w.Write([]byte(`{"id":"p","status":"ready"}`))
				case "/api/v1/projects/p/environments/preview/connections/gcp":
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"selection refused"}`))
				default:
					t.Errorf("selection failure continued with %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			err := app.Run([]string{"services", "create", "Orders", "--provider", "cloud-sql", "--target", "selected", "--env", "preview", "--json"})
			if err == nil || !strings.Contains(err.Error(), "project p") || !strings.Contains(err.Error(), "selection refused") {
				t.Fatalf("lost account failure or created project identity: %v", err)
			}
		})
	}
}

func TestCatalogManagedProfilesRejectUnsupportedOptionsBeforeWrites(t *testing.T) {
	for _, args := range [][]string{
		{"--provider", "rds", "--kind", "mysql"},
		{"--provider", "cloud-sql", "--kind", "redis"},
		{"--provider", "server", "--region", "us-east-1"},
		{"--provider", "kubernetes", "--region", "us-central1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			requests := 0
			app, _ := recoveryCLI(t, func(http.ResponseWriter, *http.Request) { requests++ })
			command := append([]string{"services", "create", "Orders", "--target", "selected"}, args...)
			if err := app.Run(command); err == nil || requests != 0 {
				t.Fatalf("unsupported profile wrote a project: requests=%d err=%v", requests, err)
			}
		})
	}
}
