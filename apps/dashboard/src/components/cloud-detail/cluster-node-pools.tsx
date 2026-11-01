import * as api from "@/client/forge/loomplane/hooks"
import type { CloudCluster } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { useEffect, useState } from "react"
import { messageOf } from "@/client/errors"
import {
  CloudEmpty,
  CloudError,
  CloudLoading,
  CloudSectionHead,
  Rows,
  Row,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"

import { useLiveState } from "@/lib/realtime-state"
import { useNow } from "@/lib/use-runs"
import { observedTime } from "./cluster-money"

export function ClusterNodePools({
  cluster,
  canManage,
  onChange,
}: {
  cluster: CloudCluster
  canManage: boolean
  onChange: (kind: string) => void
}) {
  const capacity = useQuery(api.useCloudClustersCapacity, {
    path: { cloudId: cluster.cloudId, clusterId: cluster.id },
  })
  const refresh = useMutation(api.useCloudClustersRefreshCapacity)
  const [error, setError] = useState("")
  async function refreshCapacity() {
    setError("")
    try {
      await refresh.mutateAsync({
        path: { cloudId: cluster.cloudId, clusterId: cluster.id },
      })
      await capacity.refetch()
    } catch (e) {
      setError(messageOf(e, "Capacity refresh failed."))
    }
  }
  const live = useLiveState()
  const { refetch } = capacity
  useEffect(() => {
    void refetch().catch(() => undefined)
  }, [refetch, live.lastSync, cluster.observedRevision])
  useInfrastructureRefresh(true, refetch)
  const now = useNow(1000)
  const data = capacity.data
  const checked = Date.parse(data?.checkedAt ?? "")
  const fresh =
    !!data?.fresh &&
    checked > 0 &&
    checked <= now &&
    now - checked <= 2 * 60_000
  return (
    <section className="grid gap-3" aria-label="Node pools">
      <CloudSectionHead
        title="Node pools"
        description={`${cluster.observation?.version || "Version unknown"} · observed ${observedTime(cluster.observation?.observedAt) ?? "time unknown"}`}
      >
        {canManage && cluster.ownership === "managed" && (
          <div className="flex flex-wrap gap-2">
            {[
              ["node-pool-create", "Add pool"],
              ["node-pool-update", "Change nodes"],
              ["node-pool-scaling", "Scaling"],
              ["node-pool-delete", "Remove pool"],
            ].map(([kind, label]) => (
              <Button
                key={kind}
                size="sm"
                variant="outline"
                onClick={() => onChange(kind)}
              >
                {label}
              </Button>
            ))}
          </div>
        )}
      </CloudSectionHead>
      {!cluster.observation?.pools?.length ? (
        <CloudEmpty kind="compact" title="No observed node pools">
          Refresh the provider observation before changing capacity.
        </CloudEmpty>
      ) : (
        <Rows>
          {cluster.observation.pools.map((pool) => {
            const policy = cluster.scalingPolicies?.[pool.id ?? ""]
            return (
              <Row key={pool.id || pool.name}>
                <span className="min-w-0 break-words">
                  {pool.name}
                  <span className="block text-xs text-muted-foreground">
                    {pool.size} ·{" "}
                    {pool.autoscale
                      ? "Provider native controller"
                      : "Manual count"}
                  </span>
                </span>
                <span className="min-w-0 text-right text-sm">
                  Observed {pool.count} nodes
                  {pool.autoscale && ` · bounds ${pool.min} to ${pool.max}`}
                  {policy && (
                    <span className="block text-xs">
                      Reviewed {policy.mode} · bounds{" "}
                      {policy.min_nodes ?? policy.minNodes ?? "unknown"} to{" "}
                      {policy.max_nodes ?? policy.maxNodes ?? "unknown"}
                      {policy.mode === "native" &&
                      (!pool.autoscale ||
                        pool.min !== (policy.min_nodes ?? policy.minNodes) ||
                        pool.max !== (policy.max_nodes ?? policy.maxNodes))
                        ? " · Awaiting provider convergence"
                        : ""}
                      {policy.pause_reason
                        ? ` · ${String(policy.pause_reason).replaceAll("_", " ")}`
                        : ""}
                    </span>
                  )}
                </span>
              </Row>
            )
          })}
        </Rows>
      )}
      <details>
        <summary className="cursor-pointer text-sm font-medium">
          Capacity observation and workload requests
        </summary>
        <div className="mt-2 grid gap-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-xs">
              {data
                ? `${fresh ? "Fresh" : "Stale"} · ${data.complete && data.workloadsComplete ? "Complete" : "Incomplete"} · checked ${observedTime(data.checkedAt) ?? "time unavailable"}`
                : "Observation unavailable"}
            </p>
            {canManage && (
              <Button
                size="sm"
                variant="outline"
                disabled={refresh.isPending}
                onClick={() => void refreshCapacity()}
              >
                Refresh capacity
              </Button>
            )}
          </div>
          <CloudError message={error} />
          {capacity.status === "error" ? (
            <QueryError
              error={capacity.error}
              retry={capacity.refetch}
              fallback="Capacity could not be loaded."
            />
          ) : !data ? (
            <CloudLoading>Loading capacity...</CloudLoading>
          ) : (
            <>
              <Rows>
                {(data.nodes ?? []).map((node, i) => (
                  <Row key={i}>
                    <span>
                      {node.poolId} · {node.zone || "Zone unknown"} ·{" "}
                      {node.ready && !node.unschedulable
                        ? "Ready"
                        : "Unavailable"}
                    </span>
                    <span>
                      CPU {node.requests.cpuMillis ?? "unknown"} /{" "}
                      {node.allocatable.cpuMillis ?? "unknown"} millicores · RAM{" "}
                      {node.requests.memoryBytes == null
                        ? "unknown"
                        : `${Math.round(node.requests.memoryBytes / 1048576)} MiB`}{" "}
                      /{" "}
                      {node.allocatable.memoryBytes == null
                        ? "unknown"
                        : `${Math.round(node.allocatable.memoryBytes / 1048576)} MiB`}
                    </span>
                  </Row>
                ))}
              </Rows>
              {(data.conditions ?? []).map((c, i) => (
                <p key={i} className="text-xs">
                  {c.type}: {c.status} {c.reason?.replaceAll("_", " ")}
                </p>
              ))}
            </>
          )}
          <p className="text-xs text-muted-foreground">
            Requests and observations are distinct from guaranteed reservations.
            Missing requests are unknown. Best-effort workloads receive no
            guaranteed reservation; provider autoscaling remains within its
            reviewed maximum.
          </p>
        </div>
      </details>
    </section>
  )
}
