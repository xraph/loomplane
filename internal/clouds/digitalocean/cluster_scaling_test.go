package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterScalingOneWriter(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			writes := 0
			guarded := false
			c := maintenanceAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				writes++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if !guarded || body["count"] != nil || body["size"] != nil || body["auto_scale"] != native {
					t.Fatalf("unsafe native payload: %+v", body)
				}
				if native && (body["min_nodes"] != float64(0) || body["max_nodes"] != float64(3)) {
					t.Fatal(body)
				}
				if !native && (body["min_nodes"] != nil || body["max_nodes"] != nil) {
					t.Fatal(body)
				}
				_, _ = fmt.Fprint(w, `{"node_pool":{"id":"pool"}}`)
			})
			a := doksAccess()
			a.BeforeEffect = func(context.Context) error { guarded = true; return nil }
			bounds := provider.NodePoolScaling{Native: native}
			if native {
				bounds.Max = 3
			}
			if _, err := c.UpdateNodePoolScaling(t.Context(), a, maintenanceRef(), "pool", bounds); err != nil {
				t.Fatal(err)
			}
			if writes != 1 {
				t.Fatal(writes)
			}
			a.BeforeEffect = nil
			if _, err := c.UpdateNodePoolScaling(t.Context(), a, maintenanceRef(), "pool", bounds); err == nil || writes != 1 {
				t.Fatal("unguarded effect")
			}
		})
	}
}
