import { useEffect, useRef, useState, type ReactNode } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  CloudCluster,
  CloudClusterOperation,
  Profile,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import { ClustersIcon } from "@/components/icons"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudPageHeader,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
  PageMenu,
  RowMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
  StatePill,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { Checkbox } from "@/components/ui/checkbox"
import { Button } from "@/components/ui/button"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { useOrganization } from "@/lib/organization"
import { useLiveState } from "@/lib/realtime-state"
import { clusterChangeLabel } from "./changes"
import { ClusterNodeCapacity, ClusterNodePools } from "./cluster-node-pools"
import { ClusterMaintenance } from "./cluster-maintenance"
import {
  ClusterMoney,
  microCost,
  microUSD,
  observedTime,
} from "./cluster-money"
import { ownershipPhrase, providerName } from "./clusters"

const namespaceColumns =
  "minmax(0,1.1fr) minmax(0,1fr) minmax(0,1.4fr) 110px 200px"
const retainedColumns = "minmax(0,1.4fr) minmax(0,1fr) minmax(0,1.2fr)"

/**
 * ClusterDetail is a cluster's own page under a Cloud's Infrastructure tab:
 * its node pools, facts, namespace access and what it leaves behind.
 */
export function ClusterDetail({
  cloud,
  id,
  onBack,
  onSaved,
  onOpenChanges,
  onOpenTargets,
}: {
  cloud: Profile
  id: string
  onBack: () => void
  onSaved: (id: string) => void
  onOpenChanges?: () => void
  onOpenTargets?: () => void
}) {
  const query = useQuery(api.useCloudClustersGet, {
    path: { cloudId: cloud.id, clusterId: id },
  })
  const { can } = useOrganization()
  const [kind, setKind] = useState("")
  const [poolId, setPoolId] = useState<string>()
  const [attachmentId, setAttachmentId] = useState("")
  const attachments = useQuery(api.useCloudClustersAttachments, {
    path: { cloudId: cloud.id, clusterId: id },
  })
  const projects = useQuery(api.useProjectsList)
  const live = useLiveState()
  const { refetch } = query
  useEffect(() => {
    void refetch()
  }, [refetch, live.lastSync])
  useInfrastructureRefresh(true, refetch)
  useInfrastructureRefresh(true, attachments.refetch)
  const cluster = query.data
  const parent = { label: `${cloud.name} · Infrastructure`, onClick: onBack }
  if (query.status === "error")
    return (
      <div className="grid gap-6">
        <CloudPageHeader
          parent={parent}
          glyph={<ClustersIcon className="size-[22px]" />}
          title="Cluster"
        />
        <QueryError
          error={query.error}
          retry={refetch}
          fallback="The cluster couldn't be loaded."
        />
      </div>
    )
  if (!cluster)
    return (
      <div aria-busy="true" className="grid gap-6">
        <Skeleton className="h-12 w-72" />
        <Skeleton className="h-32 rounded-xl" />
      </div>
    )
  const name = cluster.observation?.name || cluster.externalId
  const region = cluster.observation?.ref.region || "Region unknown"
  const version = cluster.observation?.version
  const manage = can("cluster:manage")
  const retire = can("cluster:delete")
  const items = attachments.data?.items ?? []
  const projectName = (projectId: string) =>
    projects.data?.projects?.find((p) => p?.id === projectId)?.name ||
    projectId ||
    "No project"
  const retained = cluster.retainedResources ?? []
  const retainedUSD = retained.reduce<number | undefined>((sum, r) => {
    const usd = microUSD(r.estimatedHourlyMicroUsd)
    return usd === undefined ? sum : (sum ?? 0) + usd
  }, undefined)
  const history = cluster.lifecycleAuthority ?? []
  const open = (next: string, attachment?: string) => {
    if (attachment !== undefined) setAttachmentId(attachment)
    setKind(next)
  }
  return (
    <div className="grid min-w-0 gap-6" aria-label="Cluster details">
      <CloudPageHeader
        parent={parent}
        glyph={<ClustersIcon className="size-[22px]" />}
        mono
        title={name}
        status={<StatePill status={cluster.status} />}
        meta={[
          "Kubernetes cluster",
          providerName(cluster.provider),
          region,
          ownershipPhrase(cluster.ownership),
          version,
        ]
          .filter(Boolean)
          .join(" · ")}
        actions={
          <>
            {manage && cluster.ownership === "managed" && (
              <>
                <Button
                  variant="outline"
                  onClick={() => open("cluster-configure")}
                >
                  Maintenance
                </Button>
                <Button
                  variant="outline"
                  onClick={() => open("cluster-upgrade")}
                >
                  Upgrade
                </Button>
              </>
            )}
            <PageMenu label="More cluster actions">
              {onOpenChanges && (
                <DropdownMenuItem onClick={onOpenChanges}>
                  Operation history
                </DropdownMenuItem>
              )}
              {onOpenTargets && (
                <DropdownMenuItem onClick={onOpenTargets}>
                  Runtime targets
                </DropdownMenuItem>
              )}
              {retire && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    variant="destructive"
                    onClick={() => open("retire-cluster")}
                  >
                    Retire cluster
                  </DropdownMenuItem>
                </>
              )}
            </PageMenu>
          </>
        }
      />
      <div className="grid min-w-0 gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)] lg:items-start">
        <ClusterNodePools
          cluster={cluster}
          canManage={manage}
          onChange={(next, pool) => {
            setPoolId(pool)
            open(next)
          }}
        />
        <section className="grid min-w-0 gap-2.5" aria-label="About">
          <CloudSectionHead
            title="About"
            description="What the provider last reported for this cluster."
          />
          <FactList
            facts={[
              {
                label: "Account",
                value: (
                  <span className="font-mono text-[12.5px] break-all">
                    {cluster.accountId || "Unknown"}
                  </span>
                ),
              },
              { label: "Region", value: region },
              {
                label: "Kubernetes version",
                value: version || "Not observed yet",
              },
              { label: "Ownership", value: ownershipPhrase(cluster.ownership) },
              {
                label: "Observed",
                value:
                  observedTime(cluster.observation?.observedAt) ??
                  "Not observed yet",
              },
              ...(retainedUSD !== undefined
                ? [{ label: "Retained cost", value: microCostOf(retainedUSD) }]
                : []),
            ]}
          />
          <p className="text-[12.5px] text-muted-foreground">
            A running cluster doesn't mean workloads, routing, storage or builds
            are ready. Each target checks its own prerequisites.
          </p>
        </section>
      </div>
      <ClusterNodeCapacity cluster={cluster} canManage={manage} />
      <section className="grid min-w-0 gap-2.5" aria-label="Namespace access">
        <CloudSectionHead
          title="Namespace access"
          count={attachments.data ? items.length : undefined}
          description="Namespaces project targets run in, and the permissions each was reviewed for."
        />
        {attachments.status === "error" ? (
          <QueryError
            error={attachments.error}
            retry={attachments.refetch}
            fallback="Namespace access couldn't be loaded."
          />
        ) : !attachments.data ? (
          <Skeleton className="h-32 rounded-xl" />
        ) : !items.length ? (
          <CloudEmpty kind="list" icon={ClustersIcon} title="No namespaces yet">
            Set up a project runtime target on this cluster to review its
            namespace access here.
          </CloudEmpty>
        ) : (
          <CloudTable
            label="Namespace access"
            columns={namespaceColumns}
            head={["Namespace", "Project", "Permissions", "Status", ""]}
          >
            {items.map((a) => (
              <CloudTableRow key={a.id}>
                <NameCell mono name={a.namespace} />
                <Cell className="text-[12.5px]">
                  {projectName(a.projectId)}
                </Cell>
                <Cell className="text-[12.5px]">
                  {a.profile?.cleanupDependents
                    ? "Cleanup granted"
                    : "Cleanup missing"}
                  {" · "}
                  {a.profile?.hpa
                    ? "Horizontal scaling granted"
                    : "Horizontal scaling missing"}
                </Cell>
                <Cell>{a.state && <StatePill status={a.state} />}</Cell>
                <ActionsCell>
                  {manage && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => open("runtime-access-upgrade", a.id)}
                    >
                      Review access upgrade
                    </Button>
                  )}
                  {retire && (
                    <RowMenu label={`More actions for ${a.namespace}`}>
                      <DropdownMenuItem
                        variant="destructive"
                        onClick={() => open("detach-runtime", a.id)}
                      >
                        Review detach
                      </DropdownMenuItem>
                    </RowMenu>
                  )}
                </ActionsCell>
              </CloudTableRow>
            ))}
          </CloudTable>
        )}
      </section>
      <section className="grid min-w-0 gap-2.5" aria-label="Lifecycle history">
        <CloudSectionHead
          title="Lifecycle history"
          count={history.length}
          description="Lifecycle actions this cluster has completed. History grants no standing permission; each action needs current authorization and a fresh review."
        />
        {!history.length ? (
          <CloudEmpty
            kind="list"
            icon={ClustersIcon}
            title="No lifecycle actions yet"
          >
            Changes you apply to this cluster are listed here once they finish.
          </CloudEmpty>
        ) : (
          <CloudTable
            label="Lifecycle history"
            columns="minmax(0,1fr)"
            head={["Action"]}
            minWidth={320}
          >
            {history.map((action, i) => (
              <CloudTableRow key={`${action}-${i}`}>
                <Cell>{clusterChangeLabel(action)}</Cell>
              </CloudTableRow>
            ))}
          </CloudTable>
        )}
      </section>
      <section className="grid min-w-0 gap-2.5" aria-label="Retained resources">
        <CloudSectionHead
          title="Retained resources"
          count={retained.length}
          description="Resources that outlive the cluster and keep costing money until they're deleted."
        />
        {!retained.length ? (
          <CloudEmpty
            kind="list"
            icon={ClustersIcon}
            title="No retained resources yet"
          >
            Volumes and load balancers kept after a change show here with what
            they cost.
          </CloudEmpty>
        ) : (
          <CloudTable
            label="Retained resources"
            columns={retainedColumns}
            head={["Resource", "Ownership", "Cost"]}
            minWidth={480}
          >
            {retained.map((r) => (
              <CloudTableRow key={r.id}>
                <NameCell mono name={r.id} meta={r.kind} />
                <Cell className="text-[12.5px]">
                  {ownershipPhrase(r.ownership)}
                </Cell>
                <Cell className="text-[12.5px] tabular-nums">
                  {microCost(r.estimatedHourlyMicroUsd)}
                </Cell>
              </CloudTableRow>
            ))}
          </CloudTable>
        )}
      </section>
      {kind && (
        <ClusterMaintenance
          cluster={cluster}
          cloud={cloud}
          kind={kind}
          poolId={poolId}
          attachmentId={attachmentId}
          attachmentHpa={
            items.find((a) => a.id === attachmentId)?.profile?.hpa ?? false
          }
          attachmentRevision={
            items.find((a) => a.id === attachmentId)?.revision
          }
          onClose={() => setKind("")}
          onSaved={onSaved}
        />
      )}
    </div>
  )
}

