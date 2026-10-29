package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestDOKSCreateEmptyListIsNotRetryPermission(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/v2/account" {
			_, _ = fmt.Fprint(w, `{"account":{"uuid":"account"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"kubernetes_clusters":[]}`)
	}))
	defer server.Close()
	spec := provider.ClusterSpec{OperationID: "operation", Name: "cluster", Region: "nyc1", Version: "v1", NetworkID: "network", NodePools: []provider.NodePoolSpec{{Name: "workers", Size: "s-2", Count: 1}}, ReconcileOnly: true}
	_, err := New(server.URL).CreateCluster(t.Context(), doksAccess(), spec)
	if !errors.Is(err, provider.ErrInfrastructureAmbiguous) || posts != 0 {
		t.Fatalf("empty listing: posts=%d err=%v", posts, err)
	}
}

func TestDOKSMutationRequiresExactRequestEvidence(t *testing.T) {
	spec := provider.ClusterSpec{OperationID: "operation", Name: "same-name", Region: "nyc1", Version: "v1", NetworkID: "network", NodePools: []provider.NodePoolSpec{{Name: "workers", Size: "s-2", Count: 1}}}
	digest := provider.ClusterRequestDigest(spec)
	for _, tc := range []struct {
		name, tags string
		copies     int
		want       error
	}{
		{"name-only", `[]`, 1, provider.ErrInfrastructureAmbiguous},
		{"wrong-request", `["loomplane-operation:operation","loomplane-request:other"]`, 1, provider.ErrInfrastructureAmbiguous},
		{"exact", `["loomplane-operation:operation","loomplane-request:` + digest + `"]`, 1, nil},
		{"duplicates", `["loomplane-operation:operation","loomplane-request:` + digest + `"]`, 2, provider.ErrInfrastructureIdentity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				items := make([]string, 0, tc.copies)
				for i := range tc.copies {
					items = append(items, fmt.Sprintf(`{"id":"id-%d","name":"same-name","region":"nyc1","created_at":"2026-10-29T00:00:00Z","status":{"state":"running"},"tags":%s}`, i, tc.tags))
				}
				_, _ = fmt.Fprintf(w, `{"kubernetes_clusters":[%s]}`, strings.Join(items, ","))
			})
			_, err := c.ObserveClusterMutation(t.Context(), doksAccess(), provider.ClusterMutation{OperationID: spec.OperationID, RequestDigest: digest, Ref: provider.ClusterRef{AccountID: "account", Region: "nyc1"}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}
func TestDOKSCreateBoundedAndRetryAfter(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/account" {
			_, _ = fmt.Fprint(w, `{"account":{"uuid":"account"}}`)
			return
		}
		posts++
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"message":"secret must not survive"}`)
	}))
	defer server.Close()
	c := New(server.URL)
	spec := provider.ClusterSpec{OperationID: "operation", Name: "cluster", Region: "nyc1", Version: "v1", NetworkID: "network", NodePools: []provider.NodePoolSpec{{Name: "workers", Size: "s-2", Count: 1}}}
	_, err := c.CreateCluster(t.Context(), doksAccess(), spec)
	var api *provider.InfrastructureAPIError
	if !errors.As(err, &api) || api.RetryAfter != 2*time.Minute || posts != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("rate limit: %d %v", posts, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = c.CreateCluster(ctx, doksAccess(), spec); !errors.Is(err, context.Canceled) || posts != 1 {
		t.Fatalf("cancelled call: %d %v", posts, err)
	}
}
