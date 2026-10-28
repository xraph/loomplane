// The environments on a Cloud, and the three things you do with one: start
// using the Cloud, choose which target each service runs on, and stop using
// it. Each goes through the same reviewed change the rest of the page uses.
import type { UseQueryResult } from "@forge-go/client-react"
import { PlusIcon } from "lucide-react"
import { useState } from "react"

import * as api from "@/client/forge/loomplane/hooks"
import type {
  Environment,
  EnvironmentUsage,
  Project,
  ProjectGrant,
  Sync,
  Target,
  Usage,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { ChangeReview } from "@/components/cloud-targets/change-review"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import {
  CloudEmpty,
  CloudSectionHead,
  Field,
  Row,
  Rows,
} from "@/components/cloud-targets/infrastructure-ui"
import { ServiceAdoption } from "@/components/cloud-targets/service-adoption"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { syncLabel } from "@/lib/cloud-targets"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"

import { movingSync, serviceCount, type CloudScope } from "./data"

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

export function EnvironmentsOnCloud({
  scope,
  usage,
  syncs,
  grants,
  projects,
  onChanged,
}: {
  scope: CloudScope
  usage: UseQueryResult<Usage>
  syncs: Sync[]
  /** Projects allowed on the Cloud; undefined in a project's own view. */
  grants?: ProjectGrant[]
  projects: Project[]
  onChanged: () => void
}) {
  const { can } = useOrganization()
  const permitted = can("cloud_account:manage") && can("deployment:approve")
  const [adding, setAdding] = useState(false)
  const [adopting, setAdopting] = useState<EnvironmentUsage | null>(null)
  const [leaving, setLeaving] = useState<EnvironmentUsage | null>(null)
  const envs = usage.data?.environments ?? []
  const assigned = envs.filter((e) => e.assigned)
  const projectOf = (id: string) => projects.find((p) => p.id === id)
  const choices = grants
    ? projects.filter((p) => grants.some((g) => g.projectId === p.id))
    : projects.filter((p) => p.id === scope.project)
  return (
    <section aria-label="Environments on this Cloud" className="grid gap-3">
      <CloudSectionHead
        title="Environments on this Cloud"
        count={usage.data ? assigned.length : undefined}
      >
        {permitted && choices.length > 0 && (
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
            <PlusIcon />
            Use in an environment
          </Button>
        )}
      </CloudSectionHead>
      {usage.status === "error" ? (
        <QueryError
          error={usage.error}
          fallback="The environments on this Cloud couldn't be loaded."
          retry={usage.refetch}
        />
      ) : !usage.data ? (
        <Skeleton className="h-24 rounded-xl" />
      ) : envs.length === 0 ? (
        <CloudEmpty kind="inline" title="No environment uses this Cloud yet">
          {grants && grants.length === 0
            ? "Give a project access first, then choose an environment to use it."
            : "Choose an environment to use it. Its services then run on this Cloud's targets."}
        </CloudEmpty>
      ) : (
        <Rows>
          {envs.map((env) => {
            const project = projectOf(env.projectId)
            const style = project ? projectStyle(project) : null
            const state = envState(env, syncs)
            const running = env.placements.reduce(
              (n, p) => n + p.services.length,
              0
            )
            return (
              <Row key={env.environmentId} className="flex-wrap gap-y-2">
                <span className="flex min-w-0 flex-[1_1_220px] items-center gap-2.5">
                  {style && (
                    <ProjectIcon
                      icon={style.icon}
                      color={style.color}
                      size="sm"
                    />
                  )}
                  <span className="grid min-w-0 gap-0.5">
                    <span className="truncate text-[13px] font-medium">
                      {env.environmentName}
                    </span>
                    <span className="truncate text-xs text-muted-foreground">
                      <Link
                        to="/projects/$id"
                        params={{ id: env.projectId }}
                        className="hover:underline"
                      >
                        {env.projectName}
                      </Link>{" "}
                      · {serviceCount(running)}
                    </span>
                  </span>
                </span>
                <StatusPill tone={state.tone}>{state.label}</StatusPill>
                {permitted && env.assigned && (
                  <span className="flex flex-wrap gap-1.5">
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => setAdopting(env)}
                    >
                      Choose targets
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setLeaving(env)}
                    >
                      Stop using
                    </Button>
                  </span>
                )}
              </Row>
            )
          })}
        </Rows>
      )}
      {adding && (
        <UseInEnvironment
          scope={scope}
          projects={choices}
          taken={assigned.map((e) => e.environmentId)}
          onClose={() => setAdding(false)}
          onApplied={() => {
            setAdding(false)
            onChanged()
          }}
        />
      )}
      {adopting && (
        <ChooseTargets
          scope={scope}
          env={adopting}
          onClose={() => setAdopting(null)}
        />
      )}
      {leaving && (
        <Assignment
          projectId={leaving.projectId}
          environmentId={leaving.environmentId}
          cloudId=""
          title={`Stop using ${scope.cloud.name} in ${leaving.environmentName}`}
          onClose={() => setLeaving(null)}
          onApplied={() => {
            setLeaving(null)
            onChanged()
          }}
        />
      )}
    </section>
  )
}

