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
  if (!env.default) return { label: "Services placed here", tone: "waiting" }
  return { label: "In sync", tone: "live" }
}

/**
 * EnvironmentsOnCloud lists the environments that use the Cloud: every
 * environment of a project whose default it is, and any environment still
 * running services placed on it. Each row opens its project's Clouds tab.
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
  const onDefault = envs.filter((e) => e.default)
  const projectOf = (id: string) => projects.find((p) => p.id === id)
  return (
    <section aria-label="Environments on this Cloud" className="grid gap-2.5">
      <CloudSectionHead
        title="Environments on this Cloud"
        count={usage.data ? onDefault.length : undefined}
        description="Environments of projects whose default Cloud this is, and any still running services placed here."
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
            ? "Give a project access. A project's first Cloud becomes its default, and all its environments use it."
            : "Make this a project's default Cloud from that project's Clouds tab. All its environments then use it."}
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
                        search={{ tab: "cloud" }}
                        aria-label={`Open ${env.projectName}'s Clouds`}
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
