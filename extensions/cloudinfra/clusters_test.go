package cloudinfra

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

func TestClusterPublicOperationOmitsPrivateEnrollment(t *testing.T) {
	raw, err := json.Marshal(publicClusterOperation(&infra.ClusterOperation{ID: "operation", Enrollment: &infra.ClusterEnrollment{VaultKey: "secret-vault-key", RetiredVaultKeys: []string{"retired-secret"}}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-vault-key", "retired-secret", "vault_key", "lease_owner", "principal"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("public operation contains %q", secret)
		}
	}
}

type clusterCloudStore struct{ cloudprofiles.Store }

func (clusterCloudStore) GetProfile(_ context.Context, org, _, id string) (*cloudprofiles.Profile, error) {
	return &cloudprofiles.Profile{ID: id, OrgID: org}, nil
}

type clusterRouteStore struct{ infra.ClusterStore }

func (clusterRouteStore) GetManagedResource(_ context.Context, org, _ string) (*infra.ManagedResource, error) {
	return &infra.ManagedResource{ID: "resource", OrgID: org, CloudID: "other-cloud", Kind: "cluster"}, nil
}
func (clusterRouteStore) GetClusterOperation(_ context.Context, org, cloud, id string) (*infra.ClusterOperation, error) {
	return &infra.ClusterOperation{ID: id, OrgID: org, CloudID: cloud, Digest: "exact-review", Principal: security.Principal{OrgID: org, UserID: "operator"}, Enrollment: &infra.ClusterEnrollment{VaultKey: "private-sentinel"}, Error: "private-sentinel"}, nil
}
func TestClusterRoutesScopeAndReview(t *testing.T) {
	serve := func(allow, signed bool, method, path, body string) *httptest.ResponseRecorder {
		svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: allow}, Clouds: clusterCloudStore{}, Clusters: clusterRouteStore{}, ManagedClusters: config.ManagedClustersConfig{Enabled: true}})
		r := forge.NewRouter()
		if err := (&Extension{svc: svc}).clusterRoutes(r, nil); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if signed {
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	for _, path := range []string{"/api/v1/clouds/cloud/clusters", "/api/v1/clouds/cloud/clusters/resource", "/api/v1/clouds/cloud/cluster-operations", "/api/v1/clouds/cloud/cluster-operations/operation", "/api/v1/clouds/cloud/cluster-options/account", "/api/v1/connections/account/clusters"} {
		if out := serve(false, false, "GET", path, ""); out.Code != 401 {
			t.Fatalf("unauthenticated %s: %d %s", path, out.Code, out.Body)
		}
		if out := serve(false, true, "GET", path, ""); out.Code != 403 {
			t.Fatalf("project caller %s: %d %s", path, out.Code, out.Body)
		}
	}
	if out := serve(true, true, "GET", "/api/v1/clouds/cloud/clusters/resource", ""); out.Code != 404 {
		t.Fatalf("cross-cloud resource %d %s", out.Code, out.Body)
	}
	if out := serve(true, true, "POST", "/api/v1/clouds/cloud/cluster-operations/operation/apply", `{"review":"stale"}`); out.Code != 409 {
		t.Fatalf("stale review %d %s", out.Code, out.Body)
	}
	if out := serve(true, true, "GET", "/api/v1/clouds/cloud/cluster-operations/operation", ""); out.Code != 200 || strings.Contains(out.Body.String(), "private-sentinel") {
		t.Fatalf("public operation %d %s", out.Code, out.Body)
	}
}

type publicRouteAuth struct{ overviewAuthz }

func (a publicRouteAuth) RequireCurrentMembership(ctx context.Context, p security.Principal) error {
	return a.Require(ctx, p, security.OrganizationCloudManage, p.OrgID)
}

type publicRouteCredentials struct{}

func (publicRouteCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return &connections.Connection{ID: "account", OrgID: "org", Cloud: "digitalocean", Status: connections.StatusReady, Generation: 1, Details: map[string]string{"account_uuid": "provider-account"}}, []byte("private-credential"), nil
}

type publicRouteProvider struct{ provider.ClusterReader }

func (publicRouteProvider) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	return provider.ClusterObservation{}, nil
}

type publicOperationStore struct{ infra.ClusterLifecycleStore }

