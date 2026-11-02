package deployments

import (
	"context"
	"errors"
	"slices"
	"testing"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/security/securitytest"
)

// Retrying, reading, and finding a plan's deployment check the
// deployment's own project, so a team's grant there counts and nowhere
// else.
func TestRetryChecksApproveOnTheDeploymentsProject(t *testing.T) {
	ctx := context.Background()
	w := securitytest.New(t, "org")
	rec := securitytest.NewRecorder(w.Authz)
	projA, projB := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	// Running, so a retry stops right after its check.
	m := &memStore{d: map[string]*Deployment{
		"dep_a": {ID: "dep_a", OrgID: "org", ProjectID: projA, PlanID: "plan_a", Status: StatusRunning},
		"dep_b": {ID: "dep_b", OrgID: "org", ProjectID: projB, PlanID: "plan_b", Status: StatusRunning},
	}}
	svc := NewService(m, rec, nil, nil)
	admin := w.Workspace(t, "ausr_admin", security.RoleAdmin)
	viewer := w.Workspace(t, "ausr_viewer", security.RoleViewer)
	approver := w.ProjectOnly(t, "ausr_grace", security.RoleAdmin, projA)
	reader := w.ProjectOnly(t, "ausr_ray", security.RoleDeveloper, projA)

	tests := []struct {
		name      string
		run       func(p security.Principal, which string) error
		check     string
		allowed   error // what an allowed call returns
		workspace security.Principal
		grant     security.Principal
	}{
		{"Retry", func(p security.Principal, which string) error {
			_, err := svc.Retry(ctx, p, "dep_"+which)
			return err
		}, "deployment:approve", ErrNotFailed, admin, approver},
		{"Get", func(p security.Principal, which string) error {
			_, err := svc.Get(ctx, p, "dep_"+which)
			return err
		}, "deployment:read", nil, viewer, reader},
		{"ForPlan", func(p security.Principal, which string) error {
			_, err := svc.ForPlan(ctx, p, "plan_"+which)
			return err
		}, "deployment:read", nil, viewer, reader},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec.Reset()
			if err := tt.run(tt.workspace, "a"); !errors.Is(err, tt.allowed) {
				t.Fatalf("workspace role on A: %v, want %v", err, tt.allowed)
			}
			if got, want := rec.Checks(), []string{tt.check + " " + projA}; !slices.Equal(got, want) {
				t.Fatalf("checks %v, want %v", got, want)
			}
			if err := tt.run(tt.workspace, "b"); !errors.Is(err, tt.allowed) {
				t.Fatalf("workspace role on B: %v, want %v", err, tt.allowed)
			}
			if err := tt.run(tt.grant, "a"); !errors.Is(err, tt.allowed) {
				t.Fatalf("project grant on A: %v, want %v", err, tt.allowed)
			}
			if err := tt.run(tt.grant, "b"); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("project grant on B: %v, want ErrForbidden", err)
			}
		})
	}
	// A workspace developer still may not retry.
	dev := w.Workspace(t, "ausr_dev", security.RoleDeveloper)
	if _, err := svc.Retry(ctx, dev, "dep_a"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer retry: %v, want ErrForbidden", err)
	}
}
