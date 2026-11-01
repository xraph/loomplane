package security

import (
	"errors"
	"testing"
)

func TestFleetCredentialCannotUseOperatorPermissions(t *testing.T) {
	a := newAuthorizer(t, map[string]string{alice: "owner"})
	p := Principal{UserID: alice, OrgID: org, KeyID: "key", Kind: KindFleet, Fleet: &FleetScope{ProjectID: "project", FleetID: "fleet"}}
	for _, perm := range []Permission{ProjectRead, EnvironmentManage, VariableRead, VariableManage, OrganizationAdminister, CloudAccountRead, DeploymentApprove} {
		if err := a.Require(t.Context(), p, perm, "project"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("fleet credential granted %s: %v", perm.Name(), err)
		}
	}
}
func TestFleetScopeCannotBeResolvedAsMember(t *testing.T) {
	if _, err := Resolve(FleetSubject("key"), org, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("service subject resolved as a member: %v", err)
	}
	a := newAuthorizer(t, map[string]string{FleetSubject("key"): "owner"})
	if err := a.Require(t.Context(), ActorPrincipal(org, FleetSubject("key")), ProjectRead, "project"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("saved actor escaped fleet scope: %v", err)
	}
}
