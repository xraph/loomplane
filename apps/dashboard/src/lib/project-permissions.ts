// What the signed-in person may do on one project. A team can give a role
// on a single project, which the session's workspace permissions don't
// show, so a project's page asks the API what the person holds there.
import { useCallback } from "react"

import { useProjectsPermissions } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { useOrganization } from "@/lib/organization"

/**
 * PROJECT_PERMISSIONS are the permissions a project's list answers for, in
 * the server's order (security.ProjectPermissions). Anything else, such as
 * repository:connect or agent:use, is only ever held workspace-wide, so the
 * list never names it. internal/security fails a test if the two drift.
 */
export const PROJECT_PERMISSIONS: readonly string[] = [
  "project:read",
  "project:update",
  "project:delete",
  "environment:read",
  "environment:manage",
  "analysis:run",
  "variable:read",
  "variable:manage",
  "deployment:read",
  "deployment:create",
  "deployment:approve",
  "infrastructure:read",
  "infrastructure:modify",
  "service_backup:read",
  "service_backup:manage",
  "service_restore:manage",
  "cloud_account:read",
  "cloud_account:manage",
  "secret:manage",
  "ai:read",
  "ai:edit",
  "ai:manage",
]

const projectScoped = new Set(PROJECT_PERMISSIONS)

/**
 * useProjectCan answers for the project once its permissions have loaded,
 * and with the workspace's answer while they load or if they can't be read.
 * A permission the project's list doesn't cover is always the workspace's.
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
      held && projectScoped.has(permission)
        ? held.includes(permission)
        : can(permission),
    [held, can]
  )
}
