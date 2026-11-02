// One history for a Cloud: every reviewed move of a target, the default or an
// environment, and server and cluster operations, newest first within each page.
import type { UseQueryResult } from "@forge-go/client-react"
import { ArrowRightIcon, TargetIcon } from "lucide-react"
import { useEffect, useState, type ReactNode } from "react"

import { messageOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  CloudClusterOperation,
  CloudClusterOperationPage,
  Connection,
  Operation,
  OperationList,
  Sync,
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { ChangeReview } from "@/components/cloud-targets/change-review"
import {
  ActionsCell,
  Cell,
  CloudTable,
  CloudTableRow,
  RowMenu,
  Segments,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
  StatePill,
  stateTone,
  statusWord,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { EnvironmentProgress } from "@/components/cloud-targets/organization-activity"
import {
  ActivityIcon,
  ClustersIcon,
  InfrastructureIcon,
} from "@/components/icons"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { DropdownMenuItem } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { dateTime, relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { useLiveState } from "@/lib/realtime-state"
import { Link } from "@/lib/router"
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
  return { label: statusWord(sync.status), tone: stateTone(sync.status) }
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
  resize: "Resize server",
  delete: "Delete server",
  disconnect: "Disconnect server",
}

type Kind = "placement" | "servers" | "clusters"
type Filter = "all" | Kind

const filters: { id: Filter; label: string; empty: string }[] = [
  { id: "all", label: "All", empty: "No changes yet" },
  { id: "placement", label: "Placement", empty: "No placement changes yet" },
  { id: "servers", label: "Servers", empty: "No server changes yet" },
  { id: "clusters", label: "Clusters", empty: "No cluster changes yet" },
]

type Entry =
  | { kind: "placement"; at: number; sync: Sync }
  | { kind: "servers"; at: number; op: Operation }
  | { kind: "clusters"; at: number; op: CloudClusterOperation }

/**
 * Pages walks a cursor-paged history. Each history only says what comes
 * after a page, so going back to newer changes retraces the cursors taken.
 */
type Pages = {
  cursor: string
  first: boolean
  older: (next: string) => void
  newer: () => void
}

function usePages(): Pages {
  const [stack, setStack] = useState<string[]>([""])
  return {
    cursor: stack[stack.length - 1] ?? "",
    first: stack.length === 1,
    older: (next) => setStack((s) => [...s, next]),
    newer: () => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s)),
  }
}

/** when is a change's time: relative within a week, else "Oct 20, 9:40 AM". */
function when(at: number, now: number) {
  return now - at < 7 * 24 * 60 * 60 * 1000
    ? relative(at, now)
    : new Date(at).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      })
}

function Time({ iso, now }: { iso: string; now: number }) {
  const at = Date.parse(iso)
  return (
    <time dateTime={iso} title={dateTime(at)}>
      {when(at, now)}
    </time>
  )
}

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
  const clusterPages = usePages()
  const clusters = useQuery(api.useClusterOperationsList, {
    path: { cloudId: props.scope.cloud.id },
    query: { cursor: clusterPages.cursor, limit: 50 },
  })
  return (
    <ChangeList
      {...props}
      operations={operations}
      clusters={clusters}
      clusterPages={clusterPages}
    />
  )
}

