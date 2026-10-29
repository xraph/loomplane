package digitalocean

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterCapacityProviderDropletIdentity(t *testing.T) {
	c := doksFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/kubernetes/clusters/cluster":
			_, _ = fmt.Fprint(w, `{"kubernetes_cluster":{"id":"cluster","region":"nyc1","created_at":"2026-01-01T00:00:00Z","status":{"state":"running"},"node_pools":[{"id":"pool","count":1,"nodes":[{"id":"provider-node","droplet_id":"42"}]}]}}`)
		case "/v2/droplets":
			_, _ = fmt.Fprint(w, `{"droplets":[{"id":42,"vcpus":4,"memory":8192,"disk":80}]}`)
		case "/v2/volumes":
			_, _ = fmt.Fprint(w, `{"volumes":[]}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ref := provider.ClusterRef{ID: "cluster", AccountID: "account", Region: "nyc1", Incarnation: "2026-01-01T00:00:00Z"}
	snapshot, err := c.ObserveClusterCapacity(t.Context(), doksAccess(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].ExternalVMID != "42" || snapshot.Nodes[0].ProviderNodeID != "provider-node" || *snapshot.Nodes[0].CPUMillis != 4000 {
		t.Fatalf("wrong physical identity: %+v", snapshot)
	}
}
