package security

import (
	"slices"
	"testing"
)

func TestMemberRolesWithApprove(t *testing.T) {
	if got := MemberRolesWith(DeploymentApprove); !slices.Equal(got, []string{"owner", "admin"}) {
		t.Fatalf("approvers: %v", got)
	}
	if got := MemberRolesWith(ProjectRead); !slices.Contains(got, "viewer") {
		t.Fatalf("readers: %v", got)
	}
}