function ChangeList({
  scope,
  targets,
  connections,
  onChanged,
  operations,
  clusters,
  clusterPages,
}: ChangesProps & {
  operations?: UseQueryResult<OperationList>
  clusters?: UseQueryResult<CloudClusterOperationPage>
  clusterPages?: Pages
}) {
  const org = !!operations
  const [filter, setFilter] = useState<Filter>("all")
  const syncPages = usePages()
  const syncs = useScopedSyncs(scope, syncPages.cursor)
  const ops = (operations?.data?.operations ?? []).filter(
    (o): o is Operation => !!o
  )
  const live = useLiveState()
  const refetchClusters = clusters?.refetch
  useEffect(() => {
    void refetchClusters?.().catch(() => undefined)
  }, [refetchClusters, live.lastSync])
  useInfrastructureRefresh(
    (clusters?.data?.items ?? []).some((o) =>
      ["accepted", "running", "waiting_provider"].includes(o.status)
    ) ||
      syncs.list.some(movingSync) ||
      ops.some((o) => ["pending", "running", "waiting"].includes(o.status)),
    async () =>
      Promise.allSettled([
        syncs.refetch(),
        operations?.refetch(),
        clusters?.refetch(),
      ])
  )
  const entries: Entry[] = [
    ...syncs.list.map((sync) => ({
      kind: "placement" as const,
      at: Date.parse(sync.createdAt),
      sync,
    })),
    ...(clusters?.data?.items ?? []).map((op) => ({
      kind: "clusters" as const,
      at: Date.parse(op.createdAt),
      op,
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
    void clusters?.refetch()
    onChanged()
  }
  const failed =
    syncs.status === "error" ||
    operations?.status === "error" ||
    clusters?.status === "error"

  // Placement and cluster histories page on their own cursors, so each
  // keeps its own Newer and Older in the one footer.
  const pagers = [
    (filter === "all" || filter === "placement") && {
      label: "placement",
      pages: syncPages,
      next: syncs.data?.next ?? "",
    },
    (filter === "all" || filter === "clusters") &&
      clusterPages && {
        label: "cluster",
        pages: clusterPages,
        next: clusters?.data?.nextCursor ?? "",
      },
  ].filter(
    (p): p is { label: string; pages: Pages; next: string } =>
      !!p && (!!p.next || !p.pages.first)
  )
  const filterLabel = filters.find((f) => f.id === filter)!

  return (
    <section aria-label="Changes" className="grid gap-2.5">
      <CloudSectionHead
        title="Changes to this Cloud"
        description="Every move of a target or the default, every environment joining or leaving, and every server or cluster change. Environments keep serving their current release until a move finishes."
      >
        {org && (
          <Segments
            label="Show"
            value={filter}
            onChange={setFilter}
            items={filters.map(({ id, label }) => ({ id, label }))}
          />
        )}
      </CloudSectionHead>
      {failed ? (
        <QueryError
          error={syncs.error ?? operations?.error ?? clusters?.error}
          fallback="This Cloud's changes couldn't be loaded."
          retry={async () => reload()}
        />
      ) : !syncs.data ||
        (operations && !operations.data) ||
        (clusters && !clusters.data) ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : entries.length === 0 && pagers.length === 0 ? (
        <CloudEmpty kind="list" icon={ActivityIcon} title={filterLabel.empty}>
          Moving a target or the default, choosing this Cloud for an
          environment, and server or cluster changes appear here.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Changes to this Cloud"
          columns="minmax(0,1fr) auto"
          head={["Change", ""]}
          minWidth={560}
          footer={
            <>
              <span>
                Showing {entries.length}{" "}
                {entries.length === 1 ? "change" : "changes"}
              </span>
              {pagers.length > 0 && (
                <span className="flex flex-wrap items-center gap-x-4 gap-y-2">
                  {pagers.map(({ label, pages, next }) => (
                    <span key={label} className="flex items-center gap-1.5">
                      {pagers.length > 1 && (
                        <span className="mr-1">
                          {label === "placement" ? "Placement" : "Clusters"}
                        </span>
                      )}
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Newer ${label} changes`}
                        disabled={pages.first}
                        onClick={pages.newer}
                      >
                        Newer
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Older ${label} changes`}
                        disabled={!next}
                        onClick={() => pages.older(next)}
                      >
                        Older
                      </Button>
                    </span>
                  ))}
                </span>
              )}
            </>
          }
        >
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
            ) : e.kind === "clusters" ? (
              <ClusterEntry key={e.op.id} cloudId={scope.cloud.id} op={e.op} />
            ) : (
              <OperationEntry
                key={e.op.id}
                cloudId={scope.cloud.id}
                op={e.op}
                onChanged={reload}
              />
            )
          )}
        </CloudTable>
      )}
    </section>
  )
}

const kindTile: Record<Kind, ReactNode> = {
  placement: <TargetIcon />,
  servers: <InfrastructureIcon />,
  clusters: <ClustersIcon />,
}

/**
 * ChangeRow is one change of any kind: its tile, the change as a sentence
 * with its status, who and when, what moved, and its actions.
 */
