// Loomplane's organization roles, and how they map onto authsome's member
// roles. Mirrors internal/security/members.go: the API decides, this only
// lets the dashboard offer the right choices.

export type Role = "owner" | "admin" | "developer" | "viewer"

export const roles: { value: Role; label: string; description: string }[] = [
  {
    value: "owner",
    label: "Owner",
    description: "Everything, including the workspace itself",
  },
  {
    value: "admin",
    label: "Admin",
    description: "Manages members, approves deployments",
  },
  {
    value: "developer",
    label: "Developer",
    description: "Creates projects and requests deployments",
  },
  { value: "viewer", label: "Viewer", description: "Reads everything" },
]

const memberRoles: Record<Role, string> = {
  owner: "owner",
  admin: "admin",
  developer: "member",
  viewer: "viewer",
}

/** memberRoleFor is the authsome member role behind a Loomplane role. */
export function memberRoleFor(role: Role): string {
  return memberRoles[role]
}

/** roleLabel names a role for people. */
export function roleLabel(role: string): string {
  return roles.find((r) => r.value === role)?.label ?? role
}

const rank: Record<Role, number> = {
  owner: 3,
  admin: 2,
  developer: 1,
  viewer: 0,
}

/** grantable lists the roles someone holding role may give: none above their own. */
export function grantable(role: string): typeof roles {
  const own = rank[role as Role] ?? 0
  return roles.filter((r) => rank[r.value] <= own)
}
