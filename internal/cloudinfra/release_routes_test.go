package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type releaseOperationStore struct {
	Store
	DomainStore
	current RouteOperation
	retries int
}

func (s *releaseOperationStore) WithOperationLock(_ context.Context, _ string, run func() error) error {
	return run()
}
func (s *releaseOperationStore) DomainProject(context.Context, string, string) error { return nil }
func (s *releaseOperationStore) RouteOperation(context.Context, string, string) (*RouteOperation, error) {
	v := s.current
	return &v, nil
}
func (s *releaseOperationStore) SaveRouteOperation(_ context.Context, op *RouteOperation) error {
	s.retries++
	s.current = *op
	// Another route worker completes this same durable ID before the caller reads it.
	s.current.Status = "succeeded"
	s.current.Route.Revision = 9
	s.current.Route.Receipt = &provider.DNSRecord{ID: "latest-receipt"}
	return nil
}
func TestReleaseCoordinatorResumesExactOperationAndRefreshesReceipt(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "waiting"} {
		t.Run(status, func(t *testing.T) {
			st := &releaseOperationStore{current: RouteOperation{ID: "durable-operation", Status: status, Route: Route{OrgID: "org", ProjectID: "project", Revision: 9, Receipt: &provider.DNSRecord{ID: "latest-receipt"}}}}
			svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}})
			old := st.current
			old.Route.Revision = 1
			old.Route.Receipt = nil
			if err := svc.reconcileReleaseOperation(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, &old); err != nil {
				t.Fatal(err)
			}
			if old.ID != "durable-operation" || old.Route.Revision != 9 || old.Route.Receipt == nil || old.Route.Receipt.ID != "latest-receipt" {
				t.Fatal("final readiness received stale operation snapshot", old)
			}
			if status != "succeeded" && st.retries != 1 {
				t.Fatal("failed operation was not resumed exactly once", st.retries)
			}
		})
	}
}

type ttlReleaseStore struct {
	releaseOperationStore
	source, destination Route
	accepted            *RouteOperation
}

func (s *ttlReleaseStore) Route(context.Context, string, string, string) (*Route, error) {
	v := s.destination
	return &v, nil
}
func (s *ttlReleaseStore) RouteOperation(_ context.Context, _ string, id string) (*RouteOperation, error) {
	v := RouteOperation{ID: id, Kind: "prepare", Status: "succeeded", Source: &s.source, Route: s.destination, PreviousReceipt: s.source.Receipt}
	return &v, nil
}
func (s *ttlReleaseStore) RouteOperations(context.Context, string, string, string) ([]*RouteOperation, error) {
	return []*RouteOperation{{ID: "cutover", Kind: "cutover", Status: "succeeded", HandoffID: releasePrepareID("plan/action", s.source.ID), Route: s.destination}}, nil
}
func (s *ttlReleaseStore) AcceptRoute(_ context.Context, op *RouteOperation) error {
	s.accepted = op
	return ErrPending
}
func TestReleaseCoordinatorUsesReviewedExternalTTL(t *testing.T) {
	for _, bound := range []int{0, 3600, 86401} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			source := Route{ID: "route", OrgID: "org", ProjectID: "project", Status: "ready", Input: RouteInput{EnvironmentID: "env", Service: "web", ExternalTTLSeconds: bound}, Binding: RouteBinding{InstanceID: "old"}}
			destination := source
			destination.Binding.InstanceID = "new"
			destination.UpdatedAt = time.Now()
			destination.Revision = 2
			st := &ttlReleaseStore{source: source, destination: destination}
			svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}})
			err := svc.ReconcileReleaseRoutes(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, ReleaseRouteRequest{Key: "plan/action", ProjectID: "project", EnvironmentID: "env", Service: "web", InstanceID: "new", Routes: []Route{source}, DrainSeconds: 60})
			if bound == 3600 {
				if !errors.Is(err, ErrPending) || st.accepted == nil || st.accepted.NotBefore.Sub(destination.UpdatedAt) != time.Hour {
					t.Fatal("reviewed TTL lost before release", err, st.accepted)
				}

				st.accepted = nil
				_, err = svc.ReleaseRouteHandoff(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "project", releasePrepareID("plan/action", source.ID), 60)
				if !errors.Is(err, ErrPending) || st.accepted == nil || st.accepted.NotBefore.Sub(destination.UpdatedAt) != time.Hour {
					t.Fatal("explicit release reduced persisted bound", err)
				}
			} else if err == nil || st.accepted != nil {
				t.Fatal("missing or invalid TTL reached release", err)
			}
		})
	}
	source := Route{Input: RouteInput{ExternalTTLSeconds: 3600}, Receipt: &provider.DNSRecord{TTL: 7200}}
	if delay, err := ReviewedRouteDrain(source, 60); err != nil || delay != 3600 {
		t.Fatal("provider receipt changed the reviewed input", delay, err)
	}
}

