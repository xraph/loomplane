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
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func doksAccess() provider.AccountAccess {
	return provider.AccountAccess{Provider: "digitalocean", AccountID: "account", Credential: []byte("secret-token")}
}
func doksFixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("mutation: %s", r.Method)
		}
		if r.URL.Path == "/v2/account" {
			_, _ = fmt.Fprint(w, `{"account":{"uuid":"account"}}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return New(server.URL)
}
func TestDOKSOptionsUseClusterCatalog(t *testing.T) {
	c := doksFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/kubernetes/options":
			_, _ = fmt.Fprint(w, `{"options":{"regions":[{"slug":"nyc1"}],"versions":[{"slug":"1.34-do.1"}],"sizes":[{"slug":"doks"}]}}`)
		case "/v2/sizes":
			_, _ = fmt.Fprint(w, `{"sizes":[{"slug":"doks","available":true,"vcpus":2,"memory":4096,"disk":80,"regions":["nyc1"]},{"slug":"droplet-only","available":true,"vcpus":4,"memory":8192,"disk":160,"regions":["nyc1"]}]}`)
		case "/v2/vpcs":
			_, _ = fmt.Fprint(w, `{"vpcs":[{"id":"vpc","region":"nyc1"},{"id":"other","region":"sfo3"}]}`)
		default:
			t.Errorf("unexpected catalog %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	options, err := c.Options(t.Context(), doksAccess(), "nyc1")
	if err != nil || len(options.NodeSizes) != 1 || options.NodeSizes[0] != "doks" || len(options.Networks) != 1 || options.Networks[0] != "vpc" {
		t.Fatalf("options: %+v %v", options, err)
	}
	if len(options.NodeSizeFacts) != 1 || options.NodeSizeFacts[0].CPUs != 2 || options.NodeSizeFacts[0].HourlyUSD != nil {
		t.Fatalf("facts: %+v", options.NodeSizeFacts)
	}
	if options.Digest == "" || time.Since(options.CheckedAt) > time.Minute {
		t.Fatal("missing freshness evidence")
	}
}
func TestDOKSAccountMismatch(t *testing.T) {
	c := doksFixture(t, func(_ http.ResponseWriter, r *http.Request) { t.Errorf("access after mismatch: %s", r.URL.Path) })
	access := doksAccess()
	access.AccountID = "wrong"
	if _, err := c.ListClusters(t.Context(), access, ""); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
	if _, err := c.BootstrapCluster(t.Context(), access, provider.ClusterRef{ID: "id", AccountID: "wrong"}, time.Minute); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
}
func TestDOKSPaginationLoop(t *testing.T) {
	c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"kubernetes_clusters":[],"links":{"pages":{"next":"https://api.digitalocean.com/v2/kubernetes/clusters?page=1"}}}`)
	})
	if _, err := c.ListClusters(t.Context(), doksAccess(), ""); !errors.Is(err, provider.ErrInfrastructurePagination) {
		t.Fatal(err)
	}
}
func TestDOKSDiscoveryNoMutation(t *testing.T) {
	c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"kubernetes_clusters":[{"id":"cluster","name":"production","region":"nyc1","created_at":"2026-01-01T00:00:00Z","version":"1.34","status":{"state":"running"}}],"links":{"pages":{"next":"https://api.digitalocean.com/v2/kubernetes/clusters?page=2"}}}`)
	})
	page, err := c.ListClusters(t.Context(), doksAccess(), "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "production" || page.NextCursor != "2" {
		t.Fatalf("incompleteness hidden: %+v %v", page, err)
	}
}
func TestDOKSBootstrapExpiryAndRedaction(t *testing.T) {
	expiry := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"server": "https://cluster.example", "certificate_authority_data": "Y2E=", "token": "private-token", "expires_at": expiry})
	})
	access, err := c.BootstrapCluster(t.Context(), doksAccess(), provider.ClusterRef{ID: "cluster", AccountID: "account"}, 10*time.Minute)
	if err != nil || !access.ExpiresAt.Equal(expiry) {
		t.Fatalf("expiry: %s %v", access.ExpiresAt, err)
	}
	b, _ := json.Marshal(access)
	if strings.Contains(string(b)+fmt.Sprintf("%+v", access), "private-token") {
		t.Fatal("bootstrap leaked")
	}
}
func TestDOKSErrors(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"message":"secret-token"}`)
			})
			_, err := c.ListClusters(t.Context(), doksAccess(), "")
			if err == nil || strings.Contains(err.Error(), "secret-token") {
				t.Fatal(err)
			}
		})
	}
	c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{"broken":"secret-token"`) })
	if _, err := c.ListClusters(t.Context(), doksAccess(), ""); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListClusters(ctx, doksAccess(), ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDOKSBootstrapRejectsUnprovenExpiry(t *testing.T) {
	for _, expiry := range []string{"", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)} {
		t.Run(expiry, func(t *testing.T) {
			c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				response := map[string]any{"server": "https://cluster.example", "certificate_authority_data": "Y2E=", "token": "private-token"}
				if expiry != "" {
					response["expires_at"] = expiry
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			access, err := c.BootstrapCluster(t.Context(), doksAccess(), provider.ClusterRef{ID: "cluster", AccountID: "account"}, 10*time.Minute)
			if err == nil || access.Token != "" || strings.Contains(err.Error(), "private-token") {
				t.Fatal("unproven expiry accepted or secret leaked")
			}
		})
	}
}
func TestDOKSPaginationBounds(t *testing.T) {
	for _, cursor := range []string{"0", "101", "-1", "https://remote", "01"} {
		c := doksFixture(t, func(_ http.ResponseWriter, _ *http.Request) { t.Fatal("invalid cursor reached provider") })
		if _, err := c.ListClusters(t.Context(), doksAccess(), cursor); !errors.Is(err, provider.ErrInfrastructurePagination) {
			t.Fatal(err)
		}
	}
	for _, body := range []string{`{}`, `{"kubernetes_clusters":null}`, `{"kubernetes_clusters":[],"links":{"pages":{"next":"?page=101"}}}`} {
		c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) })
		if _, err := c.ListClusters(t.Context(), doksAccess(), "100"); !errors.Is(err, provider.ErrInfrastructurePagination) {
			t.Fatal(err)
		}
	}
}

func TestDOKSGetClusterIdentity(t *testing.T) {
	c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"kubernetes_cluster":{"id":"cluster","name":"production","region":"nyc1","created_at":"2026-01-01T00:00:00Z","version":"1.34","status":{"state":"running"},"node_pools":[{"name":"workers","size":"doks","count":2,"auto_scale":true,"min_nodes":2,"max_nodes":4}]}}`)
	})
	ref := provider.ClusterRef{ID: "cluster", AccountID: "account", Region: "nyc1", Incarnation: "2026-01-01T00:00:00Z"}
	observation, err := c.GetCluster(t.Context(), doksAccess(), ref)
	if err != nil || observation.Name != "production" || len(observation.Pools) != 1 || !observation.Pools[0].Autoscale || observation.Pools[0].Max != 4 {
		t.Fatalf("observation: %+v %v", observation, err)
	}
	ref.Incarnation = "2025-01-01T00:00:00Z"
	if _, err := c.GetCluster(t.Context(), doksAccess(), ref); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
}
