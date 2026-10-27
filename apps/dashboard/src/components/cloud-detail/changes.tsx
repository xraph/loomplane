// One history for a Cloud: every reviewed move of a target, the default or an
// environment, and every server operation, newest first, in plain words.
import type { UseQueryResult } from "@forge-go/client-react"
import { ArrowRightIcon } from "lucide-react"
import { useState, type ReactNode } from "react"

import { messageOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Operation,
  OperationList,
  Sync,
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { ChangeReview } from "@/components/cloud-targets/change-review"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
  Rows,
  stateTone,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { EnvironmentProgress } from "@/components/cloud-targets/organization-activity"
import { ActivityIcon } from "@/components/icons"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { syncLabel } from "@/lib/cloud-targets"
import { dateTime, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"
import { useNow, usePeople } from "@/lib/use-runs"

import {
  accountName,
  movingSync,
  placementLabel,
  useScopedSyncs,
  type CloudScope,
} from "./data"

/** A sync's status as a reader would say it, and its tone. */
export function syncState(sync: Sync): { label: string; tone: Tone } {
  if (sync.status === "routing_pending")
    return { label: "Waiting on routing", tone: "waiting" }
  if (sync.status === "succeeded") return { label: "Done", tone: "live" }
  return { label: syncLabel(sync.status), tone: stateTone(sync.status) }
}

/** changeTitle names what a sync did to this Cloud. */
export function changeTitle(
  sync: Sync,
  cloudId: string,
  targets: Target[],
  connections: Connection[]
): { title: string; from?: string; to?: string } {
  if (sync.reverts) return { title: "Reverted an earlier change" }
  if (sync.targetId) {
    const t =
      targets.find((x) => x.id === sync.targetId) ?? sync.after ?? sync.before
    const name = t?.displayName || t?.name || "a target"
    return {
      title: sync.after
        ? `Moved ${name} to ${accountName(connections, sync.after.connectionId)}`
        : `Changed ${name}`,
      from: placementLabel(connections, sync.before),
      to: placementLabel(connections, sync.after),
    }
  }
  if (sync.default)
    return {
      title: sync.afterDefault
        ? `Set the default to ${accountName(connections, sync.afterDefault.connectionId)}`
        : "Cleared the default",
      from: placementLabel(connections, sync.beforeDefault),
      to: placementLabel(connections, sync.afterDefault),
    }
  const env = sync.environments?.[0]?.environment ?? "An environment"
  return sync.cloudId === cloudId
    ? { title: `${env} started using this Cloud` }
    : { title: `${env} stopped using this Cloud` }
}

const serverVerbs: Record<string, string> = {
  create: "Create server",
  adopt: "Connect server",
  resize: "Resize",
  delete: "Delete server",
  disconnect: "Disconnect server",
}

type Entry =
  | { kind: "placement"; at: number; sync: Sync }
  | { kind: "servers"; at: number; op: Operation }

type ChangesProps = {
  scope: CloudScope
  targets: Target[]
  connections: Connection[]
  onChanged: () => void
}

export function Changes(props: ChangesProps) {
  return props.scope.project ? (
    <ChangeList {...props} />
  ) : (
    <WithOperations {...props} />
  )
}

// Server operations are the organization's; a project's view never asks.
function WithOperations(props: ChangesProps) {
  const operations = useQuery(api.useInfrastructureOperationsList, {
    path: { cloudId: props.scope.cloud.id },
  })
  return <ChangeList {...props} operations={operations} />
}

function ChangeList({
  scope,
  targets,
  connections,
  onChanged,
  operations,
}: ChangesProps & {
  operations?: UseQueryResult<OperationList>
}) {
  const org = !!operations
  const [filter, setFilter] = useState<"all" | "placement" | "servers">("all")
  const [before, setBefore] = useState("")
  const syncs = useScopedSyncs(scope, before)
  const ops = (operations?.data?.operations ?? []).filter(
    (o): o is Operation => !!o
  )
  useInfrastructureRefresh(
    syncs.list.some(movingSync) ||
      ops.some((o) => ["pending", "running", "waiting"].includes(o.status)),
    async () => Promise.allSettled([syncs.refetch(), operations?.refetch()])
  )
  const entries: Entry[] = [
    ...syncs.list.map((sync) => ({
      kind: "placement" as const,
      at: Date.parse(sync.createdAt),
      sync,
    })),
    ...ops.map((op) => ({
      kind: "servers" as const,
      at: Date.parse(op.createdAt),
      op,
    })),
  ]
    .filter((e) => filter === "all" || e.kind === filter)
    .sort((a, b) => b.at - a.at)
  const reload = () => {
    void syncs.refetch()
    void operations?.refetch()
    onChanged()
  }
  const failed = syncs.status === "error" || operations?.status === "error"
  return (
    <section aria-label="Changes" className="grid gap-3">
      <CloudSectionHead
        title="Changes to this Cloud"
        description="Every move of a target or the default, every environment joining or leaving, and every server change. Environments keep serving their current release until a move finishes."
      >
        {org && (
          <div
            role="radiogroup"
            aria-label="Show"
            className="flex gap-0.5 rounded-lg bg-muted p-[3px]"
          >
            {(["all", "placement", "servers"] as const).map((f) => (
              <button
                key={f}
                type="button"
                role="radio"
                aria-checked={filter === f}
                onClick={() => setFilter(f)}
                className={cn(
                  "h-7 rounded-md px-2.5 text-[12.5px] font-medium capitalize",
                  filter === f
                    ? "bg-card text-foreground shadow-xs"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                {f}
              </button>
            ))}
          </div>
        )}
      </CloudSectionHead>
      {failed ? (
        <QueryError
          error={syncs.error ?? operations?.error}
          fallback="This Cloud's changes couldn't be loaded."
          retry={async () => reload()}
        />
      ) : !syncs.data || (operations && !operations.data) ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : entries.length === 0 ? (
        <CloudEmpty kind="list" icon={ActivityIcon} title="No changes yet">
          Moving a target or the default, choosing this Cloud for an
          environment, and server changes appear here.
        </CloudEmpty>
      ) : (
        <Rows>
          {entries.map((e) =>
            e.kind === "placement" ? (
              <SyncEntry
                key={e.sync.id}
                scope={scope}
                sync={e.sync}
                targets={targets}
                connections={connections}
                onChanged={reload}
              />
            ) : (
              <OperationEntry
                key={e.op.id}
                cloudId={scope.cloud.id}
                op={e.op}
                onChanged={reload}
              />
            )
          )}
        </Rows>
      )}
      {filter !== "servers" && (syncs.data?.next || before) && (
        <div className="flex gap-2">
          {syncs.data?.next && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => setBefore(syncs.data?.next ?? "")}
            >
              Older changes
            </Button>
          )}
          {before && (
            <Button size="sm" variant="ghost" onClick={() => setBefore("")}>
              Newest changes
            </Button>
          )}
        </div>
      )}
    </section>
  )
}

function Entry({
  title,
  state,
  meta,
  actions,
  children,
}: {
  title: string
  state: { label: string; tone: Tone }
  meta: ReactNode
  actions?: ReactNode
  children?: ReactNode
}) {
  return (
    <article className="grid gap-2 border-b px-4 py-3 last:border-b-0">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="grid min-w-0 flex-[1_1_360px] gap-0.5">
          <p className="flex flex-wrap items-center gap-2">
            <span className="text-[13px] font-semibold">{title}</span>
            <StatusPill tone={state.tone}>{state.label}</StatusPill>
          </p>
          <p className="text-xs text-muted-foreground">{meta}</p>
        </div>
        {actions && <div className="flex flex-wrap gap-1.5">{actions}</div>}
      </div>
      {children}
    </article>
  )
}

function SyncEntry({
  scope,
  sync,
  targets,
  connections,
  onChanged,
}: {
  scope: CloudScope
  sync: Sync
  targets: Target[]
  connections: Connection[]
  onChanged: () => void
}) {
  const { can } = useOrganization()
  const permitted = can("cloud_account:manage") && can("deployment:approve")
  const person = usePeople()
  const now = useNow()
  const org = !scope.project
  const retry = useMutation(
    org ? api.useOrganizationCloudSyncsRetry : api.useCloudSyncsRetry
  )
  const routing = useMutation(
    org
      ? api.useOrganizationCloudSyncsCompleteRouting
      : api.useCloudSyncsCompleteRouting
  )
  const [evidence, setEvidence] = useState<Record<string, string>>({})
  const [reverting, setReverting] = useState(false)
  const [error, setError] = useState("")
  const busy = retry.isPending || routing.isPending
  const path = { id: scope.project, syncId: sync.id }
  const act = async (environmentId?: string) => {
    setError("")
    try {
      if (environmentId)
        await routing.mutateAsync({
          path,
          body: { environmentId, evidence: evidence[environmentId]!.trim() },
        })
      else await retry.mutateAsync({ path })
      onChanged()
    } catch (err) {
      setError(messageOf(err, "That wasn't accepted. Refresh and try again."))
    }
  }
  const { title, from, to } = changeTitle(
    sync,
    scope.cloud.id,
    targets,
    connections
  )
  const at = Date.parse(sync.createdAt)
  // A revert is reviewed through the project that made the change.
  const revertProject = scope.project || sync.projectId
  return (
    <Entry
      title={title}
      state={syncState(sync)}
      meta={
        <>
          {sync.createdBy ? `${person(sync.createdBy).name} · ` : ""}
          <time dateTime={sync.createdAt} title={dateTime(at)}>
            {relative(at, now)}
          </time>
          {sync.attempt > 1 && ` · attempt ${sync.attempt}`}
        </>
      }
      actions={
        permitted && (
          <>
            {["failed", "cleanup_pending"].includes(sync.status) && (
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => void act()}
              >
                Retry
              </Button>
            )}
            {revertProject &&
              !movingSync(sync) &&
              !["reverted", "failed"].includes(sync.status) && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => setReverting(true)}
                >
                  Review revert
                </Button>
              )}
          </>
        )
      }
    >
      {from && to && (
        <p className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted-foreground">
          <span className="rounded-md bg-muted px-2 py-0.5">{from}</span>
          <ArrowRightIcon aria-label="to" className="size-3.5" />
          <span className="rounded-md bg-muted px-2 py-0.5 text-foreground">
            {to}
          </span>
        </p>
      )}
      {sync.error && <CloudError message={sync.error} />}
      {(sync.environments ?? []).map((env) => {
        const waiting =
          env.externalRouting &&
          sync.status === "routing_pending" &&
          !env.routingEvidence
        return (
          <div key={env.environmentId} className="grid gap-1.5 text-xs">
            <p className="flex flex-wrap items-center gap-2 text-muted-foreground">
              <span className="font-medium text-foreground">
                {env.environment}
              </span>
              {waiting
                ? "deployed, waiting for you to point traffic here"
                : syncLabel(env.status).toLowerCase()}
              {env.deploymentId && (
                <Link
                  to="/deployments/$id"
                  params={{ id: env.deploymentId }}
                  className="font-medium text-foreground hover:underline"
                >
                  Deployment
                </Link>
              )}
            </p>
            <EnvironmentProgress environment={env} />
            {waiting && permitted && (
              <span className="flex flex-wrap items-center gap-2">
                <Input
                  aria-label={`How ${env.environment} now reaches the new placement`}
                  placeholder="What you changed, such as the DNS record"
                  className="h-8 max-w-sm min-w-48 flex-1"
                  value={evidence[env.environmentId] ?? ""}
                  disabled={busy}
                  onChange={(e) =>
                    setEvidence((v) => ({
                      ...v,
                      [env.environmentId]: e.target.value,
                    }))
                  }
                />
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy || !evidence[env.environmentId]?.trim()}
                  onClick={() => void act(env.environmentId)}
                >
                  Confirm routing
                </Button>
              </span>
            )}
          </div>
        )
      })}
      <CloudError message={error} />
      {reverting && (
        <ChangeReview
          projectId={revertProject}
          title="Review revert"
          revert={sync.id}
          onClose={() => setReverting(false)}
          onApplied={() => {
            setReverting(false)
            onChanged()
          }}
        />
      )}
    </Entry>
  )
}

