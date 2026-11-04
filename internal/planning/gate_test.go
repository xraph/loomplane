package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/security"
)

// switchGate refuses while err is set.
type switchGate struct{ err error }

func (g *switchGate) CanRun(context.Context, string, metering.Kind, bool) error { return g.err }

func TestApproveRefusedByTheGateLeavesThePlanReady(t *testing.T) {
	store := &unpinnedStore{plan: &Plan{ID: "plan", OrgID: "org", ProjectID: "project", Status: StatusReady, Revision: "revision", Actions: []Action{{Op: Create}}}}
	workflow := &recordingWorkflow{}
	gate := &switchGate{err: &metering.Blocked{Reason: "Add a card in Settings > Billing to start running anything on Loomplane Cloud."}}
	s := NewService(Deps{Store: store, Authz: allowAll{}, Workflow: workflow, Gate: gate})
	p := security.Principal{OrgID: "org", UserID: "owner"}
	if _, err := s.Approve(t.Context(), p, "plan", DecideInput{Revision: "revision"}); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("approve before a card: %v, want ErrBlocked", err)
	}
	if len(workflow.decisions) != 0 || store.settled != "" {
		t.Fatalf("a refused approval signalled or settled: %v %q", workflow.decisions, store.settled)
	}
	gate.err = nil // the card landed
	if _, err := s.Approve(t.Context(), p, "plan", DecideInput{Revision: "revision"}); err != nil {
		t.Fatalf("approve after the card: %v", err)
	}
	if len(workflow.decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(workflow.decisions))
	}
}

func TestRejectIsNeverGated(t *testing.T) {
	store := &unpinnedStore{plan: &Plan{ID: "plan", OrgID: "org", ProjectID: "project", Status: StatusReady, Revision: "revision"}}
	s := NewService(Deps{Store: store, Authz: allowAll{}, Workflow: &recordingWorkflow{}, Gate: &switchGate{err: &metering.Blocked{Reason: "no"}}})
	if _, err := s.Reject(t.Context(), security.Principal{OrgID: "org", UserID: "owner"}, "plan", DecideInput{}); errors.Is(err, metering.ErrBlocked) {
		t.Fatal("reject was gated")
	}
}

func TestTeardownOnlyPlanIsApprovedWhileTheGateRefuses(t *testing.T) {
	gate := &switchGate{err: &metering.Blocked{Reason: "Add a card"}}
	p := security.Principal{OrgID: "org", UserID: "owner"}
	for name, tc := range map[string]struct {
		actions []Action
		blocked bool
	}{
		"teardown": {[]Action{{Op: Delete}, {Op: Keep}}, false},
		"deploy":   {[]Action{{Op: Delete}, {Op: Update}}, true},
		"create":   {[]Action{{Op: Create}}, true},
	} {
		store := &unpinnedStore{plan: &Plan{ID: "plan", OrgID: "org", ProjectID: "project", Status: StatusReady, Revision: "revision", Actions: tc.actions}}
		s := NewService(Deps{Store: store, Authz: allowAll{}, Workflow: &recordingWorkflow{}, Gate: gate})
		_, err := s.Approve(t.Context(), p, "plan", DecideInput{Revision: "revision"})
		if got := errors.Is(err, metering.ErrBlocked); got != tc.blocked {
			t.Errorf("%s: blocked=%v err=%v", name, got, err)
		}
	}
}
