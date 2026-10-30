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

type memoryEdges struct {
	*overviewStore
	EdgeStore
	state    *EdgeState
	finished []string
}

func (s *memoryEdges) FinishEdgeInvalidation(_ context.Context, _ EdgeInvalidation, message string) error {
	s.finished = append(s.finished, message)
	return nil
}

func (s *memoryEdges) Edge(_ context.Context, org, route string) (*EdgeState, error) {
	if s.state == nil || org != s.state.OrgID || route != s.state.RouteID {
		return nil, ErrNotFound
	}
	v := *s.state
	return &v, nil
}
func (s *memoryEdges) AcceptEdge(_ context.Context, _ *Route, state *EdgeState) error {
	state.Revision++
	v := *state
	s.state = &v
	return nil
}
func (s *memoryEdges) SaveEdge(_ context.Context, state *EdgeState) error {
	v := *state
	s.state = &v
	return nil
}
func (s *memoryEdges) PublishEdge(ctx context.Context, route *Route, state *EdgeState) error {
	for i, r := range s.routes {
		if r.ID == route.ID {
			v := *route
			v.Revision++
			s.routes[i] = &v
		}
	}
	return s.SaveEdge(ctx, state)
}

type edgeLocks struct{ Store }

func (edgeLocks) WithOperationLock(_ context.Context, _ string, fn func() error) error { return fn() }

type edgeCredentials struct{}

func (edgeCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return &connections.Connection{OrgID: "org", Cloud: "cloudflare", Generation: 1, Details: map[string]string{"account_id": "account"}}, []byte("token"), nil
}

type testEdgeAdapter struct {
	routeDNSAdapter
	policyCalls, purges int
	failPurge           bool
	record              provider.DNSRecord
}

func (a *testEdgeAdapter) ApplyEdge(context.Context, provider.InfrastructureCredential, provider.DNSZone, string, string, provider.EdgePolicy, []string) error {
	a.policyCalls++
	return nil
}
func (a *testEdgeAdapter) PurgeEdge(context.Context, provider.InfrastructureCredential, provider.DNSZone, string, []string) error {
	a.purges++
	if a.failPurge {
		return errors.New("purge unavailable")
	}
	return nil
}
func (a *testEdgeAdapter) VerifyEdgeDNS(context.Context, provider.InfrastructureCredential, provider.DNSZone, provider.DNSRecord) error {
	return nil
}
func (a *testEdgeAdapter) UpsertRecord(_ context.Context, _ provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	if a.record == req.Record {
		return a.record, nil
	}
	if a.record != *req.Expected {
		return provider.DNSRecord{}, provider.ErrInfrastructureDrift
	}
	a.record = req.Record
	return a.record, nil
}

