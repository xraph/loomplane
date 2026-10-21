package cloudinfra

import (
	"context"
	"testing"

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