function ChangeRow({
  kind,
  title,
  status,
  meta,
  from,
  to,
  action,
  menu,
  children,
}: {
  kind: Kind
  title: ReactNode
  status: ReactNode
  meta: ReactNode[]
  from?: string
  to?: string
  /** The one visible button. */
  action?: ReactNode
  /** The rest, in the row's More menu. */
  menu?: ReactNode
  children?: ReactNode
}) {
  return (
    <CloudTableRow className="items-start">
      <Cell className="flex gap-2.5">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground [&_svg]:size-3.5">
          {kindTile[kind]}
        </span>
        <div className="grid min-w-0 flex-1 gap-1.5">
          <div className="grid gap-px">
            <p className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="font-semibold break-words">{title}</span>
              {status}
            </p>
            <p className="text-xs text-muted-foreground">
              {meta
                .filter((m) => m !== false && m !== "" && m != null)
                .map((m, i) => (
                  <span key={i}>
                    {i > 0 && " · "}
                    {m}
                  </span>
                ))}
            </p>
          </div>
          {from && to && (
            <p className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted-foreground">
              <span className="rounded-md bg-muted px-2 py-0.5">{from}</span>
              <ArrowRightIcon aria-label="to" className="size-3.5" />
              <span className="rounded-md bg-muted px-2 py-0.5 text-foreground">
                {to}
              </span>
            </p>
          )}
          {children}
        </div>
      </Cell>
      <ActionsCell>
        {action}
        {menu && <RowMenu label="More actions for this change">{menu}</RowMenu>}
      </ActionsCell>
    </CloudTableRow>
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
  const state = syncState(sync)
  // A revert is reviewed through the project that made the change.
  const revertProject = scope.project || sync.projectId
  const canRetry = ["failed", "cleanup_pending"].includes(sync.status)
  const canRevert =
    !!revertProject &&
    !movingSync(sync) &&
    !["reverted", "failed"].includes(sync.status)
  return (
    <ChangeRow
      kind="placement"
      title={title}
      status={<StatusPill tone={state.tone}>{state.label}</StatusPill>}
      meta={[
        sync.createdBy && person(sync.createdBy).name,
        <Time key="at" iso={sync.createdAt} now={now} />,
        sync.attempt > 1 && `attempt ${sync.attempt}`,
      ]}
      from={from}
      to={to}
      action={
        permitted &&
        canRetry && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => void act()}
          >
            Retry
          </Button>
        )
      }
      menu={
        permitted &&
        canRevert && (
          <DropdownMenuItem disabled={busy} onClick={() => setReverting(true)}>
            Review revert
          </DropdownMenuItem>
        )
      }
    >
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
              {waiting ? (
                <>
                  <StatusPill tone="waiting">Waiting for you</StatusPill>
                  Deployed. Point traffic here, then say what you changed.
                </>
              ) : (
                <StatePill status={env.status} />
              )}
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
    </ChangeRow>
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
  return (
    <ChangeRow
      kind="servers"
      title={`${verb} ${subject}`.trim()}
      status={<StatePill status={op.status} />}
      meta={[
        <Time key="at" iso={op.createdAt} now={now} />,
        op.input.region,
        op.attempt > 1 && `attempt ${op.attempt}`,
        op.message,
      ]}
      action={
        ["failed", "waiting", "cleanup_pending"].includes(op.status) && (
          <Button
            size="sm"
            variant="outline"
            disabled={retry.isPending}
            onClick={() => void act("retry")}
          >
            Retry
          </Button>
        )
      }
      menu={
        !op.attempted &&
        ["preview", "pending"].includes(op.status) && (
          <DropdownMenuItem
            disabled={cancel.isPending}
            onClick={() => void act("cancel")}
          >
            Cancel
          </DropdownMenuItem>
        )
      }
    >
      <CloudError message={error} />
    </ChangeRow>
  )
}

function ClusterEntry({
  cloudId,
  op,
}: {
  cloudId: string
  op: CloudClusterOperation
}) {
  const now = useNow()
  // A cluster has a page once the operation names it; retiring ends it.
  const cluster = op.kind !== "retire-cluster" ? op.resourceId : ""
  return (
    <ChangeRow
      kind="clusters"
      title={
        <Link
          className="underline-offset-2 hover:underline"
          to="/clouds/$id"
          params={{ id: cloudId }}
          search={{ tab: "infrastructure", clusterOperation: op.id }}
        >
          {clusterChangeLabel(op.kind)}: {op.input.spec.name || op.input.ref.id}
        </Link>
      }
      status={<StatePill status={op.status} />}
      meta={[
        <Time key="at" iso={op.createdAt} now={now} />,
        op.attempt > 1 && `attempt ${op.attempt}`,
      ]}
      menu={
        cluster && (
          <DropdownMenuItem
            render={
              <Link
                to="/clouds/$id"
                params={{ id: cloudId }}
                search={{ tab: "infrastructure", cluster }}
              />
            }
          >
            Open cluster
          </DropdownMenuItem>
        )
      }
    />
  )
}

export function clusterChangeLabel(kind: string) {
  return (
    (
      {
        create: "Create cluster",
        "adopt-external": "Adopt external cluster",
        "adopt-managed": "Adopt managed cluster",
        "node-pool-create": "Add node pool",
        "node-pool-update": "Change node pool",
        "node-pool-delete": "Remove node pool",
        "node-pool-scaling": "Change scaling bounds",
        "cluster-configure": "Configure maintenance",
        "cluster-upgrade": "Upgrade cluster",
        "retire-cluster": "Retire cluster",
        "detach-runtime": "Detach runtime",
        "runtime-access-upgrade": "Upgrade namespace access",
      } as Record<string, string>
    )[kind] ?? kind.replaceAll("-", " ")
  )
}
