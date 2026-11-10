package cloudinfra

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

// removalStore is kubeRouteStore that finds an operation only by its own
// ID and counts the operations it accepts.
type removalStore struct {
	*kubeRouteStore
	accepted []string
}

func (s *removalStore) RouteOperation(_ context.Context, _ string, id string) (*RouteOperation, error) {
	if s.op.ID != id {
		return nil, ErrNotFound
	}
	op := s.op
	return &op, nil
}

func (s *removalStore) AcceptRoute(ctx context.Context, op *RouteOperation) error {
	s.accepted = append(s.accepted, op.ID)
	return s.kubeRouteStore.AcceptRoute(ctx, op)
}

func TestRouteRemovalIDIsStableAndDistinct(t *testing.T) {
	id := RouteRemovalID("plan/resource:gw--gateway", "route_k")
	if id != RouteRemovalID("plan/resource:gw--gateway", "route_k") {
		t.Fatal("the ID isn't stable")
	}
	for _, other := range []string{RouteRemovalID("plan/resource:gw--gateway", "route_j"), RouteRemovalID("other/resource:gw--gateway", "route_k"), ReleasePrepareID("plan/resource:gw--gateway", "route_k")} {
		if other == id {
			t.Fatal("two removals share an ID", id)
		}
	}
}

// routeRemovalFixture is a ready route_k with a DNS record, its store, and the
// removal request a reviewed delete of gw makes for it.
func routeRemovalFixture(t *testing.T) (*Service, *removalStore, *fakeRouteCluster, *routeDNSAdapter, RouteRemovalRequest) {
	t.Helper()
	svc, kube, cluster, dns := kubernetesRouteFixture(t)
	receipt := provider.DNSRecord{ID: "record_1", ZoneID: "zone", Name: "api", Type: "A", Value: "203.0.113.10", TTL: 60, Owner: "loomplane:route:route_k"}
	dns.records = []provider.DNSRecord{receipt}
	route := kube.op.Route
	route.Status, route.Receipt, route.Revision = "ready", &receipt, 3
	kube.route = &route
	kube.op = RouteOperation{}
	st := &removalStore{kubeRouteStore: kube}
	svc.Store, svc.Domains = st, st
	return svc, st, cluster, dns, RouteRemovalRequest{Key: "plan/resource:gw", ProjectID: route.ProjectID, EnvironmentID: route.Input.EnvironmentID, Service: route.Input.Service, Routes: []Route{route}}
}

// A reviewed delete removes each route through one operation with its own
// ID, and a retry resumes that operation instead of starting another.
func TestRemoveReviewedRoutesRemovesOnceAcrossRetries(t *testing.T) {
	svc, st, cluster, dns, in := routeRemovalFixture(t)
	if err := svc.RemoveReviewedRoutes(t.Context(), kubeRouteManager, in); err != nil {
		t.Fatal(err)
	}
	want := RouteRemovalID(in.Key, "route_k")
	if !slices.Equal(st.accepted, []string{want}) || st.op.Kind != "remove" || st.op.Status != "succeeded" || st.route.Status != "removed" || len(dns.records) != 0 {
		t.Fatalf("after removal: accepted %v, op %+v, route %+v, records %+v", st.accepted, st.op, st.route, dns.records)
	}
	if !slices.Equal(*cluster.log, []string{"delete ingress route_k", "delete dns record_1"}) {
		t.Fatal("removal order", *cluster.log)
	}
	if err := svc.RemoveReviewedRoutes(t.Context(), kubeRouteManager, in); err != nil {
		t.Fatal("a retry after the removal", err)
	}
	if len(st.accepted) != 1 || len(*cluster.log) != 2 {
		t.Fatalf("a retry removed again: accepted %v, log %v", st.accepted, *cluster.log)
	}
}

// A route that changed after the delete was reviewed isn't removed.
func TestRemoveReviewedRoutesRefusesAChangedRoute(t *testing.T) {
	svc, st, cluster, _, in := routeRemovalFixture(t)
	st.route.Revision = 4
	err := svc.RemoveReviewedRoutes(t.Context(), kubeRouteManager, in)
	if !errors.Is(err, ErrConflict) || !strings.HasSuffix(err.Error(), ": api.example.com changed since the delete was reviewed; plan again") {
		t.Fatalf("a changed route: %v", err)
	}
	if len(st.accepted) != 0 || len(*cluster.log) != 0 {
		t.Fatalf("a changed route was removed: %v %v", st.accepted, *cluster.log)
	}
}

// A request for another service's route is refused outright.
func TestRemoveReviewedRoutesRefusesAnotherServicesRoute(t *testing.T) {
	svc, st, _, _, in := routeRemovalFixture(t)
	in.Service = "other"
	if err := svc.RemoveReviewedRoutes(t.Context(), kubeRouteManager, in); !errors.Is(err, ErrInvalid) || len(st.accepted) != 0 {
		t.Fatalf("another service's route: %v %v", err, st.accepted)
	}
}
