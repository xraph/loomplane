import { Link } from "@/lib/router"
import { useEffect, useRef, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  CloudCluster,
  CloudClusterOperation,
  Profile,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import {
  CloudEmpty,
  CloudError,
  CloudLoading,
  CloudSectionHead,
  Row,
  Rows,
  StatePill,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { Checkbox } from "@/components/ui/checkbox"
import { Button } from "@/components/ui/button"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { useOrganization } from "@/lib/organization"
import { ClusterNodePools } from "./cluster-node-pools"
import { ClusterMaintenance } from "./cluster-maintenance"
import { ClusterMoney, exactUSD, observedTime } from "./cluster-money"
import { useLiveState } from "@/lib/realtime-state"

export function ClusterDetail({
  cloud,
  id,
  onBack,
  onSaved,
}: {
  cloud: Profile
  id: string
  onBack: () => void
  onSaved: (id: string) => void
}) {
  const query = useQuery(api.useCloudClustersGet, {
    path: { cloudId: cloud.id, clusterId: id },
  })
  const { can } = useOrganization()
  const [kind, setKind] = useState("")
  const [attachmentId, setAttachmentId] = useState("")
  const attachments = useQuery(api.useCloudClustersAttachments, {
    path: { cloudId: cloud.id, clusterId: id },
  })
  const live = useLiveState()
  const { refetch } = query
  useEffect(() => {
    void refetch()
  }, [refetch, live.lastSync])
  useInfrastructureRefresh(true, refetch)
  useInfrastructureRefresh(true, attachments.refetch)
  const cluster = query.data
  return (
    <section className="grid min-w-0 gap-3" aria-label="Cluster details">
      <Button
        className="justify-self-start"
        size="sm"
        variant="ghost"
        onClick={onBack}
      >
        Back to infrastructure
      </Button>
      {query.status === "error" ? (
        <QueryError
          error={query.error}
          retry={refetch}
          fallback="Cluster could not be loaded."
        />
      ) : !cluster ? (
        <CloudLoading>Loading cluster...</CloudLoading>
      ) : (
        <>
          <CloudSectionHead
            title={cluster.observation?.name || cluster.externalId}
            description={`${cluster.provider} · ${cluster.observation?.ref.region || "Region unknown"} · ${cluster.ownership}`}
          >
            <StatePill status={cluster.status} />
          </CloudSectionHead>
          {!!cluster.observation?.pools?.length && (
            <ClusterNodePools
              cluster={cluster}
              canManage={can("cluster:manage")}
              onChange={setKind}
            />
          )}
          <div className="flex flex-wrap gap-2">
            {can("cluster:manage") && cluster.ownership === "managed" && (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setKind("cluster-configure")}
                >
                  Maintenance
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setKind("cluster-upgrade")}
                >
                  Upgrade
                </Button>
              </>
            )}
            {can("cluster:delete") && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => setKind("retire-cluster")}
              >
                Review retirement
              </Button>
            )}
            <Link
              className="self-center text-xs text-primary underline"
              to="/clouds/$id"
              params={{ id: cloud.id }}
              search={{ tab: "changes" }}
            >
              Operation history
            </Link>
            <Link
              className="self-center text-xs text-primary underline"
              to="/clouds/$id"
              params={{ id: cloud.id }}
              search={{ tab: "targets" }}
            >
              Runtime targets
            </Link>
          </div>
          <section className="grid gap-2" aria-label="Namespace access">
            <CloudSectionHead
              title="Namespace access"
              description="Current attachments and separately reviewed permissions."
            />
            {attachments.status === "error" ? (
              <QueryError
                error={attachments.error}
                retry={attachments.refetch}
                fallback="Attachments could not be loaded."
              />
            ) : !attachments.data ? (
              <CloudLoading>Loading attachments...</CloudLoading>
            ) : !(attachments.data.items ?? []).length ? (
              <CloudEmpty kind="compact" title="No active runtime attachments">
                Set up a project runtime target to review its namespace access
                here.
              </CloudEmpty>
            ) : (
              <Rows>
                {(attachments.data.items ?? []).map((a) => (
                  <Row key={a.id}>
                    <span>
                      {a.namespace}
                      <span className="block text-xs text-muted-foreground">
                        {a.profile?.cleanupDependents
                          ? "Cleanup permission granted"
                          : "Cleanup permission missing"}{" "}
                        ·{" "}
                        {a.profile?.hpa
                          ? "Horizontal scaling permission granted"
                          : "Horizontal scaling permission missing"}
                      </span>
                    </span>
                    <span className="flex flex-wrap gap-2">
                      {can("cluster:manage") && (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            setAttachmentId(a.id)
                            setKind("runtime-access-upgrade")
                          }}
                        >
                          Review access upgrade
                        </Button>
                      )}
                      {can("cluster:delete") && (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            setAttachmentId(a.id)
                            setKind("detach-runtime")
                          }}
                        >
                          Review detach
                        </Button>
                      )}
                    </span>
                  </Row>
                ))}
              </Rows>
            )}
          </section>
          {!cluster.observation?.pools?.length && (
            <ClusterNodePools
              cluster={cluster}
              canManage={can("cluster:manage")}
              onChange={setKind}
            />
          )}
          <details>
            <summary className="cursor-pointer text-sm font-medium">
              Lifecycle history and retained resources
            </summary>
            <div className="mt-2 grid gap-2">
              <p className="text-xs">
                Completed lifecycle actions:{" "}
                {(cluster.lifecycleAuthority ?? []).join(", ") ||
                  "None recorded"}
                . History grants no standing permission. Each action requires
                current authorization and a fresh review.
              </p>
              <Rows>
                {(cluster.retainedResources ?? []).map((r) => (
                  <Row key={r.id}>
                    <span className="break-all">
                      {r.kind} · {r.id}
                    </span>
                    <span>
                      {r.ownership} · {exactUSD(r.estimatedHourlyMicroUsd)} /
                      hour
                    </span>
                  </Row>
                ))}
              </Rows>
              <p className="text-xs">
                Provider running does not establish workload health, routing,
                storage or build readiness. Each target uses its reviewed
                prerequisites and supported workload profile.
              </p>
            </div>
          </details>
          {kind && (
            <ClusterMaintenance
              cluster={cluster}
              cloud={cloud}
              kind={kind}
              attachmentId={attachmentId}
              attachmentHpa={
                attachments.data?.items?.find((a) => a.id === attachmentId)
                  ?.profile?.hpa ?? false
              }
              attachmentRevision={
                attachments.data?.items?.find((a) => a.id === attachmentId)
                  ?.revision
              }
              onClose={() => setKind("")}
              onSaved={onSaved}
            />
          )}
        </>
      )}
    </section>
  )
}
const clusterReasons: Record<string, string> = {
  stale_price:
    "Prices are out of date. Refresh options and create a fresh review before applying this change.",
  runtime_bootstrap_unavailable:
    "Temporary cluster management access or its trust check failed. Verify the provider account and cluster identity before retrying.",
  runtime_permissions_unavailable:
    "Cluster management access lacks required enrollment permissions. Restore those permissions before retrying.",
  runtime_namespace_setup_failed:
    "Namespace or service account setup could not finish. Check permissions and existing namespace ownership before retrying.",
  runtime_token_unavailable:
    "Scoped runtime access could not be issued. Restore TokenRequest permissions before retrying.",
  runtime_access_upgrade_required:
    "This namespace needs a separately reviewed access upgrade before cleanup or horizontal scaling. Open the cluster and review namespace access.",
  runtime_setup_incomplete:
    "Runtime setup is incomplete. Check the reviewed project grant and cluster access before retrying.",
}