func TestReleaseCoordinatorHonorsLongManagedReceiptTTL(t *testing.T) {
	for _, tt := range []struct {
		name              string
		previous, current int
	}{{"previous receipt", 90000, 60}, {"current receipt", 60, 90000}} {
		t.Run(tt.name, func(t *testing.T) {
			source := Route{ID: "route", OrgID: "org", ProjectID: "project", Status: "ready", Input: RouteInput{EnvironmentID: "env", Service: "web"}, Binding: RouteBinding{InstanceID: "old"}, Receipt: &provider.DNSRecord{TTL: tt.previous}}
			destination := source
			destination.Binding.InstanceID = "new"
			destination.UpdatedAt = time.Now()
			destination.Revision = 2
			destination.Receipt = &provider.DNSRecord{TTL: tt.current}
			st := &ttlReleaseStore{source: source, destination: destination}
			svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}})
			err := svc.ReconcileReleaseRoutes(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, ReleaseRouteRequest{Key: "plan/action", ProjectID: "project", EnvironmentID: "env", Service: "web", InstanceID: "new", Routes: []Route{source}, DrainSeconds: 60})
			if !errors.Is(err, ErrPending) || st.accepted == nil {
				t.Fatal("managed receipt prevented release acceptance", err)
			}
			if delay := st.accepted.NotBefore.Sub(destination.UpdatedAt); delay != 90000*time.Second {
				t.Fatal("trusted receipt TTL was reduced", delay)
			}
		})
	}
}

func TestReleaseCoordinatorCleanupCannotCreateRouteEffects(t *testing.T) {
	source := Route{ID: "route", OrgID: "org", ProjectID: "project", Status: "ready", Input: RouteInput{EnvironmentID: "env", Service: "web", ExternalTTLSeconds: 60}, Binding: RouteBinding{InstanceID: "old"}}
	destination := source
	destination.Binding.InstanceID = "new"
	destination.Revision = 2
	st := &ttlReleaseStore{source: source, destination: destination}
	svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}})
	err := svc.ReconcileReleaseRoutes(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, ReleaseRouteRequest{Key: "plan/action", ProjectID: "project", EnvironmentID: "env", Service: "web", InstanceID: "new", Routes: []Route{source}, DrainSeconds: 60, CleanupOnly: true})
	if !errors.Is(err, ErrConflict) || st.accepted != nil {
		t.Fatal("cleanup created a new operation", err, st.accepted)
	}
}

type retryActorStore struct {
	releaseOperationStore
	authorized []string
	denied     string
}

func (s *retryActorStore) SaveRouteOperation(_ context.Context, op *RouteOperation) error {
	s.current = *op
	return nil
}
func (*retryActorStore) BeginIngress(context.Context, *RouteOperation) (*IngressState, error) {
	return &IngressState{}, nil
}
func (*retryActorStore) ResolveRouteBinding(context.Context, string, string, RouteInput) (RouteBinding, error) {
	return RouteBinding{}, ErrPending
}
func (s *retryActorStore) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	s.authorized = append(s.authorized, p.UserID)
	if p.UserID == s.denied {
		return security.ErrForbidden
	}
	return nil
}
func TestReleaseCoordinatorRetryUsesCurrentWorkerActor(t *testing.T) {
	for _, kind := range []string{"prepare", "cutover"} {
		t.Run(kind, func(t *testing.T) {
			st := &retryActorStore{denied: "original", releaseOperationStore: releaseOperationStore{current: RouteOperation{ID: "accepted", Kind: kind, Status: "waiting", Principal: security.Principal{OrgID: "org", UserID: "original"}, Route: Route{OrgID: "org", ProjectID: "project", Binding: RouteBinding{ServerID: "server"}}}}}
			svc := NewService(Deps{Store: st, Domains: st, Authz: st})
			op := st.current
			err := svc.reconcileReleaseOperation(t.Context(), security.Principal{OrgID: "org", UserID: "current"}, &op)
			if !errors.Is(err, ErrPending) || st.current.Status != "waiting" {
				t.Fatalf("retry did not reach current actor verification: status=%s auth=%v err=%v", st.current.Status, st.authorized, err)
			}
			for _, actor := range st.authorized {
				if actor != "current" {
					t.Fatalf("worker used old actor: %v", st.authorized)
				}
			}
			if st.current.Principal.UserID != "original" || st.current.ID != "accepted" {
				t.Fatal("accepted initiator or identity changed")
			}
		})
	}
}

func TestRouteWorkerRechecksLegacyAndRetryActor(t *testing.T) {
	st := &retryActorStore{denied: "original", releaseOperationStore: releaseOperationStore{current: RouteOperation{ID: "accepted", Kind: "prepare", Status: "waiting", Principal: security.Principal{OrgID: "org", UserID: "original"}, Route: Route{OrgID: "org", ProjectID: "project", Binding: RouteBinding{ServerID: "server"}}}}}
	svc := NewService(Deps{Store: st, Domains: st, Authz: st})
	if err := svc.ReconcileRoute(t.Context(), "org", "accepted", nil); err != nil {
		t.Fatal(err)
	}
	if st.current.Status != "failed" || len(st.authorized) != 1 || st.authorized[0] != "original" {
		t.Fatal("legacy actor authorization skipped", st.authorized, st.current.Status)
	}
	if _, err := svc.RetryRoute(t.Context(), security.Principal{OrgID: "org", UserID: "current"}, "project", "accepted"); err != nil {
		t.Fatal(err)
	}
	st.denied = "current"
	st.authorized = nil
	if err := svc.ReconcileRoute(t.Context(), "org", "accepted", nil); err != nil {
		t.Fatal(err)
	}
	if st.current.Status != "failed" || len(st.authorized) != 1 || st.authorized[0] != "current" {
		t.Fatal("revoked retry actor authorization skipped", st.authorized, st.current.Status)
	}
}

func (s *ttlReleaseStore) RouteHandoffReceipts(ctx context.Context, org, project, route, _ string) ([]*RouteOperation, error) {
	return s.RouteOperations(ctx, org, project, route)
}
