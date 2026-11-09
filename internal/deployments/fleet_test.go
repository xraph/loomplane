package deployments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/pkg/fleet"
)

type revokedInstanceVerifier struct{}

func (revokedInstanceVerifier) VerifyInstanceSnapshot(context.Context, fleets.OperationAuthority, fleets.InstanceSnapshot) (*fleets.InstanceSnapshot, error) {
	return nil, fleets.ErrAuthorityRevoked
}
func TestFleetLegacyRunnerDenied(t *testing.T) {
	r := NewRunner(RunnerDeps{})
	err := r.authorizeSync(t.Context(), &planning.Plan{ManagedInstanceID: "instance"})
	if !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy runner authority: %v", err)
	}
}
func TestFleetRunnerRequiresCurrentAuthority(t *testing.T) {
	r := NewRunner(RunnerDeps{Instances: revokedInstanceVerifier{}})
	p := &planning.Plan{Kind: planning.KindInstance, ManagedInstanceID: "instance", FleetInput: &planning.InstancePlanInput{Snapshot: fleets.InstanceSnapshot{InstanceID: "instance"}}}
	if err := r.authorizeSync(t.Context(), p); !errors.Is(err, fleets.ErrAuthorityRevoked) {
		t.Fatalf("revoked runner authority: %v", err)
	}
}

type sealedInstanceVerifier struct{}

func (sealedInstanceVerifier) VerifyInstanceSnapshot(_ context.Context, _ fleets.OperationAuthority, s fleets.InstanceSnapshot) (*fleets.InstanceSnapshot, error) {
	return &s, nil
}
func TestInstanceVariablesNeverInheritMutableProjectValues(t *testing.T) {
	snapshot := fleets.InstanceSnapshot{InstanceID: "instance", EnvironmentID: "env", Values: map[string]map[string]string{"api": {"FIXED": "reviewed"}}, Secrets: map[string]map[string]fleet.SecretRef{}}
	plan := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Kind: planning.KindInstance, ManagedInstanceID: "instance", FleetInput: &planning.InstancePlanInput{Authority: fleets.OperationAuthority{Scope: fleets.FleetScope{OrgID: "org", ProjectID: "project"}}, Snapshot: snapshot}}
	vars := fakeVariables{"svc:artifact/api": {"FIXED": "changed", "UNREVIEWED": "inherited"}}
	r := NewRunner(RunnerDeps{Plans: &fakePlans{plan: plan}, Instances: sealedInstanceVerifier{}, Variables: vars})
	d := &Deployment{PlanID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	values, _, err := r.deploymentVariables(t.Context(), d, "svc:artifact/api")
	if err != nil || values["FIXED"] != "reviewed" || values["UNREVIEWED"] != "" {
		t.Fatalf("unsealed variables: %v %v", values, err)
	}
	values["FIXED"] = "mutated"
	if snapshot.Values["api"]["FIXED"] != "reviewed" {
		t.Fatal("runtime aliased snapshot values")
	}
	if _, _, err = r.deploymentVariables(t.Context(), d, "svc:artifact/other"); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("foreign service accepted: %v", err)
	}
}
