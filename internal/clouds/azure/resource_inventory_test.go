package azure

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestResourceInventoryGroupAndKinds(t *testing.T) {
	var paths []string
	s, _ := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != "GET" || !strings.Contains(r.URL.Path, "/resourceGroups/") {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		if strings.HasSuffix(r.URL.Path, "/databases") {
			w.Write([]byte(`{"value":[]}`))
			return
		}
		fmt.Fprintf(w, `{"value":[{"id":"%s/main","name":"main","location":"eastus","systemData":{"createdAt":"2026-01-01T00:00:00Z"},"properties":{"state":"Ready","provisioningState":"Succeeded","fullyQualifiedDomainName":"db.test","hostName":"cache.test"}}]}`, r.URL.Path)
	})
	got, err := s.ResourceInventory(t.Context())
	if err != nil || !got.Complete || len(got.Resources) != 3 || len(paths) != 4 {
		t.Fatalf("%+v %v %v", got, err, paths)
	}
}
func TestResourceInventoryRejectsForeignNextLink(t *testing.T) {
	s, _ := monitorSession(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"value":[],"nextLink":"https://attacker.test/steal"}`))
	})
	got, err := s.ResourceInventory(t.Context())
	if err == nil || got.Complete {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestResourceInventoryPaginationKeepsScopeAndReadPort(t *testing.T) {
	calls := 0
	s, _ := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/databases") {
			w.Write([]byte(`{"value":[{"id":"` + strings.TrimSuffix(r.URL.Path, "/databases") + `/databases/default","name":"default","properties":{"port":12000}}]}`))
			return
		}
		if strings.Contains(r.URL.Path, "redisEnterprise") {
			w.Write([]byte(`{"value":[{"id":"` + r.URL.Path + `/cache","name":"cache","properties":{"hostName":"cache.test"}}]}`))
			return
		}
		if r.URL.Query().Get("$skiptoken") == "" {
			w.Write([]byte(`{"value":[],"nextLink":"http://` + r.Host + r.URL.Path + `?api-version=2024-08-01&$skiptoken=second"}`))
			return
		}
		w.Write([]byte(`{"value":[]}`))
	})
	got, err := s.ResourceInventory(t.Context())
	if err != nil || !got.Complete || len(got.Resources) != 1 || got.Resources[0].Port != 12000 || calls != 6 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
}

func TestResourceInventoryRejectsCrossGroupDuplicateAndDeniedPages(t *testing.T) {
	for _, scenario := range []string{"cross-group", "duplicate", "denied", "repeated-page"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := monitorSession(t, func(w http.ResponseWriter, r *http.Request) {
				if scenario == "denied" {
					w.WriteHeader(403)
					w.Write([]byte("SECRET"))
					return
				}
				if scenario == "repeated-page" {
					w.Write([]byte(`{"value":[],"nextLink":"http://` + r.Host + r.URL.Path + `?api-version=2024-08-01"}`))
					return
				}
				id := r.URL.Path + "/main"
				if scenario == "cross-group" {
					id = strings.Replace(id, "resourceGroups/loomplane", "resourceGroups/foreign", 1)
				}
				item := `{"id":"` + id + `","name":"main"}`
				if scenario == "duplicate" {
					item += "," + item
				}
				w.Write([]byte(`{"value":[` + item + `]}`))
			})
			got, err := s.ResourceInventory(t.Context())
			if err == nil || got.Complete || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestResourceInventoryMissingCollectionIsIncomplete(t *testing.T) {
	s, _ := monitorSession(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) })
	got, err := s.ResourceInventory(t.Context())
	if err == nil || got.Complete {
		t.Fatalf("unproven completeness: %+v %v", got, err)
	}
}
