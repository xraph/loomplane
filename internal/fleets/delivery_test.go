package fleets

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type deliveryFixtureStore struct {
	Store
	EventDeliveryStore
	item     *EventDeliveryItem
	finished bool
	failure  string
}

func (s *deliveryFixtureStore) ClaimEventDelivery(_ context.Context, id string) (*EventDeliveryItem, error) {
	if id != "event" {
		return nil, ErrNotFound
	}
	if s.finished {
		return nil, nil
	}
	return s.item, nil
}
func (s *deliveryFixtureStore) FinishEventDelivery(_ context.Context, id, lease string, accepted bool, reason string) error {
	if id != "event" || lease != "lease" {
		return ErrConflict
	}
	s.finished = accepted
	s.failure = reason
	return nil
}

type deliveryFixtureTarget struct {
	calls   int
	failure error
}

func (d *deliveryFixtureTarget) DeliverFleetEvent(_ context.Context, org string, event fleet.Event) error {
	if org != "org" || event.ID != "event" || event.InstanceID != "instance" {
		return ErrInvalid
	}
	d.calls++
	return d.failure
}
func TestFleetDeliveryDuplicate(t *testing.T) {
	store := &deliveryFixtureStore{item: &EventDeliveryItem{OrgID: "org", Lease: "lease", Event: fleet.Event{ID: "event", InstanceID: "instance"}}}
	target := &deliveryFixtureTarget{}
	s := NewService(Deps{Store: store, Delivery: target})
	if err := s.DeliverEvent(t.Context(), "event"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverEvent(t.Context(), "event"); err != nil {
		t.Fatal(err)
	}
	if target.calls != 1 {
		t.Fatalf("duplicate acceptance: %d", target.calls)
	}
}
func TestFleetDeliveryRedactedFailure(t *testing.T) {
	store := &deliveryFixtureStore{item: &EventDeliveryItem{OrgID: "org", Lease: "lease", Event: fleet.Event{ID: "event", InstanceID: "instance"}}}
	target := &deliveryFixtureTarget{failure: errors.New("provider token=synthetic-secret")}
	s := NewService(Deps{Store: store, Delivery: target})
	if err := s.DeliverEvent(t.Context(), "event"); !errors.Is(err, ErrEventDelivery) {
		t.Fatalf("error: %v", err)
	}
	if store.failure != "acceptance_failed" || store.finished {
		t.Fatalf("failure persistence: %+v", store)
	}
}

type retryEventStore struct {
	readFixtureStore
	calls int
}

func (s *retryEventStore) RetryEventAcceptance(_ context.Context, scope FleetScope, instance, event string) error {
	if scope.OrgID != "org" || scope.ProjectID != "project" || scope.FleetID != "fleet" || instance != "instance" || event != "event" {
		return ErrNotFound
	}
	s.calls++
	return nil
}

type retryEventAuthorizer struct{ denied security.Permission }

func (a retryEventAuthorizer) Require(_ context.Context, _ security.Principal, p security.Permission, project string) error {
	if p == a.denied {
		return security.ErrForbidden
	}
	if (p == security.ProjectRead || p == security.ProjectUpdate) && project != "project" {
		return security.ErrForbidden
	}
	return nil
}
func TestFleetDeliveryRetryRequiresHumanAuthority(t *testing.T) {
	store := &retryEventStore{}
	svc := NewService(Deps{Store: store, Authz: retryEventAuthorizer{}})
	human := security.Principal{OrgID: "org", UserID: "operator"}
	for _, p := range []security.Principal{{OrgID: "org", UserID: "operator", ReadOnly: true}, {OrgID: "org", UserID: "operator", KeyID: "key"}, {OrgID: "org", UserID: security.FleetSubject("key"), Kind: security.KindFleet, KeyID: "key"}} {
		if err := svc.RetryEventDelivery(t.Context(), p, "instance", "event"); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("non-human or read-only retry: %v", err)
		}
	}
	for _, denied := range []security.Permission{security.ProjectRead, security.ProjectUpdate, security.MemberManage} {
		svc.authz = retryEventAuthorizer{denied: denied}
		if err := svc.RetryEventDelivery(t.Context(), human, "instance", "event"); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("permission %v bypass: %v", denied, err)
		}
	}
	if store.calls != 0 {
		t.Fatal("unauthorized retry reached store")
	}
	svc.authz = retryEventAuthorizer{}
	if err := svc.RetryEventDelivery(t.Context(), human, "instance", "event"); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatal("authorized retry missing")
	}
	if err := svc.RetryEventDelivery(t.Context(), security.Principal{OrgID: "other", UserID: "operator"}, "instance", "event"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign retry: %v", err)
	}
}