const microCostOf = (usd: number) =>
  microCost(String(Math.round(usd * 1_000_000)))

const clusterReasons: Record<string, string> = {
  stale_price:
    "Prices are out of date. Refresh options and create a fresh review before applying this change.",
  runtime_bootstrap_unavailable:
    "Temporary cluster management access or its trust check failed. Verify the provider account and cluster identity before retrying.",
  runtime_permissions_unavailable:
    "Cluster management access lacks required enrollment permissions. Restore those permissions before retrying.",
  runtime_namespace_setup_failed:
    "Namespace or service account setup couldn't finish. Check permissions and existing namespace ownership before retrying.",
  runtime_token_unavailable:
    "Scoped runtime access couldn't be issued. Restore TokenRequest permissions before retrying.",
  runtime_access_upgrade_required:
    "This namespace needs a separately reviewed access upgrade before cleanup or horizontal scaling. Open the cluster and review namespace access.",
  runtime_setup_incomplete:
    "Runtime setup is incomplete. Check the reviewed project grant and cluster access before retrying.",
}

export function clusterPending(op: CloudClusterOperation) {
  return ["accepted", "running", "waiting_provider"].includes(op.status)
}

/**
 * ClusterOperationDetail is one saved cluster change on its own page: what
 * it does, what it costs, and the step that waits on you.
 */
