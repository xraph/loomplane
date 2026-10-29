package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

const maintenanceClusterJSON = `{"kubernetes_cluster":{"id":"cluster","name":"cluster","region":"nyc1","created_at":"2026-01-01T00:00:00Z","version":"1.34.1-do.0","status":{"state":"running"},"node_pools":[{"id":"pool","name":"workers","size":"s-2","count":2}]}}`

func maintenanceAdapter(t *testing.T, mutate func(http.ResponseWriter, *http.Request)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutate(w, r)
			return
		}
		switch r.URL.Path {
		case "/v2/account":
			_, _ = fmt.Fprint(w, `{"account":{"uuid":"account"}}`)
		case "/v2/kubernetes/clusters/cluster":
			_, _ = fmt.Fprint(w, maintenanceClusterJSON)
		case "/v2/kubernetes/clusters/cluster/upgrades":
			_, _ = fmt.Fprint(w, `{"available_upgrade_versions":[{"slug":"1.34.2-do.0"}]}`)
		default:
			t.Errorf("unexpected read %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return New(server.URL)
}
func maintenanceRef() provider.ClusterRef {
	return provider.ClusterRef{ID: "cluster", AccountID: "account", Region: "nyc1", Incarnation: "2026-01-01T00:00:00Z"}
}
func TestClusterUpgradeUnsupportedPath(t *testing.T) {
	c := maintenanceAdapter(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unsafe effect") })
	for _, tc := range []struct{ version, reason string }{{"", "unsupported_upgrade_path"}, {"1.33.9-do.0", "unsupported_upgrade_path"}, {"1.34.2-do.0", "provider_force_drain_unavoidable"}} {
		_, err := c.UpgradeCluster(t.Context(), doksAccess(), maintenanceRef(), tc.version)
		var blocked *provider.ClusterLifecycleBlockedError
		if !errors.As(err, &blocked) || blocked.Reason != tc.reason {
			t.Fatalf("%s: %v", tc.version, err)
		}
	}
}
func TestClusterMaintenanceEffectsRequireFinalGuard(t *testing.T) {
	mutations := 0
	guarded := false
	c := maintenanceAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if !guarded {
			t.Error("effect before guard")
		}
		mutations++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/v2/kubernetes/clusters/cluster":
			if body["name"] != "cluster" || body["ha"] != nil || body["auto_upgrade"] != nil {
				t.Fatal(body)
			}
			_, _ = fmt.Fprint(w, maintenanceClusterJSON)
		case "/v2/kubernetes/clusters/cluster/node_pools/pool":
			if body["count"] != float64(3) || body["size"] != nil {
				t.Fatal(body)
			}
			_, _ = fmt.Fprint(w, `{"node_pool":{"id":"pool"}}`)
		case "/v2/kubernetes/clusters/cluster/node_pools":
			if body["tags"] == nil || body["auto_scale"] != false {
				t.Fatal(body)
			}
			_, _ = fmt.Fprint(w, `{"node_pool":{"id":"new-pool"}}`)
		default:
			t.Fatal(r.URL.Path)
		}
	})
	a := doksAccess()
	a.BeforeEffect = func(context.Context) error { guarded = true; return nil }
	change := provider.ClusterUpdate{FieldMask: []string{"maintenance"}, Maintenance: provider.ClusterMaintenance{Day: "monday", StartTime: "04:00"}}
	if _, err := c.UpdateCluster(t.Context(), a, maintenanceRef(), change); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateNodePool(t.Context(), a, maintenanceRef(), "pool", provider.NodePoolSpec{Name: "workers", Size: "s-2", Count: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateNodePool(t.Context(), a, maintenanceRef(), provider.NodePoolSpec{Name: "new", Size: "s-2", Count: 1, OperationID: "operation", RequestDigest: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	a.BeforeEffect = func(context.Context) error { return errors.New("revoked") }
	if _, err := c.UpdateCluster(t.Context(), a, maintenanceRef(), change); err == nil {
		t.Fatal("revocation ignored")
	}
	if mutations != 3 {
		t.Fatal(mutations)
	}
}
func TestClusterDrainNativeShrinkAndDeleteBlocked(t *testing.T) {
	c := maintenanceAdapter(t, func(http.ResponseWriter, *http.Request) { t.Fatal("destructive effect") })
	_, err := c.UpdateNodePool(t.Context(), doksAccess(), maintenanceRef(), "pool", provider.NodePoolSpec{Name: "workers", Size: "s-2", Count: 1})
	var blocked *provider.ClusterLifecycleBlockedError
	if !errors.As(err, &blocked) {
		t.Fatal(err)
	}
	if _, err = c.DeleteNodePool(t.Context(), doksAccess(), maintenanceRef(), "pool"); !errors.As(err, &blocked) {
		t.Fatal(err)
	}
}
func TestClusterMaintenanceRejectsMasks(t *testing.T) {
	c := maintenanceAdapter(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unsupported field mutated") })
	for _, mask := range [][]string{nil, {"version"}, {"ha"}, {"maintenance", "maintenance"}} {
		if _, err := c.UpdateCluster(t.Context(), doksAccess(), maintenanceRef(), provider.ClusterUpdate{FieldMask: mask}); err == nil {
			t.Fatal(mask)
		}
	}
}

func TestClusterMaintenanceObservationFieldsCannotEnterCreate(t *testing.T) {
	for _, field := range []string{"id", "operation", "digest"} {
		pool := provider.NodePoolSpec{Name: "workers", Size: "size", Count: 1}
		switch field {
		case "id":
			pool.ID = "existing"
		case "operation":
			pool.OperationID = "other"
		case "digest":
			pool.RequestDigest = "other"
		}
		spec := provider.ClusterSpec{OperationID: "operation", Name: "cluster", Region: "nyc1", Version: "v1", NetworkID: "network", NodePools: []provider.NodePoolSpec{pool}}
		if err := validateDOKSSpec(spec); err == nil {
			t.Fatal("creation ignored observed field", field)
		}
	}
}
