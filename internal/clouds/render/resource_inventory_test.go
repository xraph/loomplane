package render

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestResourceInventoryOwnerAndCursor(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Query().Get("ownerId") != "tea-1" {
			t.Errorf("scope: %s %s", r.Method, r.URL)
		}
		if r.URL.Query().Get("cursor") != "" {
			w.Write([]byte(`[]`))
			return
		}
		key := "postgres"
		if r.URL.Path == "/key-value" {
			key = "keyValue"
		}
		fmt.Fprintf(w, `[{"%s":{"id":"%s-1","name":"main","owner":{"id":"tea-1"},"createdAt":"2026-01-01T00:00:00Z"},"cursor":"next"}]`, key, key)
	}))
	defer srv.Close()
	got, err := New(srv.URL).ResourceInventory(t.Context(), Key{Value: "token", OwnerID: "tea-1"})
	if err != nil || !got.Complete || len(got.Resources) != 2 || calls != 4 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
}

func TestResourceInventoryRejectsForeignOwnerRepeatedCursorAndFailures(t *testing.T) {
	for _, scenario := range []string{"foreign-owner", "repeated-cursor", "denied", "second-page-denied"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if scenario == "denied" || scenario == "second-page-denied" && calls > 1 {
					w.WriteHeader(403)
					w.Write([]byte(`SECRET`))
					return
				}
				owner := "tea-1"
				if scenario == "foreign-owner" {
					owner = "tea-2"
				}
				fmt.Fprintf(w, `[{"postgres":{"id":"pg-%d","owner":{"id":"%s"}},"cursor":"next"}]`, calls, owner)
			}))
			defer srv.Close()
			got, err := New(srv.URL).ResourceInventory(t.Context(), Key{Value: "token", OwnerID: "tea-1"})
			if err == nil || got.Complete || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestVerifyResourceEndpointReadsOnlySelectedResourceAndDropsCredentials(t *testing.T) {
	for _, kind := range []string{"postgres", "redis"} {
		t.Run(kind, func(t *testing.T) {
			var paths []string
			path := "/postgres/pg-1"
			scheme := "postgres"
			port := 5432
			if kind == "redis" {
				path = "/key-value/pg-1"
				scheme = "rediss"
				port = 6379
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("invalid read")
				}
				if r.URL.Path == path {
					w.Write([]byte(`{"id":"pg-1","owner":{"id":"tea-1"},"createdAt":"created"}`))
					return
				}
				if r.URL.Path == path+"/connection-info" {
					fmt.Fprintf(w, `{"password":"SECRET","externalConnectionString":"%s://app:SECRET@db.test:%d/main","internalConnectionString":"%s://app:SECRET@internal.test:%d/main","psqlCommand":"SECRET"}`, scheme, port, scheme, port)
					return
				}
				t.Errorf("unexpected %s", r.URL.Path)
				w.WriteHeader(404)
			}))
			defer srv.Close()
			got, err := New(srv.URL).VerifyResourceEndpoint(t.Context(), Key{Value: "token", OwnerID: "tea-1"}, provider.ResourceObservation{ID: "pg-1", Incarnation: "created", Kind: kind}, "db.test", port)
			if err != nil || got.Host != "db.test" || got.Port != port || len(paths) != 3 {
				t.Fatalf("%+v %v paths=%v", got, err, paths)
			}
			body, _ := json.Marshal(got)
			if strings.Contains(string(body), "SECRET") {
				t.Fatalf("secret leaked %s", body)
			}
		})
	}
}
func TestVerifyResourceEndpointRejectsIdentityAndHostChanges(t *testing.T) {
	for _, scenario := range []string{"owner", "incarnation", "id", "endpoint", "denied", "replacement"} {
		t.Run(scenario, func(t *testing.T) {
			metadataReads := 0
			sensitiveReads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/connection-info") {
					sensitiveReads++
					w.Write([]byte(`{"externalConnectionString":"postgres://app:SECRET@db.test/main"}`))
					return
				}
				metadataReads++
				if scenario == "denied" {
					w.WriteHeader(403)
					w.Write([]byte("SECRET"))
					return
				}
				owner, id, created := "tea-1", "pg-1", "created"
				if scenario == "owner" {
					owner = "tea-2"
				}
				if scenario == "id" {
					id = "pg-2"
				}
				if scenario == "incarnation" || scenario == "replacement" && metadataReads > 1 {
					created = "replacement"
				}
				fmt.Fprintf(w, `{"id":"%s","createdAt":"%s","owner":{"id":"%s"}}`, id, created, owner)
			}))
			defer srv.Close()
			host := "db.test"
			if scenario == "endpoint" {
				host = "other.test"
			}
			got, err := New(srv.URL).VerifyResourceEndpoint(t.Context(), Key{Value: "token", OwnerID: "tea-1"}, provider.ResourceObservation{ID: "pg-1", Incarnation: "created", Kind: "postgres"}, host, 5432)
			if err == nil || got.Host != "" || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%+v %v", got, err)
			}
			if scenario != "endpoint" && scenario != "replacement" && sensitiveReads != 0 {
				t.Error("read secrets before identity matched")
			}
		})
	}
}
