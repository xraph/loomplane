// What a team gives its members, in words. The API decides who may do what;
// this only names it.
import type { Team, TeamGrant } from "@/client/forge/loomplane/types"
import { roleLabel } from "@/lib/roles"

// A team can give viewer, developer or admin, never owner.
const rank: Record<string, number> = { viewer: 0, developer: 1, admin: 2 }

/** grantLabel reads a grant as "Developer on checkout-api" or "Viewer on the workspace". */
export function grantLabel(
  grant: TeamGrant,
  projectName: (id: string) => string
): string {
  const scope = grant.projectId ? projectName(grant.projectId) : "the workspace"
  return `${roleLabel(grant.role)} on ${scope}`
}

/**
 * teamRolesFor names the teams a person is in, and lists the grants that
 * give them more than their own role does, such as "Admin on checkout-api
 * through Payments". A grant at or below their role changes nothing, so it
 * is left out.
 */
export function teamRolesFor(
  userId: string,
  memberRole: string,
  teams: Team[],
  projectName: (id: string) => string
): { teams: string[]; higher: string[] } {
  const own = rank[memberRole] ?? 3 // an owner outranks every grant
  const names: string[] = []
  const higher: string[] = []
  for (const team of teams) {
    if (!team.members.some((m) => m.userId === userId)) continue
    names.push(team.name)
    for (const grant of team.grants) {
      if ((rank[grant.role] ?? 0) > own) {
        higher.push(`${grantLabel(grant, projectName)} through ${team.name}`)
      }
    }
  }
  return { teams: names, higher }
}
