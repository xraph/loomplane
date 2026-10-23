package cloudinfra

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type compensationRouteStore struct {
	releaseOperationStore
	source, destination Route
	prepared, cutover   RouteOperation
	accepted            *RouteOperation
	missing             bool
}

func (s *compensationRouteStore) RouteOperation(_ context.Context, _ string, id string) (*RouteOperation, error) {
	if s.missing {
		return nil, ErrNotFound
	}
	if id == s.prepared.ID {
		cloned := s.prepared
		return &cloned, nil
	}
	cloned := s.cutover
	return &cloned, nil
}
func (s *compensationRouteStore) Route(_ context.Context, _, _, _ string) (*Route, error) {
	cloned := s.destination
	return &cloned, nil
}
func (s *compensationRouteStore) RouteOperations(context.Context, string, string, string) ([]*RouteOperation, error) {
	return []*RouteOperation{&s.prepared, &s.cutover}, nil
}
func (*compensationRouteStore) ValidateRouteCleanup(context.Context, Route) error  { return ErrPending }
func (*compensationRouteStore) ValidateRetainedRoute(context.Context, Route) error { return nil }
func (*compensationRouteStore) Zone(context.Context, string, string) (*Zone, error) {
	return &Zone{ID: "zone", Mode: "external", Name: "example.com"}, nil
}
func (s *compensationRouteStore) AcceptRoute(_ context.Context, op *RouteOperation) error {
	s.accepted = op
	return ErrPending
}
func TestCloudCompensationRollbackUsesLatestReceiptAndRetainsUnknown(t *testing.T) {
	for _, scenario := range []string{"known", "unknown", "cancelled", "before-route-publication"} {
		t.Run(scenario, func(t *testing.T) {
			unknown := scenario == "unknown"
			source := Route{ID: "route", OrgID: "org", ProjectID: "project", Revision: 1, Status: "ready", Input: RouteInput{ZoneID: "zone", Hostname: "app.example.com", EnvironmentID: "env", Service: "web", ExternalTTLSeconds: 3600}, Binding: RouteBinding{InstanceID: "source", Address: "192.0.2.1", ServerID: "source-host"}}
			destination := source
			destination.Revision = 2
			destination.Binding = RouteBinding{InstanceID: "candidate", Address: "192.0.2.2", ServerID: "candidate-host"}
			st := &compensationRouteStore{source: source, destination: destination}
			st.prepared = RouteOperation{ID: releasePrepareID("old/action", source.ID), Kind: "prepare", Source: &source, Route: destination, Status: "succeeded"}
			st.cutover = RouteOperation{ID: "cutover", Kind: "cutover", HandoffID: st.prepared.ID, Source: &source, Route: destination, Status: "waiting", DNSAttempted: true, Receipt: &provider.DNSRecord{ID: "latest", Owner: "owner", Type: "A", Name: source.Input.Hostname, Value: destination.Binding.Address, TTL: 60}}
			if unknown {
				st.cutover.Receipt = nil
			}
			if scenario == "cancelled" {
				st.cutover.Status = "cancelled"
			}
			if scenario == "before-route-publication" {
				st.destination = source
			}
			svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}})
			err := svc.ReconcileCompensationRoutes(t.Context(), security.Principal{OrgID: "org", UserID: "current"}, ReleaseRouteRequest{Key: "old/action", ProjectID: "project", EnvironmentID: "env", Service: "web", InstanceID: "source", Routes: []Route{source}, DrainSeconds: 60})
			if unknown {
				if !errors.Is(err, ErrConflict) || st.accepted != nil {
					t.Fatal("unknown DNS outcome replaced", err)
				}
				return
			}
			if !errors.Is(err, ErrPending) || st.accepted == nil || st.accepted.Kind != "rollback" || !reflect.DeepEqual(st.accepted.PreviousReceipt, st.cutover.Receipt) || st.accepted.Route.Binding.InstanceID != "source" {
				t.Fatal("rollback lost exact receipt", err, st.accepted)
			}
		})
	}
}
func TestCloudCompensationMissingExternalTTLCanSettleUnchangedRoute(t *testing.T) {
	source := Route{ID: "route", OrgID: "org", ProjectID: "project", Revision: 1, Status: "ready", Binding: RouteBinding{InstanceID: "source"}}
	st := &compensationRouteStore{missing: true, source: source, destination: source}
	svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}})
	in := ReleaseRouteRequest{Key: "old/action", ProjectID: "project", InstanceID: "source", Routes: []Route{source}}
	if err := svc.ReconcileCompensationRoutes(t.Context(), security.Principal{OrgID: "org"}, in); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	st.destination.Revision++
	if err := svc.ReconcileCompensationRoutes(t.Context(), security.Principal{OrgID: "org"}, in); !errors.Is(err, ErrConflict) {
		t.Fatal("concurrent route mutation accepted", err)
	}
}

func (*compensationRouteStore) ClaimCompensationHandoff(context.Context, *RouteOperation, *Route, *RouteOperation) error {
	return nil
}
