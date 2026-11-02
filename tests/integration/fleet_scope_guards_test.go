//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type noFleetSourceManifest struct{ planning.Manifests }

func (noFleetSourceManifest) Latest(context.Context, string, string) (*manifests.Revision, error) {
	return nil, manifests.ErrNotFound
}
func TestFleetLegacyWriteDenied(t *testing.T) {
	f := newFleetFixture(t, 2)
	reservation, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("a"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := f.projects.GetEnvironment(t.Context(), "org", "proj", reservation.Instance.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if env.InstanceID != reservation.Instance.ID {
		t.Fatal("environment ownership missing")
	}
	p := security.Principal{OrgID: "org", UserID: "usr"}
	plans := planning.NewService(planning.Deps{Projects: f.projects, Store: planning.NewPgStore(f.db), Authz: fleetAuth{}, Manifests: noFleetSourceManifest{}})
	if _, err = plans.Create(t.Context(), p, "proj", planning.CreatePlanInput{Environment: env.Name}); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy plan: %v", err)
	}
	if _, err = plans.ChooseConnection(t.Context(), p, "proj", env.Name, "kubernetes", planning.ChooseConnectionInput{}); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy connection: %v", err)
	}
	if _, err = plans.PreviewCloudChange(t.Context(), p, "proj", cloudprofiles.ChangeInput{EnvironmentID: env.ID}); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy cloud assignment: %v", err)
	}
	deploymentStore := fleetGuardDeploymentStore{deployment: &deployments.Deployment{ID: "deployment", OrgID: "org", ProjectID: "proj", EnvironmentID: env.ID, Status: deployments.StatusFailed}}
	deploymentAPI := deployments.NewService(deploymentStore, fleetAuth{}, nil, nil)
	deploymentAPI.Projects = f.projects
	if _, retryErr := deploymentAPI.Retry(t.Context(), p, "deployment"); !errors.Is(retryErr, projects.ErrFleetManaged) {
		t.Fatalf("direct deployment retry: %v", retryErr)
	}
	if _, resumeErr := deploymentAPI.ResumeCapacity(t.Context(), p, "deployment", 1); !errors.Is(resumeErr, projects.ErrFleetManaged) {
		t.Fatalf("direct capacity resume: %v", resumeErr)
	}
	if _, cancelErr := deploymentAPI.CancelCapacityWait(t.Context(), p, "deployment", 1); !errors.Is(cancelErr, projects.ErrFleetManaged) {
		t.Fatalf("direct capacity cancellation: %v", cancelErr)
	}
	variables := environments.NewService(environments.NewPgStore(f.db), nil, f.projects, nil, fleetAuth{}, nil, nil)
	if _, err = variables.CreateProject(t.Context(), p, "proj", environments.CreateVariableInput{Key: "SETTING", Value: "changed", Environment: env.Name}); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy variable: %v", err)
	}
	prior, err := variables.CreateProject(t.Context(), p, "proj", environments.CreateVariableInput{Key: "PRIOR", Value: "original", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_variables SET environment=$1,environment_id=$2 WHERE id=$3`, env.Name, env.ID, prior.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = variables.Update(t.Context(), p, prior.ID, environments.UpdateVariableInput{Value: "changed"}); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy variable update: %v", err)
	}
	if err = variables.Delete(t.Context(), p, prior.ID); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy variable delete: %v", err)
	}
}
func TestFleetSiblingEnvironmentUnaffected(t *testing.T) {
	f := newFleetFixture(t, 2)
	reservation, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("a"))
	if err != nil {
		t.Fatal(err)
	}
	envs, err := f.projects.ListEnvironments(t.Context(), "org", "proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range envs {
		if env.ID != reservation.Instance.EnvironmentID && env.InstanceID != "" {
			t.Fatal("ordinary sibling marked managed")
		}
	}
	p := security.Principal{OrgID: "org", UserID: "usr"}
	plans := planning.NewService(planning.Deps{Projects: f.projects, Authz: fleetAuth{}, Manifests: noFleetSourceManifest{}})
	if _, err = plans.Create(t.Context(), p, "proj", planning.CreatePlanInput{Environment: "production"}); !errors.Is(err, planning.ErrNoManifest) {
		t.Fatalf("ordinary planning changed: %v", err)
	}
	variables := environments.NewService(environments.NewPgStore(f.db), nil, f.projects, nil, fleetAuth{}, nil, nil)
	if _, err = variables.CreateProject(t.Context(), p, "proj", environments.CreateVariableInput{Key: "SETTING", Value: "allowed", Environment: "production"}); err != nil {
		t.Fatalf("ordinary variable write: %v", err)
	}
}

type fleetGuardDeploymentStore struct {
	deployments.Store
	deployment *deployments.Deployment
}

func (s fleetGuardDeploymentStore) GetDeployment(context.Context, string, string) (*deployments.Deployment, error) {
	return s.deployment, nil
}