func edgeFixture(t *testing.T) (*Service, *memoryEdges, *testEdgeAdapter, security.Principal) {
	t.Helper()
	s, base := overviewFixture()
	z := base.zones[0]
	z.Name = "example.com"
	z.Mode = "managed"
	z.Provider = "cloudflare"
	z.ConnectionID = "cf"
	z.ConnectionGeneration = 1
	z.AccountID = "account"
	z.RecordOwnership = true
	z.ProviderZone = provider.DNSZone{ID: "zone", Name: "example.com", AccountID: "account", Verified: true}
	z.ProofName = "_loomplane.example.com"
	base.routes[0].Input.Hostname = "api.example.com"
	receipt := provider.DNSRecord{ID: "dns", ZoneID: "zone", Name: "api", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route:route_a"}
	base.routes[0].Receipt = &receipt
	st := &memoryEdges{overviewStore: base}
	a := &testEdgeAdapter{record: receipt}
	s.Domains = st
	s.Store = edgeLocks{}
	s.Connections = edgeCredentials{}
	s.Resolver = proofResolver{[]string{"proof"}}
	s.Adapters = NewAdapters()
	if err := s.Adapters.RegisterDNS("cloudflare", a); err != nil {
		t.Fatal(err)
	}
	return s, st, a, security.Principal{OrgID: "org", UserID: "dev"}
}

func TestEdgePersistsFailureAndRetriesAfterDNSChanged(t *testing.T) {
	s, st, a, p := edgeFixture(t)
	policy := provider.EdgePolicy{Enabled: true, InvalidateOnDeploy: true, Rules: []provider.EdgeRule{{PathPrefix: "/assets/", Mode: "public", EdgeTTL: 3600}}}
	state, err := s.UpdateEdge(t.Context(), p, "shop", "route_a", EdgeInput{Policy: policy})
	if err != nil || state.Status != "pending" || a.policyCalls != 0 {
		t.Fatalf("accept: %+v %v", state, err)
	}
	a.failPurge = true
	if err = s.ReconcileEdge(t.Context(), "org", "route_a"); err == nil {
		t.Fatal("expected purge failure")
	}
	if st.state.Status != "failed" || !a.record.Proxied || st.routes[0].Receipt.Proxied {
		t.Fatal("lost response fixture did not hold both states")
	}
	if _, err = s.UpdateEdge(t.Context(), p, "shop", "route_a", EdgeInput{Revision: state.Revision, Policy: policy}); !errors.Is(err, ErrConflict) {
		t.Fatal("replaced unfinished operation", err)
	}
	a.failPurge = false
	if _, err = s.RetryEdge(t.Context(), p, "shop", "route_a", state.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileEdge(t.Context(), "org", "route_a"); err != nil {
		t.Fatal(err)
	}
	if st.state.Status != "ready" || !st.routes[0].Receipt.Proxied || st.state.LastPurgeAt.IsZero() {
		t.Fatal(st.state)
	}
	if _, err = s.UpdateEdge(t.Context(), p, "shop", "route_a", EdgeInput{Revision: st.state.Revision}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileEdge(t.Context(), "org", "route_a"); err != nil {
		t.Fatal(err)
	}
	if st.state.Status != "disabled" || st.routes[0].Receipt.Proxied {
		t.Fatal("disable failed")
	}
}

func TestEdgeAuthorizationScopeAndStaleRevision(t *testing.T) {
	s, st, a, p := edgeFixture(t)
	for _, actor := range []security.Principal{{OrgID: "org", UserID: "dev", ReadOnly: true}, {OrgID: "other", UserID: "dev"}, {OrgID: "org", UserID: "outsider"}} {
		if _, err := s.UpdateEdge(t.Context(), actor, "shop", "route_a", EdgeInput{}); err == nil {
			t.Fatal("unauthorized", actor)
		}
	}
	if _, err := s.UpdateEdge(t.Context(), p, "docs", "route_a", EdgeInput{}); err == nil {
		t.Fatal("cross-project route")
	}
	if _, err := s.UpdateEdge(t.Context(), p, "shop", "route_a", EdgeInput{Revision: 9}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if a.policyCalls != 0 {
		t.Fatal("effect before authorization")
	}
	if _, err := s.UpdateEdge(t.Context(), p, "shop", "route_a", EdgeInput{Policy: provider.EdgePolicy{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	st.zones[0].ProjectIDs = nil
	if err := s.ReconcileEdge(t.Context(), "org", "route_a"); err == nil {
		t.Fatal("revoked grant accepted")
	}
	if a.policyCalls != 0 || st.state.Status != "failed" {
		t.Fatal("revoked grant reached cloud")
	}
}

func TestEdgePurgeUsesSavedPolicyAndExactHost(t *testing.T) {
	s, st, a, p := edgeFixture(t)
	st.state = &EdgeState{OrgID: "org", ProjectID: "shop", RouteID: "route_a", Revision: 3, Status: "ready", Policy: provider.EdgePolicy{Enabled: true}, Principal: p, AppliedAt: time.Now()}
	if _, err := s.PurgeEdge(t.Context(), p, "shop", "route_a", EdgePurgeInput{Revision: 3, URLs: []string{"https://other.example.com/"}}); err == nil {
		t.Fatal("foreign purge")
	}
	if _, err := s.PurgeEdge(t.Context(), p, "shop", "route_a", EdgePurgeInput{Revision: 3, URLs: []string{"https://api.example.com/logo.svg"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileEdge(t.Context(), "org", "route_a"); err != nil {
		t.Fatal(err)
	}
	if a.policyCalls != 0 || a.purges != 1 {
		t.Fatal("purge reconfigured policy")
	}
}

func TestEdgeFailedEnableCanBeDisabled(t *testing.T) {
	s, st, a, p := edgeFixture(t)
	if _, err := s.UpdateEdge(t.Context(), p, "shop", "route_a", EdgeInput{Policy: provider.EdgePolicy{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	a.failPurge = true
	if err := s.ReconcileEdge(t.Context(), "org", "route_a"); err == nil {
		t.Fatal("expected purge failure")
	}
	a.failPurge = false
	if _, err := s.UpdateEdge(t.Context(), p, "shop", "route_a", EdgeInput{Revision: st.state.Revision}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileEdge(t.Context(), "org", "route_a"); err != nil {
		t.Fatal(err)
	}
	if st.state.Status != "disabled" || a.record.Proxied {
		t.Fatal("failed enable could not be disabled")
	}
}

func TestEdgeDeploymentInvalidationRetriesAndRechecksAccess(t *testing.T) {
	s, st, a, p := edgeFixture(t)
	st.state = &EdgeState{OrgID: "org", ProjectID: "shop", RouteID: "route_a", Revision: 3, Status: "ready", Policy: provider.EdgePolicy{Enabled: true, InvalidateOnDeploy: true}, Principal: p}
	job := EdgeInvalidation{Org: "org", Project: "shop", Route: "route_a", Deployment: "deploy"}
	a.failPurge = true
	if err := s.invalidateEdgeDeployment(t.Context(), st, job); err == nil || st.state.InvalidationError == "" || st.finished[0] == "" {
		t.Fatal("purge failure was not retained", err)
	}
	a.failPurge = false
	if err := s.invalidateEdgeDeployment(t.Context(), st, job); err != nil {
		t.Fatal(err)
	}
	if st.state.InvalidationError != "" || st.state.LastDeploymentID != "deploy" || st.state.LastPurgeAt.IsZero() || st.finished[1] != "" {
		t.Fatal("purge success was not recorded")
	}
	st.zones[0].ProjectIDs = nil
	if err := s.invalidateEdgeDeployment(t.Context(), st, job); err == nil || a.purges != 2 {
		t.Fatal("revoked grant reached Cloudflare", err)
	}
	st.state.Policy.InvalidateOnDeploy = false
	if err := s.invalidateEdgeDeployment(t.Context(), st, job); err != nil || a.purges != 2 || st.finished[len(st.finished)-1] != "" {
		t.Fatal("disabled invalidation was not skipped", err)
	}
}