/** Assignment reads the environment's assignment generation, then reviews. */
function Assignment({
  projectId,
  environmentId,
  cloudId,
  title,
  onClose,
  onApplied,
}: {
  projectId: string
  environmentId: string
  cloudId: string
  title: string
  onClose: () => void
  onApplied: () => void
}) {
  const current = useQuery(api.useEnvironmentCloudsGet, {
    path: { id: projectId, environmentId },
  })
  if (current.status === "error")
    return (
      <Dialog open onOpenChange={(open) => !open && onClose()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <QueryError
            error={current.error}
            fallback="The environment's Cloud couldn't be loaded."
            retry={current.refetch}
          />
        </DialogContent>
      </Dialog>
    )
  if (!current.data) return null
  return (
    <ChangeReview
      projectId={projectId}
      title={title}
      assignment={{ environmentId, cloudId, revision: current.data.revision }}
      onClose={onClose}
      onApplied={onApplied}
    />
  )
}

function UseInEnvironment({
  scope,
  projects,
  taken,
  onClose,
  onApplied,
}: {
  scope: CloudScope
  projects: Project[]
  taken: string[]
  onClose: () => void
  onApplied: () => void
}) {
  const [projectId, setProjectId] = useState(projects[0]?.id ?? "")
  const [environmentId, setEnvironmentId] = useState("")
  const [reviewing, setReviewing] = useState(false)
  const environments = useQuery(api.useEnvironmentsList, {
    path: { id: projectId },
  })
  const open = (environments.data?.environments ?? []).filter(
    (e): e is Environment => !!e && !taken.includes(e.id)
  )
  const chosen = open.find((e) => e.id === environmentId)
  if (reviewing && chosen)
    return (
      <Assignment
        projectId={projectId}
        environmentId={chosen.id}
        cloudId={scope.cloud.id}
        title={`Use ${scope.cloud.name} in ${chosen.name}`}
        onClose={onClose}
        onApplied={onApplied}
      />
    )
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Use {scope.cloud.name} in an environment</DialogTitle>
          <DialogDescription>
            The environment&apos;s services move to this Cloud&apos;s targets
            after you review the change. An environment uses one Cloud at a
            time.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          {projects.length > 1 && (
            <Field label="Project">
              <Select
                value={projectId}
                onValueChange={(v) => {
                  setProjectId(v ?? "")
                  setEnvironmentId("")
                }}
              >
                <SelectTrigger aria-label="Project" className="w-full min-w-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {projects.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          {environments.status === "error" ? (
            <QueryError
              error={environments.error}
              fallback="Environments couldn't be loaded."
              retry={environments.refetch}
            />
          ) : !environments.data ? (
            <Skeleton className="h-9" />
          ) : open.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">
              Every environment in this project already uses this Cloud.
            </p>
          ) : (
            <Field label="Environment">
              <Select
                value={environmentId}
                onValueChange={(v) => setEnvironmentId(v ?? "")}
              >
                <SelectTrigger
                  aria-label="Environment"
                  className="w-full min-w-0"
                >
                  <SelectValue placeholder="Choose an environment" />
                </SelectTrigger>
                <SelectContent>
                  {open.map((e) => (
                    <SelectItem key={e.id} value={e.id}>
                      {e.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!chosen} onClick={() => setReviewing(true)}>
            Review change
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** ChooseTargets edits which target each of an environment's services uses. */
function ChooseTargets({
  scope,
  env,
  onClose,
}: {
  scope: CloudScope
  env: EnvironmentUsage
  onClose: () => void
}) {
  const environments = useQuery(api.useEnvironmentsList, {
    path: { id: env.projectId },
  })
  const targets = useQuery(api.useCloudTargetsList, {
    path: { id: env.projectId, cloudId: scope.cloud.id },
  })
  const list = (environments.data?.environments ?? []).filter(
    (e): e is Environment => !!e
  )
  const environment = list.find((e) => e.id === env.environmentId)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            Targets for {env.projectName} · {env.environmentName}
          </DialogTitle>
          <DialogDescription>
            Pick a target for each service. This saves the project&apos;s
            manifest; services move on the next deployment.
          </DialogDescription>
        </DialogHeader>
        {environments.status === "error" || targets.status === "error" ? (
          <QueryError
            error={environments.error ?? targets.error}
            fallback="The environment's services couldn't be loaded."
            retry={() =>
              Promise.allSettled([environments.refetch(), targets.refetch()])
            }
          />
        ) : !environment || !targets.data ? (
          <Skeleton className="h-40" />
        ) : (
          <ServiceAdoption
            projectId={env.projectId}
            environment={environment}
            environments={list}
            targets={targets.data.targets.filter((t): t is Target => !!t)}
            onSaved={async () => onClose()}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