export function clusterPending(op: CloudClusterOperation) {
  return ["accepted", "running", "waiting_provider"].includes(op.status)
}
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
      setError(messageOf(err, "Cluster operation failed."))
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
      setError(messageOf(e, "A fresh review could not be created."))
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }
  const op = query.data
  const estimate = op?.budgetReview?.estimate ?? op?.moneyEstimate
  const unknownCost = !!estimate && !estimate.known
  const automaticUnknown = unknownCost && op?.input.scaling?.mode === "native"
  const missingCategories =
    unknownCost &&
    (!(estimate.components ?? []).length ||
      (estimate.components ?? []).some((c) => !c.category))
  const needsFreshReview =
    stale || op?.error === "stale_price" || missingCategories
  const setup =
    !!op && ["create", "adopt-external", "adopt-managed"].includes(op.kind)
  const allowed =
    !!op &&
    can(
      ["retire-cluster", "detach-runtime"].includes(op.kind)
        ? "cluster:delete"
        : ["create", "adopt-external", "adopt-managed"].includes(op.kind)
          ? "organization_cloud:manage"
          : "cluster:manage"
    )
  return (
    <section className="grid min-w-0 gap-3" aria-label="Cluster operation">
      <Button
        size="sm"
        variant="ghost"
        className="justify-self-start"
        onClick={onBack}
      >
        Back to infrastructure
      </Button>
      {query.status === "error" ? (
        <QueryError
          error={query.error}
          retry={query.refetch}
          fallback="Operation couldn't be loaded."
        />
      ) : !op ? (
        <CloudLoading>Loading saved operation...</CloudLoading>
      ) : (
        <>
          <CloudSectionHead
            title={op.input.spec.name || op.input.ref.id || "Cluster change"}
            description={`Saved operation ${op.id}`}
          >
            <StatePill status={op.status} />
          </CloudSectionHead>
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

          {needsFreshReview && (
            <Button
              size="sm"
              className="justify-self-start"
              onClick={() => void freshReview()}
              disabled={busy}
            >
              Refresh options and review
            </Button>
          )}
          {op.error &&
            [
              "needs_attention",
              "setup_incomplete",
              "waiting_provider",
              "cancelled",
              "failed",
            ].includes(op.status) && (
              <p role="status" className="text-sm">
                {clusterReasons[op.error] ??
                  `${op.error.replaceAll("_", " ")}. Review the saved state before retrying.`}{" "}
                Provider resources may still exist.
              </p>
            )}
          {op.status === "setup_incomplete" && (
            <p className="text-sm">
              The cluster exists, but runtime setup is incomplete. Resume this
              operation after resolving access or prerequisites.
            </p>
          )}
          <p className="text-xs break-all text-muted-foreground">
            Review revision {op.revision}. Expires{" "}
            {observedTime(op.expiresAt) ?? "an unavailable time"}. Reloading
            resumes this operation.
          </p>
          <div className="flex flex-wrap gap-2">
            {op.status === "preview" && (
              <Button
                size="sm"
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
            {["needs_attention", "setup_incomplete"].includes(op.status) && (
              <Button
                size="sm"
                disabled={busy || !allowed || needsFreshReview}
                onClick={() => void act("retry")}
              >
                Retry saved operation
              </Button>
            )}
            {!["succeeded", "cancelled"].includes(op.status) && (
              <Button
                size="sm"
                variant="outline"
                disabled={busy || !allowed}
                onClick={() => void act("cancel")}
              >
                Request cancellation
              </Button>
            )}
          </div>
          <ClusterMoney estimate={estimate} />
          {missingCategories && (
            <p role="status" className="text-sm">
              This saved review has incomplete cost categories. Create a fresh
              review before acknowledging unknown charges.
            </p>
          )}
          {automaticUnknown && (
            <p role="status" className="text-sm">
              Native growth is unavailable while required prices are unknown. A
              manual acknowledgement cannot enable it.
            </p>
          )}
          {unknownCost &&
            !automaticUnknown &&
            !missingCategories &&
            op.status === "preview" && (
              <label className="flex items-start gap-2 text-sm">
                <Checkbox
                  aria-label="Acknowledge the displayed unknown cost categories"
                  checked={acknowledged}
                  onCheckedChange={(v) => setAcknowledged(v === true)}
                />
                <span>
                  Acknowledge the displayed unknown cost categories for this
                  exact reviewed change.
                </span>
              </label>
            )}
          {op.input.runtimeAccess && (
            <Rows>
              <Row>
                <span>Namespace access</span>
                <span>
                  {op.input.runtimeAccess.namespace ||
                    "Namespace selected by attachment"}{" "}
                  · project{" "}
                  {op.input.runtimeAccess.projectId ||
                    "Current attachment project"}
                </span>
              </Row>
              <Row>
                <span>Reviewed permissions</span>
                <span>
                  {op.input.runtimeAccess.profile.cleanupDependents
                    ? "Dependent cleanup"
                    : "Existing cleanup permissions"}{" "}
                  ·{" "}
                  {op.input.runtimeAccess.profile.hpa
                    ? "Horizontal scaling"
                    : "Existing scaling permissions"}
                </span>
              </Row>
            </Rows>
          )}
          {op.input.retirement && (
            <p className="text-sm">
              {op.kind === "detach-runtime"
                ? "Detach the selected runtime attachment after dependency checks."
                : "Retire only the reviewed owned cluster resources."}{" "}
              Retained resources remain chargeable. A changed inventory requires
              a fresh review.
            </p>
          )}
          {op.input.maintenance && (
            <p className="text-sm">
              {op.kind.replaceAll("-", " ")}:{" "}
              {op.input.maintenance.pool?.name || op.input.maintenance.poolId}
              {op.input.maintenance.pool &&
                ` · desired ${op.input.maintenance.pool.count} nodes`}
              {op.input.maintenance.version &&
                ` · version ${op.input.maintenance.version}`}
            </p>
          )}
          {op.input.scaling && (
            <p className="text-sm">
              Count controller: {op.input.scaling.mode}. Reviewed bounds{" "}
              {op.input.scaling.minNodes} to {op.input.scaling.maxNodes}.
              Provider native removals are independent of Loomplane manual
              removal reviews.
            </p>
          )}
          {op.envelopeReview?.pools?.map((p) => (
            <p key={p.poolId} className="text-xs">
              {p.poolId}: baseline {p.baseline.servers}, initial{" "}
              {p.initial.servers}, maximum {p.maximum.servers} nodes.
            </p>
          ))}
          <Rows>
            <Row>
              <span>Provider</span>
              <span className="max-w-full break-all">
                {op.input.provider} · {op.input.accountId}
              </span>
            </Row>
            <Row>
              <span>{setup ? "Region / project" : "Region"}</span>
              <span>
                {op.input.spec.region || op.input.ref.region || "Unknown"}
                {setup &&
                  ` / ${op.input.attachment?.projectId ?? "No runtime attachment"}`}
              </span>
            </Row>
            {setup && (
              <Row>
                <span>Kubernetes / network</span>
                <span className="max-w-full break-all">
                  {op.input.spec.version || "Existing version"} /{" "}
                  {op.input.spec.networkId || "Existing provider network"}
                </span>
              </Row>
            )}
            {(op.input.spec.nodePools ?? []).map((pool) => (
              <Row key={pool.name}>
                <span>Node pool</span>
                <span className="max-w-full break-words">
                  {pool.name}: {pool.count} × {pool.size}.{" "}
                  {pool.autoscale
                    ? `Provider autoscaling (${pool.min} to ${pool.max}); not managed by Loomplane`
                    : "Fixed capacity; autoscaling off"}
                </span>
              </Row>
            ))}
            {op.input.workerImage && (
              <Row>
                <span>Worker image</span>
                <span className="max-w-full text-xs break-all">
                  {op.input.workerImage}
                </span>
              </Row>
            )}
            {setup && (
              <>
                <Row>
                  <span>Runtime scope</span>
                  <span className="max-w-full break-all">
                    {op.input.attachment?.namespace} /{" "}
                    {op.input.attachment?.serviceAccountName} / target{" "}
                    {op.input.attachment?.targetSlug}
                  </span>
                </Row>
                <Row>
                  <span>Runtime access</span>
                  <StatusPill
                    tone={op.enrollment?.runtimeVerified ? "live" : "waiting"}
                  >
                    {op.enrollment?.runtimeVerified
                      ? "Verified renewable access"
                      : "Setup incomplete"}
                  </StatusPill>
                </Row>
                <Row>
                  <span>HTTP / builds</span>
                  <span>Not established by provider readiness</span>
                </Row>
              </>
            )}
            {(op.steps ?? []).map((step) => (
              <Row key={step.name}>
                <span>{step.name}</span>
                <StatePill status={step.state} />
              </Row>
            ))}
          </Rows>
          <p className="text-xs text-muted-foreground">
            Cancellation stops new attempts. Check the saved progress and
            retained resources before reviewing another change. Accepted or
            uncertain provider changes still need reconciliation.
          </p>
        </>
      )}
    </section>
  )
}
