package security

import "strings"

// Kind says who a principal is.
type Kind string

const (
	// KindUser is a person, through a session or an API key.
	KindUser Kind = ""
	// KindSystem is Loomplane automation: the identity that rolls back a
	// release whose watch or canary failed, with or without the AI agent.
	KindSystem Kind = "system"
)

// RoleSystem is the Warden role Loomplane automation holds in each
// organization. It's never a member's role.
const RoleSystem = "loomplane-automation"

// SystemActorName is how people see Loomplane automation in audit, the
// dashboard and the CLI.
const SystemActorName = "Loomplane automation"

const systemPrefix = "loomplane:"

// SystemSubject is Loomplane automation's subject in an organization. It's
// also the created_by and decided_by of an automatic rollback.
func SystemSubject(orgID string) string { return systemPrefix + orgID }

// IsSystemSubject reports whether id is an organization's Loomplane
// automation, and which organization's.
func IsSystemSubject(id string) (orgID string, ok bool) {
	orgID, ok = strings.CutPrefix(id, systemPrefix)
	if !ok || orgID == "" {
		return "", false
	}
	return orgID, true
}

// SystemPrincipal is Loomplane automation acting in an organization. Only
// the automatic rollback, and the deployment of a plan it approved, act
// as it; no session or API key ever resolves to it.
func SystemPrincipal(orgID string) Principal {
	return Principal{Kind: KindSystem, UserID: SystemSubject(orgID), OrgID: orgID}
}

// IsSystem reports whether p is Loomplane automation.
func (p Principal) IsSystem() bool { return p.Kind == KindSystem }

// IsServiceAccount reports whether p is anything but a person. The
// Authorizer checks such a principal as a Warden service account, never
// repairs its roles and never lets it retire a backup.
func (p Principal) IsServiceAccount() bool { return p.Kind != KindUser }

// ActorPrincipal rebuilds the principal a durable record names, such as a
// deployment's started_by: Loomplane automation for the organization's own
// SystemSubject, else a person. Another organization's subject stays a
// person, who holds no role here.
func ActorPrincipal(orgID, actorID string) Principal {
	if sub, ok := IsSystemSubject(actorID); ok && sub == orgID {
		return SystemPrincipal(orgID)
	}
	return Principal{OrgID: orgID, UserID: actorID}
}

// SystemRole is what Loomplane automation may do: read a project and plan,
// approve and run a rollback, and restore the infrastructure it routes to.
// The planning service limits it further, to automatic rollbacks.
func SystemRole() RoleDef {
	return RoleDef{
		Slug: RoleSystem, Name: SystemActorName,
		Description: "Rolls back a release whose watch or canary failed. Never a member's role",
		Permissions: []Permission{
			DeploymentRead, DeploymentCreate, DeploymentApprove,
			ProjectRead, EnvironmentRead, InfrastructureRead, InfrastructureModify,
		},
	}
}
