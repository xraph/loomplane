package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterRetirementReplacementUID(t *testing.T) {
	c := New("http://unused.invalid")
	adapter, ok := any(c).(interface {
		DeleteCluster(context.Context, provider.AccountAccess, provider.ClusterRef, string) (provider.ClusterMutation, error)
	})
	if !ok {
		t.Fatal("cluster retirement adapter is missing")
	}
	_, err := adapter.DeleteCluster(t.Context(), doksAccess(), maintenanceRef(), "replacement")
	if err == nil {
		t.Fatal("replacement incarnation accepted")
	}
}

func TestClusterRetirementInventoryIncomplete(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatal("unexpected provider mutation")
				}
				switch r.URL.Path {
				case "/v2/account":
					_, _ = fmt.Fprint(w, `{"account":{"uuid":"account"}}`)
				case "/v2/kubernetes/clusters/cluster":
					_, _ = fmt.Fprint(w, maintenanceClusterJSON)
				case "/v2/kubernetes/clusters/cluster/destroy_with_associated_resources":
					if complete {
						_, _ = fmt.Fprint(w, `{"volumes":[{"id":"external-volume"}],"volume_snapshots":[],"load_balancers":[]}`)
					} else {
						_, _ = fmt.Fprint(w, `{"volumes":[]}`)
					}
				default:
					t.Error(r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			c := New(server.URL)
			inventory, err := c.AssociatedResources(t.Context(), doksAccess(), maintenanceRef())
			if err != nil {
				t.Fatal(err)
			}
			if inventory.Complete != complete || inventory.ChildrenQualified {
				t.Fatal(inventory)
			}
			if complete && (len(inventory.Resources) != 1 || inventory.Resources[0].ID != "external-volume" || inventory.Resources[0].Ownership != "unknown" || inventory.Resources[0].EstimatedHourlyMicroUSD != nil) {
				t.Fatal(inventory)
			}
			_, err = c.DeleteCluster(t.Context(), doksAccess(), maintenanceRef(), maintenanceRef().Incarnation)
			var blocked *provider.ClusterLifecycleBlockedError
			if !errors.As(err, &blocked) || blocked.Reason != "child_ownership_unproven" {
				t.Fatal(err)
			}
		})
	}
}

func TestClusterRetirementRecreatedProviderCluster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("replacement received a mutation")
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/v2/account" {
			_, _ = fmt.Fprint(w, `{"account":{"uuid":"account"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"kubernetes_cluster":{"id":"cluster","region":"nyc1","created_at":"2026-02-01T00:00:00Z","status":{"state":"running"}}}`)
	}))
	defer server.Close()
	_, err := New(server.URL).DeleteCluster(t.Context(), doksAccess(), maintenanceRef(), maintenanceRef().Incarnation)
	if !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal("replacement incarnation accepted", err)
	}
}