function OperationEntry({
  cloudId,
  op,
  onChanged,
}: {
  cloudId: string
  op: Operation
  onChanged: () => void
}) {
  const retry = useMutation(api.useInfrastructureOperationsRetry)
  const cancel = useMutation(api.useInfrastructureOperationsCancel)
  const [error, setError] = useState("")
  const now = useNow()
  const at = Date.parse(op.createdAt)
  const act = async (kind: "retry" | "cancel") => {
    setError("")
    try {
      await (kind === "retry" ? retry : cancel).mutateAsync({
        path: { cloudId, operationId: op.id },
      })
      onChanged()
    } catch (err) {
      setError(messageOf(err, "The operation couldn't be updated."))
    }
  }
  const verb = serverVerbs[op.input.kind] ?? "Change server"
  const subject = op.input.name || op.input.size || ""
  const word = op.status.replace(/_/g, " ")
  return (
    <Entry
      title={`${verb}${op.input.kind === "resize" ? " server" : ""} ${subject}`.trim()}
      state={{
        label:
          op.status === "applied" || op.status === "succeeded"
            ? "Done"
            : word.charAt(0).toUpperCase() + word.slice(1),
        tone: stateTone(op.status),
      }}
      meta={
        <>
          <time dateTime={op.createdAt} title={dateTime(at)}>
            {relative(at, now)}
          </time>
          {op.input.region && ` · ${op.input.region}`}
          {op.attempt > 1 && ` · attempt ${op.attempt}`}
          {op.message && ` · ${op.message}`}
        </>
      }
      actions={
        <>
          {["failed", "waiting", "cleanup_pending"].includes(op.status) && (
            <Button
              size="sm"
              variant="outline"
              disabled={retry.isPending}
              onClick={() => void act("retry")}
            >
              Retry
            </Button>
          )}
          {!op.attempted && ["preview", "pending"].includes(op.status) && (
            <Button
              size="sm"
              variant="ghost"
              disabled={cancel.isPending}
              onClick={() => void act("cancel")}
            >
              Cancel
            </Button>
          )}
        </>
      }
    >
      <CloudError message={error} />
    </Entry>
  )
}
