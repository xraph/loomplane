import { PlusIcon } from "lucide-react"
import * as api from "@/client/forge/loomplane/hooks"
import type { CloudCluster } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { useEffect, useState } from "react"
import { messageOf } from "@/client/errors"
import { PoolsIcon } from "@/components/icons"
import {
  ActionsCell,
  Cell,
  CloudTable,
  CloudTableRow,
  NameCell,
  RowMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudError,
  CloudSectionHead,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { useLiveState } from "@/lib/realtime-state"
import { useNow } from "@/lib/use-runs"
import { observedTime } from "./cluster-money"

const poolColumns =
  "minmax(0,1.2fr) minmax(0,1fr) minmax(0,0.9fr) minmax(0,1.3fr) 110px"

const nodes = (n: number) => `${n} ${n === 1 ? "node" : "nodes"}`

/**
 * ClusterNodePools lists a cluster's node pools. Each row opens the change
 * for that pool; adding a pool is the section's one create action.
 */
export function ClusterNodePools({
  cluster,
  canManage,
  onChange,
}: {
  cluster: CloudCluster
  canManage: boolean
  onChange: (kind: string, poolId?: string) => void
}) {
  const manage = canManage && cluster.ownership === "managed"
  const pools = cluster.observation?.pools ?? []
  const add = manage && (
    <Button size="sm" onClick={() => onChange("node-pool-create")}>
      <PlusIcon />
      Add node pool
    </Button>
  )
  const observed = observedTime(cluster.observation?.observedAt)
  return (
    <section className="grid min-w-0 gap-2.5" aria-label="Node pools">
      <CloudSectionHead
        title="Node pools"
        count={pools.length}
        description={`The machines this cluster runs on. ${observed ? `Observed ${observed}.` : "Not observed yet."}`}
      >
        {add}
      </CloudSectionHead>
      {!pools.length ? (
        <CloudEmpty
          kind="list"
          icon={PoolsIcon}
          title="No node pools yet"
          action={add || undefined}
        >
          The provider reported no node pools. Refresh its observation, or add a
          pool to give the cluster machines.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Node pools"
          columns={poolColumns}
          head={["Pool", "Machine", "Nodes", "Scaling", ""]}
        >
          {pools.map((pool) => {
            const policy = cluster.scalingPolicies?.[pool.id ?? ""]
            const min = policy?.min_nodes ?? policy?.minNodes
            const max = policy?.max_nodes ?? policy?.maxNodes
            const converging =
              policy?.mode === "native" &&
              (!pool.autoscale || pool.min !== min || pool.max !== max)
            return (
              <CloudTableRow key={pool.id || pool.name}>
                <NameCell
                  mono
                  tile={<PoolsIcon />}
                  name={pool.name}
                  meta={
                    pool.autoscale ? "Provider autoscaler" : "Fixed node count"
                  }
                />
                <Cell className="font-mono text-[12.5px]">{pool.size}</Cell>
                <Cell>
                  <span className="grid gap-px">
                    <span className="tabular-nums">{nodes(pool.count)}</span>
                    {pool.autoscale && (
                      <span className="text-xs text-muted-foreground">
                        Provider bounds {pool.min} to {pool.max}
                      </span>
                    )}
                  </span>
                </Cell>
                <Cell>
                  {policy ? (
                    <span className="grid gap-1">
                      <span className="text-[12.5px]">
                        {policy.mode === "native"
                          ? "Provider autoscaler"
                          : "Manual"}
                        , reviewed bounds {min ?? "unknown"} to{" "}
                        {max ?? "unknown"}
                      </span>
                      {converging && (
                        <StatusPill tone="flight">
                          Waiting on provider
                        </StatusPill>
                      )}
                      {policy.pause_reason && (
                        <span className="text-xs text-muted-foreground">
                          Paused:{" "}
                          {String(policy.pause_reason).replaceAll("_", " ")}
                        </span>
                      )}
                    </span>
                  ) : (
                    <span className="text-[12.5px] text-muted-foreground">
                      Not reviewed
                    </span>
                  )}
                </Cell>
                <ActionsCell>
                  {manage && (
                    <>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Edit node pool ${pool.name}`}
                        onClick={() => onChange("node-pool-update", pool.id)}
                      >
                        Edit
                      </Button>
                      <RowMenu
                        label={`More actions for node pool ${pool.name}`}
                      >
                        <DropdownMenuItem
                          onClick={() => onChange("node-pool-update", pool.id)}
                        >
                          Change node count
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          onClick={() => onChange("node-pool-scaling", pool.id)}
                        >
                          Scaling
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => onChange("node-pool-delete", pool.id)}
                        >
                          Remove node pool
                        </DropdownMenuItem>
                      </RowMenu>
                    </>
                  )}
                </ActionsCell>
              </CloudTableRow>
            )
          })}
        </CloudTable>
      )}
    </section>
  )
}

const vcpu = (millis?: number) =>
  millis == null ? "unknown" : `${Number((millis / 1000).toFixed(2))}`
const gib = (bytes?: number) =>
  bytes == null ? "unknown" : `${Number((bytes / 2 ** 30).toFixed(1))}`

const nodeColumns =
  "minmax(0,1fr) minmax(0,0.8fr) 110px minmax(0,1fr) minmax(0,1fr)"

/**
 * ClusterNodeCapacity is what each node can take and what workloads ask for,
 * from the last capacity observation.
 */
export function ClusterNodeCapacity({
  cluster,
  canManage,
}: {
  cluster: CloudCluster
  canManage: boolean
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
      setError(messageOf(e, "Capacity couldn't be refreshed."))
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
  const nodeList = data?.nodes ?? []
  const conditions = data?.conditions ?? []
  return (
    <section className="grid min-w-0 gap-2.5" aria-label="Node capacity">
      <CloudSectionHead
        title="Node capacity"
        count={data ? nodeList.length : undefined}
        description="What each node can take and what workloads ask for. Requests aren't guaranteed reservations, and a missing request is unknown, not zero."
      >
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
      </CloudSectionHead>
      <CloudError message={error} />
      {capacity.status === "error" ? (
        <QueryError
          error={capacity.error}
          retry={capacity.refetch}
          fallback="Capacity couldn't be loaded."
        />
      ) : !data ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : (
        <>
          <p className="text-[12.5px] text-muted-foreground">
            {`${fresh ? "Fresh" : "Stale"} · ${data.complete && data.workloadsComplete ? "Complete" : "Incomplete"} · checked ${observedTime(data.checkedAt) ?? "at an unknown time"}`}
          </p>
          {nodeList.length > 0 && (
            <CloudTable
              label="Node capacity"
              columns={nodeColumns}
              head={[
                "Pool",
                "Zone",
                "Status",
                "CPU requested",
                "Memory requested",
              ]}
            >
              {nodeList.map((node, i) => (
                <CloudTableRow key={i}>
                  <Cell className="font-mono text-[12.5px]">{node.poolId}</Cell>
                  <Cell className="text-[12.5px]">
                    {node.zone || "Zone unknown"}
                  </Cell>
                  <Cell>
                    {node.ready && !node.unschedulable ? (
                      <StatusPill tone="live">Ready</StatusPill>
                    ) : (
                      <StatusPill tone="failed">Unavailable</StatusPill>
                    )}
                  </Cell>
                  <Cell className="text-[12.5px] tabular-nums">
                    {vcpu(node.requests.cpuMillis)} of{" "}
                    {vcpu(node.allocatable.cpuMillis)} vCPU
                  </Cell>
                  <Cell className="text-[12.5px] tabular-nums">
                    {gib(node.requests.memoryBytes)} of{" "}
                    {gib(node.allocatable.memoryBytes)} GiB
                  </Cell>
                </CloudTableRow>
              ))}
            </CloudTable>
          )}
          {conditions.length > 0 && (
            <CloudTable
              label="Capacity conditions"
              columns="minmax(0,1fr) 120px minmax(0,1.6fr)"
              head={["Condition", "Status", "Reason"]}
              minWidth={480}
            >
              {conditions.map((c, i) => (
                <CloudTableRow key={i}>
                  <Cell className="font-mono text-[12.5px]">{c.type}</Cell>
                  <Cell className="text-[12.5px]">{c.status}</Cell>
                  <Cell className="text-[12.5px] text-muted-foreground">
                    {c.reason?.replaceAll("_", " ")}
                  </Cell>
                </CloudTableRow>
              ))}
            </CloudTable>
          )}
          <p className="text-[12.5px] text-muted-foreground">
            Best-effort workloads get no guaranteed reservation. Provider
            autoscaling stays within its reviewed maximum.
          </p>
        </>
      )}
    </section>
  )
}
