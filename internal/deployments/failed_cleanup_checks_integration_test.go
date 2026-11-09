//go:build integration

package deployments

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/security/securitytest"
)

// noPlans has no plans, so a cleanup preview stops right after its check.
type noPlans struct{ Plans }

func (noPlans) GetPlan(context.Context, string, string) (*planning.Plan, error) {
	return nil, planning.ErrNotFound
}

// A failed deployment's cleanup is checked on the deployment's project, so
// a team's grant there counts and nowhere else.
func TestFailedCleanupChecksTheDeploymentsProject(t *testing.T) {
	ctx := t.Context()
	pg := reapDatabase(t)
	store := NewPgStore(pg)
	w := securitytest.New(t, "org_1")
	rec := securitytest.NewRecorder(w.Authz)
	projA, projB := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	for i, projectID := range []string{projA, projB} {
		suffix := []string{"a", "b"}[i]
		for _, q := range []string{
			`INSERT INTO loomplane_projects (id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES ('` + projectID + `','org_1','` + suffix + `','` + suffix + `','active','usr_1',now(),now())`,
			`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env_` + suffix + `','org_1','` + projectID + `','production','production',now(),now())`,
			`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest_` + suffix + `','org_1','` + projectID + `',1,'version: 1','digest','usr_1',now())`,
			`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('plan_` + suffix + `','org_1','` + projectID + `','env_` + suffix + `','production','approved','manifest_` + suffix + `',1,'usr_1',now())`,
		} {
			if _, err := pg.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.CreateDeployment(ctx, &Deployment{ID: "dep_" + suffix, OrgID: "org_1", ProjectID: projectID, EnvironmentID: "env_" + suffix,
			Environment: "production", PlanID: "plan_" + suffix, Status: StatusFailed, StartedBy: "usr_1", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	cleanups := NewFailedCleanups(NewRunner(RunnerDeps{Store: store, Authz: rec, Plans: noPlans{}}), store, nil)
	admin := w.Workspace(t, "ausr_admin", security.RoleAdmin)
	viewer := w.Workspace(t, "ausr_viewer", security.RoleViewer)
	approver := w.ProjectOnly(t, "ausr_grace", security.RoleAdmin, projA)
	reader := w.ProjectOnly(t, "ausr_ray", security.RoleDeveloper, projA)

	tests := []struct {
		name      string
		run       func(p security.Principal, id string) error
		check     string
		allowed   error // where an allowed call stops, past the check
		workspace security.Principal
		grant     security.Principal
	}{
		{"Preview", func(p security.Principal, id string) error {
			_, err := cleanups.Preview(ctx, p, id)
			return err
		}, "deployment:approve", planning.ErrNotFound, admin, approver},
		{"Get", func(p security.Principal, id string) error {
			_, err := cleanups.Get(ctx, p, id)
			return err
		}, "deployment:read", ErrNotFound, viewer, reader},
		{"Apply", func(p security.Principal, id string) error {
			_, err := cleanups.Apply(ctx, p, id, "")
			return err
		}, "deployment:approve", ErrCleanupConflict, admin, approver},
		{"Retry", func(p security.Principal, id string) error {
			_, err := cleanups.Retry(ctx, p, id)
			return err
		}, "deployment:approve", ErrCleanupConflict, admin, approver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec.Reset()
			if err := tt.run(tt.workspace, "dep_a"); !errors.Is(err, tt.allowed) {
				t.Fatalf("workspace role on A: %v, want %v", err, tt.allowed)
			}
			if got, want := rec.Checks(), []string{tt.check + " " + projA}; !slices.Equal(got, want) {
				t.Fatalf("checks %v, want %v", got, want)
			}
			if err := tt.run(tt.workspace, "dep_b"); !errors.Is(err, tt.allowed) {
				t.Fatalf("workspace role on B: %v, want %v", err, tt.allowed)
			}
			if err := tt.run(tt.grant, "dep_a"); !errors.Is(err, tt.allowed) {
				t.Fatalf("project grant on A: %v, want %v", err, tt.allowed)
			}
			if err := tt.run(tt.grant, "dep_b"); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("project grant on B: %v, want ErrForbidden", err)
			}
			if err := tt.run(tt.workspace, "dep_missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a missing deployment: %v, want ErrNotFound", err)
			}
		})
	}
}
