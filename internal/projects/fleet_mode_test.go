package projects

import (
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestProjectDefaultsToApplicationMode(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Create(t.Context(), f.principal(t, "ausr_dev", security.RoleDeveloper), CreateProjectInput{Name: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	if p.DeploymentMode != "application" {
		t.Fatalf("new project mode = %q", p.DeploymentMode)
	}
}
