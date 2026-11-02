package digitalocean

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceInventoryPagesAndSafeMetadata(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v2/databases" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		calls++
		if calls == 1 {
			w.Write([]byte(`{"databases":[{"id":"pg-1","name":"main","engine":"pg","created_at":"2026-01-01T00:00:00Z","connection":{"host":"pg.test","port":25060,"database":"defaultdb","password":"SECRET","uri":"SECRET"}}],"links":{"pages":{"next":"https://api.digitalocean.com/v2/databases?page=2&per_page=200"}}}`))
			return
		}
		if r.URL.Query().Get("page") != "2" {
			t.Error("missing next page")
		}
		w.Write([]byte(`{"databases":[{"id":"vk-1","engine":"valkey"}]}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL).ResourceInventory(t.Context(), "token")
	if err != nil || !got.Complete || len(got.Resources) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "SECRET") || got.Resources[0].Kind != "postgres" || got.Resources[1].Kind != "redis" {
		t.Fatalf("unsafe metadata %s", b)
	}
}
func TestResourceInventoryRejectsRepeatedPageAndRedactsErrors(t *testing.T) {
	for _, body := range []string{`{"databases":[],"links":{"pages":{"next":"https://api.digitalocean.com/v2/databases?page=1"}}}`, `SECRET`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if body == "SECRET" {
					w.WriteHeader(403)
				}
				w.Write([]byte(body))
			}))
			defer srv.Close()
			got, err := New(srv.URL).ResourceInventory(t.Context(), "token")
			if err == nil || got.Complete || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestResourceInventoryRejectsMissingCollectionAndTruncatedCount(t *testing.T) {
	for _, body := range []string{`{}`, `{"databases":[],"meta":{"total":1}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }))
			defer srv.Close()
			got, err := New(srv.URL).ResourceInventory(t.Context(), "token")
			if err == nil || got.Complete {
				t.Fatalf("unproven completeness: %+v %v", got, err)
			}
		})
	}
}

func TestResourceInventoryRejectsSkippedPagesAndChangingCoverage(t *testing.T) {
	for _, scenario := range []string{"skipped-page", "changing-total", "missing-next"} {
		t.Run(scenario, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "missing-next" {
					w.Write([]byte(`{"databases":[],"links":{"pages":{"last":"https://api.digitalocean.com/v2/databases?page=2"}}}`))
					return
				}
				if r.URL.Query().Get("page") == "1" {
					page := "2"
					total := ""
					if scenario == "skipped-page" {
						page = "3"
					} else {
						total = `,"meta":{"total":5}`
					}
					w.Write([]byte(`{"databases":[],"links":{"pages":{"next":"https://api.digitalocean.com/v2/databases?page=` + page + `"}}` + total + `}`))
					return
				}
				w.Write([]byte(`{"databases":[],"meta":{"total":0}}`))
			}))
			defer srv.Close()
			got, err := New(srv.URL).ResourceInventory(t.Context(), "token")
			if err == nil || got.Complete {
				t.Fatalf("unproven coverage: %+v %v", got, err)
			}
		})
	}
}
