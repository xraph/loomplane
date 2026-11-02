package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type guardedDOKS struct {
	*clusterProvider
	client *digitalocean.Client
}

func (p *guardedDOKS) CreateCluster(ctx context.Context, a provider.AccountAccess, spec provider.ClusterSpec) (provider.ClusterMutation, error) {
	return p.client.CreateCluster(ctx, a, spec)
}
func (p *guardedDOKS) BootstrapCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, ttl time.Duration) (provider.BootstrapAccess, error) {
	return p.client.BootstrapCluster(ctx, a, ref, ttl)
}
func (*guardedDOKS) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	o := clusterObservation()
	o.Endpoint = "https://cluster.test"
	o.Ref.Incarnation = "2026-10-01T00:00:00Z"
	return o, nil
}

func TestManagedClusterAuthorityAfterAdapterPreflight(t *testing.T) {
	for _, effect := range []string{"purchase", "bootstrap"} {
		for _, loss := range []string{"membership", "cancellation", "retained"} {
			t.Run(effect+"/"+loss, func(t *testing.T) {
				s, store, fake, auth, principal, in := clusterFixture(t)
				reached, release := make(chan struct{}), make(chan struct{})
				var reads, effects atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost || r.URL.Path == "/v2/kubernetes/clusters/remote/credentials" {
						effects.Add(1)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if r.URL.Path == "/v2/account" {
						if reads.Add(1) == 1 {
							close(reached)
							<-release
						}
						_, _ = fmt.Fprint(w, `{"account":{"uuid":"account"}}`)
						return
					}
					_, _ = fmt.Fprint(w, `{"kubernetes_cluster":{"id":"remote","region":"nyc1","created_at":"2026-10-01T00:00:00Z","endpoint":"https://cluster.test","status":{"state":"running"},"node_pools":[]}}`)
				}))
				defer server.Close()
				adapters := NewAdapters()
				if err := adapters.RegisterCluster("digitalocean", &guardedDOKS{clusterProvider: fake, client: digitalocean.New(server.URL)}, provider.ClusterCreation, provider.ClusterMutationObservation, provider.ClusterBootstrapAccess); err != nil {
					t.Fatal(err)
				}
				s.Adapters = adapters
				o := acceptCluster(t, s, principal, in)
				done := make(chan error, 1)
				if effect == "purchase" {
					go func() { done <- s.ReconcileClusterOperation(t.Context(), principal.OrgID, o.ID, nil) }()
				} else {
					var err error
					o, err = store.LeaseClusterOperation(t.Context(), principal.OrgID, o.ID, "worker", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					o.ResourceID = "cluster"
					o.Enrollment = &ClusterEnrollment{ResourceRevision: 1}
					observation, _ := (&guardedDOKS{}).GetCluster(t.Context(), provider.AccountAccess{}, provider.ClusterRef{})
					resource := &ManagedResource{ID: "cluster", OrgID: principal.OrgID, CloudID: o.CloudID, ConnectionID: in.ConnectionID, AccountID: in.AccountID, Revision: 1, Observation: &observation}
					store.resource = resource
					go func() { _, err := s.clusterBootstrap(t.Context(), o, resource); done <- err }()
				}
				select {
				case <-reached:
				case <-time.After(5 * time.Second):
					close(release)
					t.Fatal("provider identity read not reached")
				}
				switch loss {
				case "membership":
					auth.revoked = true
				case "cancellation":
					if _, err := store.ControlClusterOperation(t.Context(), principal.OrgID, o.CloudID, o.ID, true); err != nil {
						t.Fatal(err)
					}
				}
				close(release)
				select {
				case err := <-done:
					if effect == "bootstrap" && loss != "retained" {
						want := ErrConflict
						if loss == "membership" {
							want = security.ErrNotMember
						}
						if !errors.Is(err, want) {
							t.Fatalf("bootstrap did not reach final authority guard: %v", err)
						}
					}
				case <-time.After(5 * time.Second):
					t.Fatal("held call did not finish")
				}
				want := int32(0)
				if loss == "retained" {
					want = 1
				}
				if effects.Load() != want {
					t.Fatalf("provider effects = %d, want %d", effects.Load(), want)
				}
				if effect == "purchase" && store.op.Steps[0].State != "attempted" {
					t.Fatal("lost durable no-repeat marker")
				}
			})
		}
	}
}
