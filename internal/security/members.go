package security

import "fmt"

// authsome's member roles. Beyond owner, admin and member the role is
// free-form; Loomplane stores its viewers as "viewer".
const (
	MemberOwner  = "owner"
	MemberAdmin  = "admin"
	MemberMember = "member"
	MemberViewer = "viewer"
)

// MemberRoleFor is the authsome member role that gives a person the
// Loomplane role. It's the inverse of RoleForMember.
func MemberRoleFor(role string) (string, error) {
	switch role {
	case RoleOwner:
		return MemberOwner, nil
	case RoleAdmin:
		return MemberAdmin, nil
	case RoleDeveloper:
		return MemberMember, nil
	case RoleViewer:
		return MemberViewer, nil
	}
	return "", fmt.Errorf("unknown role %q: use owner, admin, developer or viewer", role)
}

// MayGrant reports whether a member holding one authsome role may give
// another person a role: never one above their own. It's the ceiling
// authsome's organization plugin applies to its own invitations and role
// changes, so an invitation made through Loomplane obeys the same rule.
func MayGrant(callerMemberRole, grantedMemberRole string) bool {
	return memberRank(grantedMemberRole) <= memberRank(callerMemberRole)
}

func memberRank(memberRole string) int {
	switch memberRole {
	case MemberOwner:
		return 3
	case MemberAdmin:
		return 2
	case MemberMember:
		return 1
	}
	return 0
}
