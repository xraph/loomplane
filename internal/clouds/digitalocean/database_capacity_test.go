package digitalocean

import (
	"net/http"
	"testing"
	"time"
)

func TestDatabaseCapacityAndReviewedResize(t *testing.T) {
	f, c := newFake(t)
	c.poll = time.Millisecond
	f.on("GET /v2/databases/options", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"options": map[string]EngineCapacity{"pg": {Regions: []string{"nyc3"}, Layouts: []DatabaseLayout{{Nodes: 2, Sizes: []string{"db-s-2vcpu-4gb"}}}}}})
	})
	catalog, err := c.DatabaseCapacity(t.Context(), "dop_v1_good")
	if err != nil || len(catalog.Postgres.Layouts) != 1 || catalog.Postgres.Layouts[0].Nodes != 2 {
		t.Fatalf("capacity returned %+v, %v", catalog, err)
	}
	db := Database{ID: "abc", Engine: "pg", Region: "nyc3", Status: databaseOnline, Size: "db-s-1vcpu-1gb", NumNodes: 1}
	writes := 0
	f.on("GET /v2/databases/abc", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]Database{"database": db})
	})
	f.on("PUT /v2/databases/abc/resize", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		writes++
		if body["size"] != "db-s-2vcpu-4gb" || body["num_nodes"] != float64(2) {
			t.Errorf("unexpected resize %v", body)
		}
		db.Size, db.NumNodes = "db-s-2vcpu-4gb", 2
		w.WriteHeader(http.StatusNoContent)
	})
	spec := DatabaseSpec{Engine: "pg", Region: "nyc3", Size: "db-s-2vcpu-4gb", NumNodes: 2}
	for range 2 {
		got, e := c.ResizeDatabase(t.Context(), "dop_v1_good", "abc", spec)
		if e != nil || got.Size != spec.Size {
			t.Fatalf("resize: %+v %v", got, e)
		}
	}
	spec.Region = "sfo3"
	if _, e := c.ResizeDatabase(t.Context(), "dop_v1_good", "abc", spec); e == nil || writes != 1 {
		t.Fatalf("expected region guard and one update, %v %d", e, writes)
	}
}
