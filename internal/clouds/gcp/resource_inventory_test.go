package gcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceInventoryProjectAndKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("mutation %s", r.Method)
		}
		switch {
		case strings.Contains(r.URL.Path, "/sql/"):
			w.Write([]byte(`{"items":[{"name":"main","project":"project-test","region":"us-central1","databaseVersion":"POSTGRES_16","createTime":"2026-01-01T00:00:00Z"}]}`))
		case strings.Contains(r.URL.Path, "/instances"):
			w.Write([]byte(`{"instances":[{"name":"projects/project-test/locations/us-central1/instances/cache","createTime":"2026-01-01T00:00:00Z","host":"cache.test","port":6379}]}`))
		case r.URL.Path == "/storage/v1/b":
			if r.URL.Query().Get("project") != "project-test" {
				t.Error("project filter absent")
			}
			w.Write([]byte(`{"items":[{"id":"bucket","name":"bucket","timeCreated":"2026-01-01T00:00:00Z","location":"US"}]}`))
		default:
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	got, err := metricsSession(srv).ResourceInventory(t.Context())
	if err != nil || !got.Complete || len(got.Resources) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestResourceInventoryUnreachableIsIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/instances") && !strings.Contains(r.URL.Path, "/sql/") {
			w.Write([]byte(`{"unreachable":["us-central1"]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	got, err := metricsSession(srv).ResourceInventory(t.Context())
	if err == nil || got.Complete {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestResourceInventoryPaginationAndBoundaries(t *testing.T) {
	for _, scenario := range []string{"pages", "foreign-project", "repeated-token", "duplicate", "second-page-denied"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/sql/") {
					w.Write([]byte(`{}`))
					return
				}
				calls++
				if scenario == "second-page-denied" && calls > 1 {
					w.WriteHeader(403)
					w.Write([]byte("SECRET"))
					return
				}
				if scenario == "foreign-project" {
					w.Write([]byte(`{"items":[{"name":"main","project":"other-project","databaseVersion":"POSTGRES_16"}]}`))
					return
				}
				if calls == 1 {
					w.Write([]byte(`{"items":[{"name":"main","project":"project-test","databaseVersion":"POSTGRES_16"}],"nextPageToken":"next"}`))
					return
				}
				if r.URL.Query().Get("pageToken") != "next" {
					t.Error("missing cursor")
				}
				switch scenario {
				case "duplicate":
					w.Write([]byte(`{"items":[{"name":"main","project":"project-test","databaseVersion":"POSTGRES_16"}]}`))
				case "repeated-token":
					w.Write([]byte(`{"nextPageToken":"next"}`))
				default:
					w.Write([]byte(`{"items":[{"name":"mysql","project":"project-test","databaseVersion":"MYSQL_8_0"}]}`))
				}
			}))
			defer srv.Close()
			got, err := metricsSession(srv).ResourceInventory(t.Context())
			if scenario == "pages" {
				if err != nil || !got.Complete || len(got.Resources) != 2 || got.Resources[1].Kind != "mysql" {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil || got.Complete || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestResourceInventorySQLIncarnationMatchesNativeAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/sql/") {
			w.Write([]byte(`{"items":[{"name":"main","project":"project-test","region":"us-central1","connectionName":"project-test:us-central1:main","databaseVersion":"POSTGRES_16","createTime":"2026-01-01T00:00:00Z"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	want, err := (&SQLInstance{Name: "main", Project: "project-test", Region: "us-central1", ConnectionName: "project-test:us-central1:main", CreateTime: "2026-01-01T00:00:00Z"}).Incarnation("project-test", "us-central1", "main")
	if err != nil {
		t.Fatal(err)
	}
	got, err := metricsSession(srv).ResourceInventory(t.Context())
	if err != nil || len(got.Resources) != 1 || got.Resources[0].Incarnation != want {
		t.Fatalf("incarnation = %+v %v, want %s", got, err, want)
	}
}
