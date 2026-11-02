// What the signed-in person may do on one project. A team can give a role
// on a single project, which the session's workspace permissions don't
// show, so a project's page asks the API what the person holds there.
import { useCallback } from "react"

import { useProjectsPermissions } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { useOrganization } from "@/lib/organization"

/**
 * useProjectCan answers for the project once its permissions have loaded,
 * and with the workspace's answer while they load or if they can't be read.
 *
 * Call it from the project's page and its parts, never once per row of a
 * list: the query is shared, so every call for one project is one request.
 */
export function useProjectCan(
  projectId: string | undefined
): (permission: string) => boolean {
  const { can } = useOrganization()
  const query = useQuery(useProjectsPermissions, {
    path: { id: projectId ?? "" },
  })
  const held =
    projectId && query.status === "success"
      ? query.data?.permissions
      : undefined
  return useCallback(
    (permission: string) =>
      held ? held.includes(permission) : can(permission),
    [held, can]
  )
}
