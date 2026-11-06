// The environments on a Cloud and the state each one is in. Taking or leaving
// the Cloud happens in the environment's own project, on its Cloud tab.
import type { UseQueryResult } from "@forge-go/client-react"

import type {
  EnvironmentUsage,
  Project,
  ProjectGrant,
  Sync,
  Usage,
} from "@/client/forge/loomplane/types"
import {
  ActionsCell,
  Cell,
  CloudTable,
  CloudTableRow,
  NameCell,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { syncLabel } from "@/lib/cloud-targets"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"

import { movingSync, serviceCount } from "./data"

/** envState is one environment's state on the Cloud, in words and a tone. */
export function envState(
  env: EnvironmentUsage,
  syncs: Sync[]
): { label: string; tone: Tone } {
  const change = syncs
    .filter(movingSync)
    .flatMap((s) => s.environments ?? [])
    .find((e) => e.environmentId === env.environmentId)
  if (change) {
    if (change.externalRouting && !change.routingEvidence)
      return { label: "Waiting on routing", tone: "waiting" }
    if (change.status === "failed")
      return { label: "Change failed", tone: "failed" }
    return { label: syncLabel(change.status), tone: "flight" }
  }
  if (!env.assigned) return { label: "Left, services remain", tone: "waiting" }
  return { label: "In sync", tone: "live" }
}

/**
 * EnvironmentsOnCloud lists the environments that use the Cloud. An
 * environment belongs to its project, so each row opens the project's Cloud
 * tab, where it takes or leaves the Cloud and chooses targets.
 */
export function EnvironmentsOnCloud({
  usage,
  syncs,
  grants,
  projects,
}: {
  usage: UseQueryResult<Usage>
  syncs: Sync[]
  /** Projects allowed on the Cloud; undefined in a project's own view. */
  grants?: ProjectGrant[]
  projects: Project[]
}) {
  const envs = usage.data?.environments ?? []
  const assigned = envs.filter((e) => e.assigned)
  const projectOf = (id: string) => projects.find((p) => p.id === id)
  return (
    <section aria-label="Environments on this Cloud" className="grid gap-2.5">
      <CloudSectionHead
        title="Environments on this Cloud"
        count={usage.data ? assigned.length : undefined}
        description="Each one runs its services on this Cloud's targets."
      />
      {usage.status === "error" ? (
        <QueryError
          error={usage.error}
          fallback="The environments on this Cloud couldn't be loaded."
          retry={usage.refetch}
        />
      ) : !usage.data ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : envs.length === 0 ? (
        <CloudEmpty kind="list" title="No environments yet">
          {grants && grants.length === 0
            ? "Give a project access first, then choose this Cloud from that project's Cloud tab."
            : "Choose this Cloud for an environment from its project's Cloud tab. Its services then run on this Cloud's targets."}
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Environments on this Cloud"
          columns="minmax(0,1fr) 150px 150px"
          head={["Environment", "State", ""]}
          minWidth={480}
        >
          {envs.map((env) => {
            const project = projectOf(env.projectId)
            const style = project ? projectStyle(project) : null
            const state = envState(env, syncs)
            const running = env.placements.reduce(
              (n, p) => n + p.services.length,
              0
            )
            return (
              <CloudTableRow key={env.environmentId}>
                <NameCell
                  tile={
                    style && (
                      <ProjectIcon
                        icon={style.icon}
                        color={style.color}
                        className="size-7 rounded-lg"
                      />
                    )
                  }
                  name={env.environmentName}
                  meta={
                    <>
                      <Link
                        to="/projects/$id"
                        params={{ id: env.projectId }}
                        className="hover:underline"
                      >
                        {env.projectName}
                      </Link>{" "}
                      · {serviceCount(running)}
                    </>
                  }
                />
                <Cell>
                  <StatusPill tone={state.tone}>{state.label}</StatusPill>
                </Cell>
                <ActionsCell>
                  <Button
                    size="sm"
                    variant="outline"
                    nativeButton={false}
                    render={
                      <Link
                        to="/projects/$id"
                        params={{ id: env.projectId }}
                        search={{
                          tab: "cloud",
                          environment: env.environmentName,
                        }}
                        aria-label={`Open ${env.environmentName} in ${env.projectName}`}
                      />
                    }
                  >
                    Open in project
                  </Button>
                </ActionsCell>
              </CloudTableRow>
            )
          })}
        </CloudTable>
      )}
    </section>
  )
}
