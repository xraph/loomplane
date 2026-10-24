package digitalocean

import (
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestCapacityInventoryPagesAndUnknownFacts(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/droplets", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("page") == "1" {
			reply(w, http.StatusOK, map[string]any{"droplets": []any{map[string]any{"id": 1, "vcpus": 1, "memory": 1024, "disk": 20}}, "links": map[string]any{"pages": map[string]any{"next": "page=2"}}})
			return
		}
		reply(w, http.StatusOK, map[string]any{"droplets": []any{map[string]any{"id": 2, "vcpus": 1, "memory": 1024}}})
	})
	f.on("GET /v2/volumes", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("page") == "1" {
			reply(w, http.StatusOK, map[string]any{"volumes": []any{map[string]any{"id": "1", "size_gigabytes": 10, "droplet_ids": []int{1}}}, "links": map[string]any{"pages": map[string]any{"next": "page=2"}}})
			return
		}
		reply(w, http.StatusOK, map[string]any{"volumes": []any{map[string]any{"id": "retained", "size_gigabytes": 5, "droplet_ids": []int{}}}})
	})
	got, err := c.CapacityInventory(t.Context(), infrastructureCredential())
	if err != nil || len(got.Servers) != 2 || len(got.Volumes) != 2 {
		t.Fatalf("inventory %+v: %v", got, err)
	}
	if got.Servers[1].StorageBytes != nil || got.Quota.Servers != nil {
		t.Fatal("missing facts became zero")
	}
	if got.Volumes[1].ID != "retained" || *got.Volumes[1].StorageBytes != 5_000_000_000 {
		t.Fatal("retained volume missing")
	}
}

func TestCapacityInventoryFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		expected error
	}{
		{"pagination denial", http.StatusForbidden, `{}`, provider.ErrInfrastructureScope},
		{"duplicate immutable ID", http.StatusOK, `{"droplets":[{"id":1}]}`, provider.ErrInfrastructurePagination},
		{"numeric overflow", http.StatusOK, `{"droplets":[{"id":2,"vcpus":9223372036854775807}]}`, provider.ErrInfrastructureInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, c := newFake(t)
			infrastructureAccount(f)
			f.on("GET /v2/droplets", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				if r.URL.Query().Get("page") == "1" {
					reply(w, http.StatusOK, map[string]any{"droplets": []any{map[string]any{"id": 1}}, "links": map[string]any{"pages": map[string]any{"next": "page=2"}}})
					return
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			got, err := c.CapacityInventory(t.Context(), infrastructureCredential())
			if !errors.Is(err, test.expected) || len(got.Servers) != 0 {
				t.Fatalf("partial snapshot returned: %+v %v", got, err)
			}
		})
	}
}