func (publicOperationStore) GetClusterOperation(context.Context, string, string, string) (*infra.ClusterOperation, error) {
	return publicSentinelOperation(), nil
}
func (publicOperationStore) ControlClusterOperation(context.Context, string, string, string, bool) (*infra.ClusterOperation, error) {
	return publicSentinelOperation(), nil
}
func (publicOperationStore) ListClusterOperations(context.Context, string, string, string, int) (infra.ClusterOperationPage, error) {
	return infra.ClusterOperationPage{Items: []*infra.ClusterOperation{publicSentinelOperation()}}, nil
}
func (publicOperationStore) CreateClusterPreview(_ context.Context, o *infra.ClusterOperation) error {
	o.Enrollment = publicSentinelOperation().Enrollment
	return nil
}
func publicSentinelOperation() *infra.ClusterOperation {
	return &infra.ClusterOperation{ID: "operation", CloudID: "cloud", OrgID: "org", Digest: "exact-review", Status: "accepted", Principal: security.Principal{OrgID: "org", UserID: "operator"}, Input: infra.ClusterInput{CloudRevision: 0, ConnectionID: "account", Provider: "digitalocean", AccountID: "provider-account", ConnectionGeneration: 1, ManagementEvidenceID: "private-evidence"}, Enrollment: &infra.ClusterEnrollment{VaultKey: "private-vault", RetiredVaultKeys: []string{"private-retired"}}, Error: "private-error", LeaseOwner: "private-lease"}
}
func TestClusterAllOperationResponsesUsePublicProjection(t *testing.T) {
	adapters := infra.NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", publicRouteProvider{}); err != nil {
		t.Fatal(err)
	}
	svc := infra.NewService(infra.Deps{Authz: publicRouteAuth{overviewAuthz{allow: true}}, Clouds: clusterCloudStore{}, Clusters: publicOperationStore{}, Connections: publicRouteCredentials{}, Adapters: adapters, ManagedClusters: config.ManagedClustersConfig{Enabled: true}})
	r := forge.NewRouter()
	if err := (&Extension{svc: svc}).clusterRoutes(r, nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/cluster-operations", "", 200}, {"GET", "/cluster-operations/operation", "", 200},
		{"POST", "/cluster-operations/operation/apply", `{"review":"exact-review"}`, 202},
		{"POST", "/cluster-operations/operation/retry", "", 202}, {"POST", "/cluster-operations/operation/cancel", "", 200},
		{"POST", "/clusters/preview", `{"kind":"adopt-external","connection_id":"account","provider":"digitalocean","account_id":"provider-account","connection_generation":1,"ref":{"id":"external","account_id":"provider-account","incarnation":"born","region":"nyc1"}}`, 200},
	} {
		req := httptest.NewRequestWithContext(t.Context(), test.method, "/api/v1/clouds/cloud"+test.path, strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, out.Code, out.Body)
		}
		for _, secret := range []string{"private-", "vault_key", "lease_owner", "management_evidence_id"} {
			if strings.Contains(out.Body.String(), secret) {
				t.Fatalf("%s leaked %s", test.path, secret)
			}
		}
	}
}

func TestClusterPublicSuccessIsNotAnError(t *testing.T) {
	for _, status := range []string{"accepted", "succeeded"} {
		got := publicClusterOperation(&infra.ClusterOperation{Status: status})
		raw, err := json.Marshal(got)
		if err != nil || !strings.Contains(string(raw), `"error":""`) {
			t.Fatalf("success must clear cached errors: %s, %v", raw, err)
		}
		if got.Error != "" {
			t.Fatalf("%s outcome presented as error: %s", status, got.Error)
		}
	}
}

func TestRuntimeAccessPublicReview(t *testing.T) {
	input := &provider.RuntimeAccessInput{AttachmentID: "attachment", RoleUID: "reviewed-role", Profile: provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true}}
	op := &infra.ClusterOperation{Kind: infra.RuntimeAccessUpgrade, Input: infra.ClusterInput{RuntimeAccess: input}, RuntimeAccess: &kubernetes.RuntimeAccessState{NetworkOutcome: "private-network-history"}}
	raw, err := json.Marshal(publicClusterOperation(op))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "reviewed-role") || strings.Contains(string(raw), "private-network-history") {
		t.Fatalf("public access review: %s", raw)
	}
}
