package planning

import (
	"context"
	"errors"
	"slices"
	"testing"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/security/securitytest"
)

// plansByID is a Store holding a few plans, read by ID.
type plansByID struct {
	Store
	plans map[string]*Plan
}

func (s *plansByID) GetPlan(_ context.Context, orgID, id string) (*Plan, error) {
	p, ok := s.plans[id]
	if !ok || p.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *p
	return &c, nil
}

// projectWorld is two plans, one on each of two projects, and a recording
// authorizer over the real one.
func projectWorld(t *testing.T) (svc *Service, rec *securitytest.Recorder, w *securitytest.World, projA string) {
	t.Helper()
	w = securitytest.New(t, "aorg_test")
	rec = securitytest.NewRecorder(w.Authz)
	projA, projB := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	// Superseded, so a decision stops right after its checks.
	store := &plansByID{plans: map[string]*Plan{
		"plan_a": {ID: "plan_a", OrgID: "aorg_test", ProjectID: projA, Status: StatusSuperseded},
		"plan_b": {ID: "plan_b", OrgID: "aorg_test", ProjectID: projB, Status: StatusSuperseded},
	}}
	return NewService(Deps{Store: store, Authz: rec}), rec, w, projA
}

func TestPlanGetChecksTheProject(t *testing.T) {
	ctx := context.Background()
	svc, rec, w, projA := projectWorld(t)
	workspace := w.Workspace(t, "ausr_viewer", security.RoleViewer)
	grant := w.ProjectOnly(t, "ausr_grace", security.RoleDeveloper, projA)

	if _, err := svc.Get(ctx, workspace, "plan_a"); err != nil {
		t.Fatalf("workspace viewer on A: %v", err)
	}
	if got, want := rec.Checks(), []string{"deployment:read " + projA}; !slices.Equal(got, want) {
		t.Fatalf("checks %v, want %v", got, want)
	}
	if _, err := svc.Get(ctx, workspace, "plan_b"); err != nil {
		t.Fatalf("workspace viewer on B: %v", err)
	}
	if _, err := svc.Get(ctx, grant, "plan_a"); err != nil {
		t.Fatalf("project grant on A: %v", err)
	}
	if _, err := svc.Get(ctx, grant, "plan_b"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("project grant on B: %v, want ErrForbidden", err)
	}
}

func TestDecideChecksApproveOnThePlansProject(t *testing.T) {
	ctx := context.Background()
	svc, rec, w, projA := projectWorld(t)
	admin := w.Workspace(t, "ausr_admin", security.RoleAdmin)
	developer := w.Workspace(t, "ausr_dev", security.RoleDeveloper)
	approver := w.ProjectOnly(t, "ausr_grace", security.RoleAdmin, projA)

	decisions := map[string]func(p security.Principal, id string) error{
		"Approve": func(p security.Principal, id string) error {
			_, err := svc.Approve(ctx, p, id, DecideInput{Revision: "r"})
			return err
		},
		"Reject": func(p security.Principal, id string) error {
			_, err := svc.Reject(ctx, p, id, DecideInput{})
			return err
		},
	}
	for name, decide := range decisions {
		t.Run(name, func(t *testing.T) {
			rec.Reset()
			// Allowed through, the superseded plan is what stops it.
			if err := decide(admin, "plan_a"); !errors.Is(err, ErrSuperseded) {
				t.Fatalf("workspace admin on A: %v", err)
			}
			got := rec.Checks()
			if !slices.Equal(got, []string{"deployment:approve " + projA}) {
				t.Fatalf("checks %v, want deployment:approve on %s and never on \"\"", got, projA)
			}
			if err := decide(admin, "plan_b"); !errors.Is(err, ErrSuperseded) {
				t.Fatalf("workspace admin on B: %v", err)
			}
			if err := decide(developer, "plan_a"); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("workspace developer on A: %v, want ErrForbidden", err)
			}
			if err := decide(approver, "plan_a"); !errors.Is(err, ErrSuperseded) {
				t.Fatalf("project admin grant on A: %v", err)
			}
			if err := decide(approver, "plan_b"); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("project admin grant on B: %v, want ErrForbidden", err)
			}
			// The automation is still refused before anything is read.
			if err := decide(security.SystemPrincipal("aorg_test"), "plan_missing"); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("automation: %v, want ErrForbidden", err)
			}
		})
	}
}
