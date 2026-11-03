package securitytest

import (
	"context"
	"errors"
	"testing"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

func TestProjectOnlyReachesOnlyItsProject(t *testing.T) {
	ctx := context.Background()
	w := New(t, "aorg_test")
	a, b := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	viewer := w.Workspace(t, "ausr_viewer", security.RoleViewer)
	grace := w.ProjectOnly(t, "ausr_grace", security.RoleDeveloper, a)

	for _, on := range []string{a, b, ""} {
		if err := w.Authz.Require(ctx, viewer, security.ProjectRead, on); err != nil {
			t.Fatalf("workspace viewer reads %q: %v", on, err)
		}
	}
	if err := w.Authz.Require(ctx, grace, security.ProjectRead, a); err != nil {
		t.Fatalf("project-only reads its project: %v", err)
	}
	if err := w.Authz.Require(ctx, grace, security.ProjectUpdate, a); err != nil {
		t.Fatalf("project-only updates its project: %v", err)
	}
	for _, on := range []string{b, ""} {
		if err := w.Authz.Require(ctx, grace, security.ProjectRead, on); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("project-only reads %q: %v, want ErrForbidden", on, err)
		}
	}
	if err := w.Authz.Require(ctx, grace, security.DeploymentApprove, a); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer grant approves: %v", err)
	}

	// A team grant leaves the root viewer row: reads pass everywhere, the
	// grant's role only on its project.
	ray := w.TeamGrant(t, "ausr_ray", security.RoleDeveloper, a)
	if err := w.Authz.Require(ctx, ray, security.ProjectRead, b); err != nil {
		t.Fatalf("team grant reads another project: %v", err)
	}
	if err := w.Authz.Require(ctx, ray, security.ProjectUpdate, a); err != nil {
		t.Fatalf("team grant updates its project: %v", err)
	}
	if err := w.Authz.Require(ctx, ray, security.ProjectUpdate, b); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("team grant updates another project: %v", err)
	}
}
