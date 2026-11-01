package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type instanceVerifier struct {
	revoked bool
	calls   int
}

func (v *instanceVerifier) VerifyInstanceSnapshot(_ context.Context, _ fleets.OperationAuthority, snapshot fleets.InstanceSnapshot) (*fleets.InstanceSnapshot, error) {
	v.calls++
	if v.revoked {
		return nil, fleets.ErrAuthorityRevoked
	}
	return &snapshot, nil
}
func TestInstancePlanRequiresVerifiedAuthority(t *testing.T) {
	s := NewService(Deps{})
	if _, err := s.CreateInstancePlan(t.Context(), security.Principal{}, InstancePlanInput{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("unverified input: %v", err)
	}
}
func TestFleetSavedPlanCannotUseSourcePath(t *testing.T) {
	s := NewService(Deps{})
	_, _, err := s.compute(t.Context(), &Plan{ManagedInstanceID: "instance"})
	if !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("managed plan bypassed ownership: %v", err)
	}
}
func TestInstancePlanRechecksAuthority(t *testing.T) {
	verifier := &instanceVerifier{revoked: true}
	s := NewService(Deps{Instances: verifier})
	plan := &Plan{Kind: KindInstance, OrgID: "org", ProjectID: "project", EnvironmentID: "environment", ManagedInstanceID: "instance", FleetInput: &InstancePlanInput{Authority: fleets.OperationAuthority{Scope: fleets.FleetScope{OrgID: "org", ProjectID: "project"}}, Snapshot: fleets.InstanceSnapshot{InstanceID: "instance", EnvironmentID: "environment"}}}
	_, _, err := s.compute(t.Context(), plan)
	if !errors.Is(err, fleets.ErrAuthorityRevoked) || verifier.calls != 1 {
		t.Fatalf("saved authority not rechecked: %v", err)
	}
}

func TestInstanceAuthorityFailureIsPermanent(t *testing.T) {
	for _, err := range []error{fleets.ErrAuthorityRevoked, projects.ErrFleetManaged} {
		if !permanent(err) {
			t.Fatalf("instance authority failure would retry indefinitely: %v", err)
		}
	}
	if permanent(errors.New("temporary database failure")) {
		t.Fatal("transient failure must remain retryable")
	}
}
