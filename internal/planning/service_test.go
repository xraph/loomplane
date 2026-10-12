package planning

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/orchestration"
)

// unpinnedStore holds one plan and records how it was settled. Every
// other Store method panics through the nil embedded interface, which
// proves compute refuses before it reads anything else.
type unpinnedStore struct {
	Store
	plan     *Plan
	failed   string
	settled  string
	decision string
}

func (s *unpinnedStore) GetPlan(context.Context, string, string) (*Plan, error) {
	p := *s.plan
	return &p, nil
}

func (s *unpinnedStore) FailPlanning(_ context.Context, _, _, reason string, _ time.Time) error {
	s.failed = reason
	return nil
}

func (s *unpinnedStore) Decide(_ context.Context, _, _, status, _, note string, _ time.Time) (bool, error) {
	s.settled, s.decision = status, note
	return true, nil
}

// A plan from before plans pinned repositories, which the backfill
// couldn't give one, must not plan from an empty graph.
func TestPlan_refusesAPlanThatPinsNoRepositories(t *testing.T) {
	ctx := context.Background()

	store := &unpinnedStore{plan: &Plan{ID: "plan_1", OrgID: "org_1", Status: StatusPlanning, Sources: []PlanSource{}}}
	svc := NewService(Deps{Store: store})
	status, err := svc.Plan(ctx, "org_1", "plan_1")
	if err != nil || status != StatusFailed || !strings.Contains(store.failed, "this plan pins no repositories") {
		t.Fatalf("plan: %q, %v, failed with %q", status, err, store.failed)
	}

	store = &unpinnedStore{plan: &Plan{ID: "plan_1", OrgID: "org_1", Status: StatusReady, Revision: "r1", Sources: []PlanSource{}}}
	svc = NewService(Deps{Store: store})
	status, err = svc.ApplyDecision(ctx, "org_1", "plan_1", orchestration.Decision{Approve: true, Revision: "r1", By: "usr_1"})
	if err != nil || status != StatusStale || store.settled != StatusStale || !strings.Contains(store.decision, "this plan pins no repositories") {
		t.Fatalf("approve: %q, %v, settled %q with %q", status, err, store.settled, store.decision)
	}
}
