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
  CloudError,
  CloudLoading,
  CloudSectionHead,
  Row,
  Rows,
  StatePill,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { Button } from "@/components/ui/button"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { useLiveState } from "@/lib/realtime-state"

export function ClusterDetail({
  cluster,
  onBack,
}: {
  cluster: CloudCluster
  onBack: () => void
}) {
  const operations = useQuery(api.useClusterOperationsList, {
    path: { cloudId: cluster.cloudId },
    query: { limit: 50 },
  })
  const setup = operations.data?.items?.find(
    (op) => op.resourceId === cluster.id
  )
  const cloudPath = `/clouds/${encodeURIComponent(cluster.cloudId)}`
  return (
    <section className="grid min-w-0 gap-3" aria-label="Cluster details">
      <Button
        className="justify-self-start"
        size="sm"
        variant="ghost"
        onClick={onBack}
      >
        Back to clusters
      </Button>
      <CloudSectionHead
        title={cluster.observation?.name || cluster.externalId}
        description={`${cluster.provider} · ${cluster.observation?.ref.region ?? "Region unknown"}`}
      >
        <StatePill status={cluster.status} />
      </CloudSectionHead>
      <Rows>
        <Row>
          <span>Ownership</span>
          <span>{cluster.ownership}</span>
        </Row>
        <Row>
          <span>Lifecycle authority</span>
          <span>
            {(cluster.lifecycleAuthority ?? []).join(", ") ||
              "No lifecycle actions granted"}
          </span>
        </Row>
        <Row>
          <span>Provider state</span>
          <span>{cluster.observation?.state ?? "Unknown"}</span>
        </Row>
        <Row>
          <span>Runtime readiness</span>
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <a
              className="text-primary underline underline-offset-4"
              href={
                setup
                  ? `${cloudPath}?tab=servers&clusterOperation=${encodeURIComponent(setup.id)}`
                  : `${cloudPath}?tab=changes`
              }
            >
              {setup ? "View setup operation" : "View setup history"}
            </a>
            <a
              className="text-primary underline underline-offset-4"
              href={`${cloudPath}?tab=targets`}
            >
              View Cloud targets
            </a>
          </span>
        </Row>
        {(cluster.observation?.pools ?? []).map((pool) => (
          <Row key={pool.name}>
            <span className="min-w-0 break-words">{pool.name}</span>
            <span className="min-w-0 break-words">
              {pool.count} × {pool.size}
            </span>
          </Row>
        ))}
        {(cluster.observation?.conditions ?? []).map((condition) => (
          <Row key={condition.type}>
            <span>{condition.type}</span>
            <span>
              {condition.status} {condition.reason}
            </span>
          </Row>
        ))}
      </Rows>
      <p className="text-xs text-muted-foreground">
        Provider running does not establish workload health, HTTP routing or
        build readiness. New managed clusters support digest-pinned workers.
        Storage and builds are unavailable for managed namespace targets.
      </p>
    </section>
  )
}
const clusterReasons: Record<string, string> = {
  runtime_bootstrap_unavailable:
    "Temporary cluster management access or its trust check failed. Verify the provider account and cluster identity before retrying.",
  runtime_permissions_unavailable:
    "Cluster management access lacks required enrollment permissions. Restore those permissions before retrying.",
  runtime_namespace_setup_failed:
    "Namespace or service account setup could not finish. Check permissions and existing namespace ownership before retrying.",
  runtime_token_unavailable:
    "Scoped runtime access could not be issued. Restore TokenRequest permissions before retrying.",
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
}: {
  cloud: Profile
  id: string
  onBack: () => void
  onRefreshReview: () => void
}) {
  const query = useQuery(api.useClusterOperationsGet, {
    path: { cloudId: cloud.id, operationId: id },
  })
  const apply = useMutation(api.useClusterOperationsApply)
  const retry = useMutation(api.useClusterOperationsRetry)
  const cancel = useMutation(api.useClusterOperationsCancel)
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
  const op = query.data
  return (
    <section className="grid min-w-0 gap-3" aria-label="Cluster operation">
      <Button
        size="sm"
        variant="ghost"
        className="justify-self-start"
        onClick={onBack}
      >
        Back to clusters
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
            title={op.input.spec.name || op.input.ref.id || "Cluster setup"}
            description={`Saved operation ${op.id}`}
          >
            <StatePill status={op.status} />
          </CloudSectionHead>
          <CloudError message={error} />
          {stale && (
            <Button
              size="sm"
              className="justify-self-start"
              onClick={onRefreshReview}
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
            Review digest: {op.digest}. Revision {op.revision}. Reloading
            resumes this operation.
          </p>
          <div className="flex flex-wrap gap-2">
            {op.status === "preview" && (
              <Button
                size="sm"
                disabled={busy || stale}
                onClick={() => void act("apply")}
              >
                Apply reviewed setup
              </Button>
            )}
            {["needs_attention", "setup_incomplete"].includes(op.status) && (
              <Button
                size="sm"
                disabled={busy}
                onClick={() => void act("retry")}
              >
                Retry saved operation
              </Button>
            )}
            {!["succeeded", "cancelled"].includes(op.status) && (
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => void act("cancel")}
              >
                Request cancellation
              </Button>
            )}
          </div>
          <Rows>
            <Row>
              <span>Provider</span>
              <span className="max-w-full break-all">
                {op.input.provider} · {op.input.accountId}
              </span>
            </Row>
            <Row>
              <span>Region / project</span>
              <span>
                {op.input.spec.region || op.input.ref.region} /{" "}
                {op.input.attachment?.projectId ?? "No runtime attachment"}
              </span>
            </Row>
            <Row>
              <span>Kubernetes / network</span>
              <span className="max-w-full break-all">
                {op.input.spec.version || "Existing version"} /{" "}
                {op.input.spec.networkId || "Existing provider network"}
              </span>
            </Row>
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
            <Row>
              <span>Runtime scope</span>
              <span className="max-w-full break-all">
                {op.input.attachment?.namespace} /{" "}
                {op.input.attachment?.serviceAccountName} / target{" "}
                {op.input.attachment?.targetSlug}
              </span>
            </Row>
            <Row>
              <span>Estimated nodes</span>
              <span>
                {op.kind === "create"
                  ? op.unknownCosts?.some((cost) => cost.startsWith("nodes:"))
                    ? "Node price unknown"
                    : `$${(op.estimatedNodeHourlyMicroUsd / 1e6).toFixed(4)} / hour`
                  : "Not a purchase estimate"}
              </span>
            </Row>
            <Row>
              <span>Additional costs</span>
              <span>{op.unknownCosts?.join(", ") || "Not estimated"}</span>
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
            {op.steps.map((step) => (
              <Row key={step.name}>
                <span>{step.name}</span>
                <StatePill status={step.state} />
              </Row>
            ))}
          </Rows>
          <p className="text-xs text-muted-foreground">
            Cancellation does not delete provider resources. Purchases remain
            limited to configured qualification operators until cleanup
            qualification passes.
          </p>
        </>
      )}
    </section>
  )
}
