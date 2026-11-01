package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterLifecycleRoutesDenyProjectMember(t *testing.T) {
	svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: false}, Clouds: clusterCloudStore{}, Clusters: clusterRouteStore{}, ManagedClusters: config.ManagedClustersConfig{Enabled: true}})
	r := forge.NewRouter()
	if err := (&Extension{svc: svc}).clusterRoutes(r, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{{"GET", "/clusters/resource/capacity", ""}, {"POST", "/clusters/resource/capacity/refresh", ""}, {"POST", "/clusters/resource/retirement/preview", ""}, {"POST", "/clusters/preview", `{"kind":"node-pool-update"}`}, {"POST", "/clusters/preview", `{"kind":"runtime-access-upgrade"}`}} {
		req := httptest.NewRequestWithContext(security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "member"}), tc.method, "/api/v1/clouds/cloud"+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatalf("%s: %d %s", tc.path, out.Code, out.Body)
		}
	}
}
func TestPublicMoneyExactAndPrivateBudget(t *testing.T) {
	n := int64(math.MaxInt64)
	v := &infra.ClusterOperation{MoneyEstimate: &infra.MoneyEstimate{HourlyMicroUSD: n, HorizonMicroUSD: n, Components: []infra.MoneyComponent{{Name: "nodes", Category: "nodes", Identity: "private-source", Quantity: n, HourlyMicroUSD: &n}}}, Input: infra.ClusterInput{BudgetReview: &infra.BudgetReview{AccountScope: "private-membership", Estimate: infra.MoneyEstimate{HourlyMicroUSD: n}}}}
	raw, err := json.Marshal(publicClusterOperation(v))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") || !strings.Contains(string(raw), `"hourly_micro_usd":"9223372036854775807"`) {
		t.Fatalf("unsafe money: %s", raw)
	}
	for _, bad := range []string{"", "-1", "+1", "1.5", "9223372036854775808", "1e6", " 1"} {
		if _, parseErr := privatePolicy(CapacityPolicy{Budget: &client.CapacityBudget{MaxHourlyMicroUSD: bad, MaxHorizonMicroUSD: "1"}}); parseErr == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	got, err := privatePolicy(CapacityPolicy{Budget: &client.CapacityBudget{MaxHourlyMicroUSD: "9223372036854775807", MaxHorizonMicroUSD: "1", HorizonSeconds: 60}})
	if err != nil || got.Budget.MaxHourlyMicroUSD != n {
		t.Fatalf("exact budget: %+v %v", got, err)
	}
}

type capacityReadStore struct {
	clusterRouteStore
	reads int
}

func (s *capacityReadStore) GetManagedResource(_ context.Context, org, id string) (*infra.ManagedResource, error) {
	return &infra.ManagedResource{ID: id, OrgID: org, CloudID: "cloud", Kind: "cluster"}, nil
}
func (s *capacityReadStore) ReadClusterCapacity(context.Context, string, string, string) (*infra.ClusterCapacitySnapshot, error) {
	s.reads++
	return &infra.ClusterCapacitySnapshot{ResourceID: "resource", ObservationRevision: 2, CheckedAt: time.Now(), WorkloadsComplete: true}, nil
}
func TestClusterCapacityGetDoesNotIssueCredentials(t *testing.T) {
	store := &capacityReadStore{}
	svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: true}, Clouds: clusterCloudStore{}, Clusters: store})
	r := forge.NewRouter()
	if err := (&Extension{svc: svc}).clusterRoutes(r, nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "operator"}), "GET", "/api/v1/clouds/cloud/clusters/resource/capacity", nil)
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Code != 200 || store.reads != 1 {
		t.Fatalf("read: %d %s", out.Code, out.Body)
	}
}

func TestClusterAccessUpgradeProblem(t *testing.T) {
	r := forge.NewRouter()
	if err := r.GET("/problem", func(c forge.Context) error {
		_, err := clusterBlockResult(c, errors.Join(kube.ErrCleanupPermissionsUnavailable, errors.New("private-attachment")))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequestWithContext(t.Context(), "GET", "/problem", nil))
	if out.Code != 422 || !strings.Contains(out.Body.String(), "runtime_access_upgrade_required") || strings.Contains(out.Body.String(), "private-") {
		t.Fatalf("problem: %d %s", out.Code, out.Body)
	}
}

func TestPublicClusterEmptyCollections(t *testing.T) {
	observation := publicObservation(&provider.ClusterObservation{})
	if observation.Pools == nil || observation.Conditions == nil || publicSpec(provider.ClusterSpec{}).NodePools == nil {
		t.Fatal("public cluster collections must encode as arrays")
	}
}