export function ClusterOperationDetail({
  cloud,
  id,
  onBack,
  onRefreshReview,
  onSaved,
}: {
  cloud: Profile
  id: string
  onBack: () => void
  onSaved?: (id: string) => void
  onRefreshReview: () => void
}) {
  const query = useQuery(api.useClusterOperationsGet, {
    path: { cloudId: cloud.id, operationId: id },
  })
  const { can } = useOrganization()
  const preview = useMutation(api.useCloudClustersPreview)
  const apply = useMutation(api.useClusterOperationsApply)
  const retry = useMutation(api.useClusterOperationsRetry)
  const cancel = useMutation(api.useClusterOperationsCancel)
  const [retirementRecovery, setRetirementRecovery] = useState<{
    cluster: CloudCluster
    attachmentRevision?: number
  }>()
  const [acknowledged, setAcknowledged] = useState(false)
  const [error, setError] = useState("")
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const submitting = useRef(false)
  const live = useLiveState()
  const { refetch: refetchOperation } = query
  useEffect(() => {
    void refetchOperation()
  }, [refetchOperation, live.lastSync])
  useInfrastructureRefresh(
    !!query.data && clusterPending(query.data),
    query.refetch
  )
  const act = async (action: "apply" | "retry" | "cancel") => {
    if (submitting.current || !query.data) return
    submitting.current = true
    setBusy(true)
    setError("")
    try {
      const path = { cloudId: cloud.id, operationId: id }
      if (action === "apply")
        await apply.mutateAsync({ path, body: { review: query.data.digest } })
      if (action === "retry") await retry.mutateAsync({ path })
      if (action === "cancel") await cancel.mutateAsync({ path })
      await query.refetch()
    } catch (err) {
      setStale(statusOf(err) === 409)
      setError(messageOf(err, "The cluster operation failed."))
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }
  async function freshReview() {
    const saved = query.data
    if (
      !saved?.resourceId ||
      ["create", "adopt-external", "adopt-managed"].includes(saved.kind) ||
      !onSaved
    ) {
      onRefreshReview()
      return
    }
    if (submitting.current) return
    submitting.current = true
    setBusy(true)
    setError("")
    try {
      const path = { cloudId: cloud.id, clusterId: saved.resourceId }
      const current = await api.useCloudClustersGet({ path }).refetch()
      if (saved.input.retirement) {
        const attachments = await api
          .useCloudClustersAttachments({ path })
          .refetch()
        const selected = attachments.items?.find(
          (a) => a.id === saved.input.retirement?.attachmentId
        )
        setAcknowledged(false)
        setRetirementRecovery({
          cluster: current,
          attachmentRevision: selected?.revision,
        })
        return
      }
      const currentCloud = await api
        .useOrganizationCloudsGet({ path: { cloudId: cloud.id } })
        .refetch()
      const options = await api
        .useCloudClustersOptions({
          path: { cloudId: cloud.id, connectionId: current.connectionId },
          query: { region: current.observation?.ref.region ?? "" },
        })
        .refetch()
      const policy = await api
        .useCloudCapacityGetPolicy({ path: { cloudId: cloud.id } })
        .refetch()
      if (!policy.policy) throw new Error("Capacity policy is unavailable.")
      const input = {
        ...saved.input,
        cloudRevision: currentCloud.revision,
        resourceRevision: current.revision,
        connectionGeneration: options.connectionGeneration,
        optionsDigest: options.digest,
        optionsCheckedAt: options.checkedAt,
        policyRevision: policy.policy.revision,
      }
      if (input.runtimeAccess)
        input.runtimeAccess = {
          attachmentId: input.runtimeAccess.attachmentId,
          profile: input.runtimeAccess.profile,
        }
      if (input.maintenance)
        input.maintenance = {
          ...input.maintenance,
          observationDigest: "",
          drainDigest: "",
          observedRevision: current.observedRevision,
        }
      if (input.scaling)
        input.scaling = {
          ...input.scaling,
          expectedRevision: Number(
            current.scalingPolicies?.[input.scaling.poolId]?.revision ?? 0
          ),
        }
      const next = await preview.mutateAsync({
        path: { cloudId: cloud.id },
        body: input,
      })
      onSaved(next.id)
    } catch (e) {
      setError(messageOf(e, "A fresh review couldn't be created."))
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }
  const op = query.data
  const parent = { label: `${cloud.name} · Infrastructure`, onClick: onBack }
  const glyph = <ClustersIcon className="size-[22px]" />
  if (query.status === "error")
    return (
      <div className="grid gap-6">
        <CloudPageHeader parent={parent} glyph={glyph} title="Cluster change" />
        <QueryError
          error={query.error}
          retry={query.refetch}
          fallback="The operation couldn't be loaded."
        />
      </div>
    )
  if (!op)
    return (
      <div aria-busy="true" className="grid gap-6">
        <Skeleton className="h-12 w-72" />
        <Skeleton className="h-32 rounded-xl" />
      </div>
    )
  const estimate = op.budgetReview?.estimate ?? op.moneyEstimate
  const unknownCost = !!estimate && !estimate.known
  const automaticUnknown = unknownCost && op.input.scaling?.mode === "native"
  const missingCategories =
    unknownCost &&
    (!(estimate.components ?? []).length ||
      (estimate.components ?? []).some((c) => !c.category))
  const needsFreshReview =
    stale || op.error === "stale_price" || missingCategories
  const setup = ["create", "adopt-external", "adopt-managed"].includes(op.kind)
  const allowed = can(
    ["retire-cluster", "detach-runtime"].includes(op.kind)
      ? "cluster:delete"
      : setup
        ? "organization_cloud:manage"
        : "cluster:manage"
  )
  const subject = op.input.spec.name || op.input.ref.id
  const canApply = op.status === "preview"
  const canRetry = ["needs_attention", "setup_incomplete"].includes(op.status)
  const canCancel = !["succeeded", "cancelled"].includes(op.status)
  const facts: { label: string; value: ReactNode }[] = [
    {
      label: "Provider",
      value: (
        <span className="break-all">
          {providerName(op.input.provider)}
          {op.input.accountId && ` · ${op.input.accountId}`}
        </span>
      ),
    },
    {
      label: "Region",
      value: op.input.spec.region || op.input.ref.region || "Unknown",
    },
  ]
  if (setup)
    facts.push(
      {
        label: "Project",
        value: op.input.attachment?.projectId ?? "No runtime attachment",
      },
      {
        label: "Kubernetes version",
        value: op.input.spec.version || "Existing version",
      },
      {
        label: "Network",
        value: (
          <span className="break-all">
            {op.input.spec.networkId || "Existing provider network"}
          </span>
        ),
      }
    )
  for (const pool of op.input.spec.nodePools ?? [])
    facts.push({
      label: `Node pool ${pool.name}`,
      value: `${pool.count} × ${pool.size}. ${
        pool.autoscale
          ? `Provider autoscaling (${pool.min} to ${pool.max}), not managed by Loomplane`
          : "Fixed capacity, autoscaling off"
      }`,
    })
  if (op.input.workerImage)
    facts.push({
      label: "Worker image",
      value: (
        <span className="font-mono text-xs break-all">
          {op.input.workerImage}
        </span>
      ),
    })
  if (setup)
    facts.push(
      {
        label: "Runtime scope",
        value: (
          <span className="break-all">
            {op.input.attachment?.namespace} /{" "}
            {op.input.attachment?.serviceAccountName} / target{" "}
            {op.input.attachment?.targetSlug}
          </span>
        ),
      },
      {
        label: "Runtime access",
        value: (
          <StatusPill
            tone={op.enrollment?.runtimeVerified ? "live" : "waiting"}
          >
            {op.enrollment?.runtimeVerified
              ? "Verified renewable access"
              : "Setup incomplete"}
          </StatusPill>
        ),
      },
      { label: "HTTP and builds", value: "Not set up by provider readiness" }
    )
  if (op.input.runtimeAccess)
    facts.push(
      {
        label: "Namespace",
        value: `${
          op.input.runtimeAccess.namespace || "Selected by attachment"
        } · project ${op.input.runtimeAccess.projectId || "of the attachment"}`,
      },
      {
        label: "Reviewed permissions",
        value: `${
          op.input.runtimeAccess.profile.cleanupDependents
            ? "Dependent cleanup"
            : "Existing cleanup permissions"
        } · ${
          op.input.runtimeAccess.profile.hpa
            ? "Horizontal scaling"
            : "Existing scaling permissions"
        }`,
      }
    )
  if (op.input.maintenance)
    facts.push({
      label: clusterChangeLabel(op.kind),
      value: [
        op.input.maintenance.pool?.name || op.input.maintenance.poolId,
        op.input.maintenance.pool &&
          `${op.input.maintenance.pool.count} nodes wanted`,
        op.input.maintenance.version &&
          `version ${op.input.maintenance.version}`,
      ]
        .filter(Boolean)
        .join(" · "),
    })
  if (op.input.scaling)
    facts.push({
      label: "Scaling",
      value: `${
        op.input.scaling.mode === "native" ? "Provider autoscaler" : "Manual"
      }, reviewed bounds ${op.input.scaling.minNodes} to ${op.input.scaling.maxNodes}`,
    })
  facts.push(
    { label: "Review revision", value: op.revision },
    {
      label: "Review expires",
      value: observedTime(op.expiresAt) ?? "Unknown",
    }
  )
  return (
    <div className="grid min-w-0 gap-6" aria-label="Cluster operation">
      <CloudPageHeader
        parent={parent}
        glyph={glyph}
        title={
          <>
            {op.kind ? clusterChangeLabel(op.kind) : "Change cluster"}
            {subject && (
              <>
                {" "}
                <span className="font-mono font-medium">{subject}</span>
              </>
            )}
          </>
        }
        status={op.status && <StatePill status={op.status} />}
        meta={`Saved operation ${op.id}. Reloading the page picks it up where it left off.`}
        actions={
          <>
            {needsFreshReview && (
              <Button onClick={() => void freshReview()} disabled={busy}>
                Refresh options and review
              </Button>
            )}
            {canApply && (
              <Button
                variant={needsFreshReview ? "outline" : "default"}
                disabled={
                  busy ||
                  needsFreshReview ||
                  !allowed ||
                  missingCategories ||
                  automaticUnknown ||
                  (unknownCost && !acknowledged)
                }
                onClick={() => void act("apply")}
              >
                Apply reviewed change
              </Button>
            )}
            {canRetry && (
              <Button
                variant={needsFreshReview ? "outline" : "default"}
                disabled={busy || !allowed || needsFreshReview}
                onClick={() => void act("retry")}
              >
                Retry saved operation
              </Button>
            )}
            {canCancel && (
              <PageMenu label="More operation actions">
                <DropdownMenuItem
                  variant="destructive"
                  disabled={busy || !allowed}
                  onClick={() => void act("cancel")}
                >
                  Request cancellation
                </DropdownMenuItem>
              </PageMenu>
            )}
          </>
        }
      />
      <CloudError message={error} />
      {retirementRecovery && (
        <ClusterMaintenance
          cluster={retirementRecovery.cluster}
          cloud={cloud}
          kind={op.kind}
          attachmentId={op.input.retirement?.attachmentId}
          attachmentRevision={retirementRecovery.attachmentRevision}
          onClose={() => setRetirementRecovery(undefined)}
          onSaved={(nextId) => {
            setRetirementRecovery(undefined)
            onSaved?.(nextId)
          }}
        />
      )}
      {op.error &&
        [
          "needs_attention",
          "setup_incomplete",
          "waiting_provider",
          "cancelled",
          "failed",
        ].includes(op.status) && (
          <div role="status">
            <CloudCallout title="This change stopped before it finished">
              {clusterReasons[op.error] ??
                `${op.error.replaceAll("_", " ")}. Review the saved state before retrying.`}{" "}
              Provider resources may still exist.
            </CloudCallout>
          </div>
        )}
      {op.status === "setup_incomplete" && (
        <CloudCallout tone="info" title="Runtime setup is incomplete">
          The cluster exists. Resume this operation after you resolve access or
          prerequisites.
        </CloudCallout>
      )}
      <section className="grid min-w-0 gap-2.5" aria-label="About this change">
        <CloudSectionHead
          title="About this change"
          description={
            op.input.retirement
              ? `${
                  op.kind === "detach-runtime"
                    ? "Detaches the selected namespace after dependency checks."
                    : "Retires only the reviewed cluster resources Loomplane owns."
                } Retained resources keep costing money, and a changed inventory needs a fresh review.`
              : "What the saved review changes. Applying it uses exactly this review."
          }
        />
        <FactList facts={facts} />
      </section>
      <section className="grid min-w-0 gap-2.5" aria-label="Cost">
        <CloudSectionHead
          title="Cost"
          description="What this change costs while it runs. Unknown charges aren't zero."
        />
        <ClusterMoney estimate={estimate} />
        {missingCategories && (
          <p role="status" className="text-[13px]">
            This saved review has incomplete cost categories. Create a fresh
            review before acknowledging unknown charges.
          </p>
        )}
        {automaticUnknown && (
          <p role="status" className="text-[13px]">
            Native growth is unavailable while required prices are unknown. A
            manual acknowledgement can't enable it.
          </p>
        )}
        {unknownCost &&
          !automaticUnknown &&
          !missingCategories &&
          op.status === "preview" && (
            <label className="flex items-start gap-2 text-[13px]">
              <Checkbox
                aria-label="Acknowledge the displayed unknown cost categories"
                checked={acknowledged}
                onCheckedChange={(v) => setAcknowledged(v === true)}
              />
              <span>
                Acknowledge the displayed unknown cost categories for this exact
                reviewed change.
              </span>
            </label>
          )}
      </section>
      {!!op.envelopeReview?.pools?.length && (
        <section className="grid min-w-0 gap-2.5" aria-label="Growth envelope">
          <CloudSectionHead
            title="Growth envelope"
            description="How many nodes each pool starts with and the most it may grow to."
          />
          <CloudTable
            label="Growth envelope"
            columns="minmax(0,1.4fr) 100px 100px 100px"
            head={["Pool", "Baseline", "Initial", "Maximum"]}
            minWidth={480}
          >
            {op.envelopeReview.pools.map((p) => (
              <CloudTableRow key={p.poolId}>
                <Cell className="font-mono text-[12.5px]">{p.poolId}</Cell>
                <Cell className="tabular-nums">{p.baseline.servers}</Cell>
                <Cell className="tabular-nums">{p.initial.servers}</Cell>
                <Cell className="tabular-nums">{p.maximum.servers}</Cell>
              </CloudTableRow>
            ))}
          </CloudTable>
        </section>
      )}
      {!!op.steps?.length && (
        <section className="grid min-w-0 gap-2.5" aria-label="Steps">
          <CloudSectionHead
            title="Steps"
            count={op.steps.length}
            description="Where the change is. Cancelling stops new attempts; accepted or uncertain provider changes still need reconciling."
          />
          <CloudTable
            label="Steps"
            columns="minmax(0,1fr) 160px"
            head={["Step", "Status"]}
            minWidth={360}
          >
            {op.steps.map((step) => (
              <CloudTableRow key={step.name}>
                <Cell>{step.name}</Cell>
                <Cell>
                  <StatePill status={step.state} />
                </Cell>
              </CloudTableRow>
            ))}
          </CloudTable>
        </section>
      )}
      {!op.steps?.length && (
        <p className="text-[12.5px] text-muted-foreground">
          Cancelling stops new attempts. Check the saved progress and retained
          resources before reviewing another change. Accepted or uncertain
          provider changes still need reconciling.
        </p>
      )}
    </div>
  )
}
