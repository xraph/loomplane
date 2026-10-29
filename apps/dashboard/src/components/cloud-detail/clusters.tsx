import { useEffect, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { CloudCluster, Profile } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { statusOf } from "@/client/errors"
import {
  CloudEmpty,
  CloudLoading,
  CloudSectionHead,
  Row,
  Rows,
  StatePill,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { Servers } from "@/components/cloud-targets/servers"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { QueryError } from "@/components/query-error"
import { useOrganization } from "@/lib/organization"
import { useLiveState } from "@/lib/realtime-state"
import { ClusterCreate } from "./cluster-create"
import { ClusterDetail, ClusterOperationDetail } from "./cluster-detail"

export function saveClusterOperation(id: string) {
  const url = new URL(window.location.href)
  if (id) {
    url.searchParams.set("clusterOperation", id)
    url.searchParams.set("tab", "servers")
  } else url.searchParams.delete("clusterOperation")
  window.history.replaceState(window.history.state, "", url)
}

export function CloudInfrastructure({ cloud }: { cloud: Profile }) {
  const [filter, setFilter] = useState(
    new URLSearchParams(window.location.search).has("clusterOperation")
      ? "clusters"
      : "servers"
  )
  return (
    <div className="grid gap-3">
      <div
        role="group"
        aria-label="Infrastructure type"
        className="flex flex-wrap gap-1"
      >
        {(["servers", "clusters"] as const).map((kind) => (
          <Button
            key={kind}
            size="sm"
            variant={filter === kind ? "secondary" : "ghost"}
            onClick={() => setFilter(kind)}
          >
            {kind === "servers" ? "Servers" : "Clusters"}
          </Button>
        ))}
      </div>
      {filter === "servers" ? (
        <Servers cloud={cloud} />
      ) : (
        <Clusters cloud={cloud} />
      )}
    </div>
  )
}
export function Clusters({ cloud }: { cloud: Profile }) {
  const { can } = useOrganization()
  if (!can("organization_cloud:manage"))
    return (
      <CloudEmpty kind="compact" title="Cluster management is restricted">
        You can use granted project targets. An organization manager reviews
        cluster setup and provider accounts.
      </CloudEmpty>
    )
  return <ClusterInventory cloud={cloud} />
}
function ClusterInventory({ cloud }: { cloud: Profile }) {
  const [cursor, setCursor] = useState("")
  const [search, setSearch] = useState("")
  const [creating, setCreating] = useState(false)
  const [selected, setSelected] = useState<CloudCluster | null>(null)
  const [operation, setOperation] = useState(
    () =>
      new URLSearchParams(window.location.search).get("clusterOperation") ?? ""
  )
  const clusters = useQuery(api.useCloudClustersList, {
    path: { cloudId: cloud.id },
    query: { cursor },
  })
  const live = useLiveState()
  const { refetch: refetchClusters } = clusters
  useEffect(() => {
    void refetchClusters()
  }, [live.lastSync, refetchClusters])
  useInfrastructureRefresh(true, clusters.refetch)
  const all = clusters.data?.items ?? []
  const items = all.filter((c) =>
    `${c.observation?.name ?? c.externalId} ${c.provider} ${c.status}`
      .toLowerCase()
      .includes(search.toLowerCase())
  )
  const openOperation = (id: string) => {
    setOperation(id)
    saveClusterOperation(id)
    setCreating(false)
    void clusters.refetch()
  }
  if (operation)
    return (
      <ClusterOperationDetail
        cloud={cloud}
        id={operation}
        onBack={() => openOperation("")}
        onRefreshReview={() => {
          openOperation("")
          setCreating(true)
        }}
      />
    )
  if (selected)
    return <ClusterDetail cluster={selected} onBack={() => setSelected(null)} />
  return (
    <section className="grid gap-3" aria-label="Clusters">
      <CloudSectionHead
        title="Kubernetes clusters"
        count={all.length}
        description="Provider lifecycle and project runtime access have separate readiness checks."
      >
        <Button size="sm" onClick={() => setCreating(true)}>
          Set up cluster
        </Button>
      </CloudSectionHead>
      <Input
        aria-label="Filter clusters"
        placeholder="Filter this page of clusters"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />
      {clusters.status === "error" ? (
        statusOf(clusters.error) === 403 ? (
          <CloudEmpty kind="compact" title="Cluster access is restricted">
            Ask an organization manager to review your permissions.
          </CloudEmpty>
        ) : (
          <QueryError
            error={clusters.error}
            retry={clusters.refetch}
            fallback="Clusters couldn't be loaded."
          />
        )
      ) : !clusters.data ? (
        <CloudLoading>Loading clusters...</CloudLoading>
      ) : !items.length ? (
        <CloudEmpty
          kind="compact"
          title={search ? "No matching clusters" : "No clusters in this Cloud"}
          action={
            <Button
              size="sm"
              variant="outline"
              onClick={() => (search ? setSearch("") : setCreating(true))}
            >
              {search ? "Clear filter" : "Set up cluster"}
            </Button>
          }
        >
          {search
            ? "Try a different name or status."
            : "Create a qualified worker cluster or adopt an existing cluster with an explicit review."}
        </CloudEmpty>
      ) : (
        <Rows>
          {items.map((cluster) => (
            <Row key={cluster.id}>
              <button
                className="min-w-0 flex-1 text-left text-sm font-medium hover:underline"
                onClick={() => setSelected(cluster)}
              >
                <span className="block break-words">
                  {cluster.observation?.name || cluster.externalId}
                </span>
                <span className="text-xs font-normal text-muted-foreground">
                  {cluster.observation?.ref.region} · {cluster.ownership} ·{" "}
                  {cluster.observation?.version}
                </span>
              </button>
              <StatePill status={cluster.status} />
            </Row>
          ))}
        </Rows>
      )}
      {(cursor || clusters.data?.nextCursor) && (
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={!cursor}
            onClick={() => setCursor("")}
          >
            First page
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!clusters.data?.nextCursor}
            onClick={() => setCursor(clusters.data?.nextCursor ?? "")}
          >
            Next page
          </Button>
        </div>
      )}
      {creating && (
        <ClusterCreate
          cloud={cloud}
          onClose={() => setCreating(false)}
          onSaved={openOperation}
        />
      )}
    </section>
  )
}
