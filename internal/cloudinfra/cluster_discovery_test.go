package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type doksCredentials struct {
	reads       int
	org, status string
	generation  int64
}

func (c *doksCredentials) Credential(_ context.Context, _, id string) (*connections.Connection, []byte, error) {
	c.reads++
	return &connections.Connection{ID: id, OrgID: c.org, Cloud: "digitalocean", Status: c.status, Generation: c.generation, Details: map[string]string{"account_uuid": "account"}}, []byte("token"), nil
}

type doksReader struct {
	reads   int
	checked time.Time
	account string
}

func (r *doksReader) Options(context.Context, provider.AccountAccess, string) (provider.ClusterOptions, error) {
	r.reads++
	return provider.ClusterOptions{CheckedAt: r.checked, Digest: "digest"}, nil
}
func (r *doksReader) ListClusters(context.Context, provider.AccountAccess, string) (provider.ClusterPage, error) {
	r.reads++
	return provider.ClusterPage{Items: []provider.ClusterObservation{{Ref: provider.ClusterRef{AccountID: r.account}}}}, nil
}
func (*doksReader) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	return provider.ClusterObservation{}, nil
}
func TestDOKSDiscoveryAuthorization(t *testing.T) {
	auth := overviewAuth{"manager": {{security.OrganizationCloudManage, "org"}}}
	creds := &doksCredentials{org: "org", status: connections.StatusReady, generation: 1}
	reader := &doksReader{checked: time.Now(), account: "account"}
	adapters := NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", reader); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{Connections: creds, Adapters: adapters, Authz: auth, Clouds: capacityAPIClouds{}})
	p := security.Principal{UserID: "manager", OrgID: "org"}
	if _, err := svc.DiscoverClusters(t.Context(), p, "conn", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClusterOptions(t.Context(), p, "cloud", "conn", "nyc1"); err != nil {
		t.Fatal(err)
	}
	delete(auth, "manager")
	if _, err := svc.DiscoverClusters(t.Context(), p, "conn", ""); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := svc.ClusterOptions(t.Context(), p, "cloud", "conn", "nyc1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	if creds.reads != 2 || reader.reads != 2 {
		t.Fatal("revoked grant reached credentials or provider")
	}
	auth["manager"] = []grant{{security.OrganizationCloudManage, "org"}}
	creds.org = "other"
	if _, err := svc.DiscoverClusters(t.Context(), p, "conn", ""); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
	if reader.reads != 2 {
		t.Fatal("cross-org credential reached provider")
	}
	creds.org = "org"
	creds.status = "failed"
	if _, err := svc.DiscoverClusters(t.Context(), p, "conn", ""); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
	creds.status = connections.StatusReady
	reader.account = "other"
	if _, err := svc.DiscoverClusters(t.Context(), p, "conn", ""); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal(err)
	}
}
func TestDOKSOptionsFreshness(t *testing.T) {
	now := time.Now()
	reader := &doksReader{checked: now.Add(-5 * time.Minute), account: "account"}
	adapters := NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", reader); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{Connections: &doksCredentials{org: "org", status: connections.StatusReady, generation: 1}, Adapters: adapters, Authz: cleanupTestAuth{}, Clouds: capacityAPIClouds{}})
	svc.now = func() time.Time { return now }
	p := security.Principal{UserID: "manager", OrgID: "org"}
	for _, checked := range []time.Time{now.Add(-5 * time.Minute), now.Add(time.Second), {}} {
		reader.checked = checked
		if _, err := svc.ClusterOptions(t.Context(), p, "cloud", "conn", "nyc1"); !errors.Is(err, provider.ErrInfrastructureInvalid) {
			t.Fatalf("accepted %s: %v", checked, err)
		}
	}
	reader.checked = now.Add(-5*time.Minute + time.Second)
	if _, err := svc.ClusterOptions(t.Context(), p, "cloud", "conn", "nyc1"); err != nil {
		t.Fatal(err)
	}
}
func TestDOKSInstalledReadCapabilities(t *testing.T) {
	adapters := BuiltinAdapters(Endpoints{})
	for _, capability := range []provider.InfrastructureCapability{provider.ClusterDiscovery, provider.ClusterOptionsRead, provider.ClusterBootstrapAccess, provider.ClusterCreation, provider.ClusterMutationObservation} {
		if _, err := adapters.Cluster("digitalocean", capability); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := adapters.Cluster("digitalocean", provider.ClusterCreation); err != nil {
		t.Fatal(err)
	}
}

func TestClusterOptionsPublishProviderIdentityForReview(t *testing.T) {
	reader := &doksReader{checked: time.Now(), account: "account"}
	adapters := NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", reader); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{Connections: &doksCredentials{org: "org", status: connections.StatusReady, generation: 7}, Adapters: adapters, Authz: cleanupTestAuth{}, Clouds: capacityAPIClouds{}})
	options, err := svc.ClusterOptions(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "cloud", "conn", "nyc1")
	if err != nil {
		t.Fatal(err)
	}
	if options.AccountID != "account" || options.ConnectionGeneration != 7 || options.Digest != "digest" {
		t.Fatalf("review identity %+v", options)
	}
	if options.AccountID == connections.AccountIdentity("digitalocean", "", map[string]string{"account_uuid": "account"}) {
		t.Fatal("provider account replaced with generic connection fingerprint")
	}
}
