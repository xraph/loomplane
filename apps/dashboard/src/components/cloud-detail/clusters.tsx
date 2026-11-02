import { PlusIcon } from "lucide-react"
import { useEffect, useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type { Profile } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { statusOf } from "@/client/errors"
import { ClustersIcon } from "@/components/icons"
import {
  CloudEmpty,
  CloudSectionHead,
  StatePill,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudTable,
  CloudTableRow,
  NameCell,
  Segments,
} from "@/components/cloud-targets/cloud-page"
import { Servers } from "@/components/cloud-targets/servers"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { QueryError } from "@/components/query-error"
import { useOrganization } from "@/lib/organization"
import { useLiveState } from "@/lib/realtime-state"
import { ClusterCreate } from "./cluster-create"

/** The lists inside the Infrastructure tab. */
export type InfrastructureView = "servers" | "pools" | "clusters"

export function CloudInfrastructure({
  cloud,
  view,
  onView,
  onOpenCluster,
  onOpenOperation,
}: {
  cloud: Profile
  view: InfrastructureView
  onView: (view: InfrastructureView) => void
  onOpenCluster: (id: string) => void
  onOpenOperation: (id: string) => void
}) {
  const servers = useQuery(api.useCloudServersList, {
    path: { cloudId: cloud.id },
  })
  const pools = useQuery(api.useCloudPoolsList, { path: { cloudId: cloud.id } })
  return (
    <div className="grid gap-4">
      <Segments
        label="Infrastructure"
        value={view}
        onChange={onView}
        items={[
          {
            id: "servers",
            label: "Servers",
            count: servers.data?.servers.length,
          },
          { id: "pools", label: "Pools", count: pools.data?.pools.length },
          { id: "clusters", label: "Clusters" },
        ]}
      />
      {view === "clusters" ? (
        <Clusters
          cloud={cloud}
          onOpenCluster={onOpenCluster}
          onOpenOperation={onOpenOperation}
        />
      ) : (
        <Servers cloud={cloud} view={view} />
      )}
    </div>
  )
}

export function Clusters({
  cloud,
  onOpenCluster,
  onOpenOperation,
}: {
  cloud: Profile
  onOpenCluster: (id: string) => void
  onOpenOperation: (id: string) => void
}) {
  const { can } = useOrganization()
  if (!can("organization_cloud:manage"))
    return (
      <CloudEmpty
        kind="notice"
        icon={ClustersIcon}
        title="Clusters are managed by your organization"
      >
        You can use the targets your project was given. An organization manager
        sets up clusters and the accounts they run in.
      </CloudEmpty>
    )
  return (
    <ClusterInventory
      cloud={cloud}
      onOpenCluster={onOpenCluster}
      onOpenOperation={onOpenOperation}
    />
  )
}

const clusterColumns = "minmax(0,1.1fr) minmax(0,1fr) 110px 150px 90px"

function ClusterInventory({
  cloud,
  onOpenCluster,
  onOpenOperation,
}: {
  cloud: Profile
  onOpenCluster: (id: string) => void
  onOpenOperation: (id: string) => void
}) {
  const [cursor, setCursor] = useState("")
  const [search, setSearch] = useState("")
  const [creating, setCreating] = useState(false)
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
    `${c.observation?.name ?? c.externalId} ${c.provider} ${c.observation?.ref.region ?? ""} ${c.status}`
      .toLowerCase()
      .includes(search.toLowerCase())
  )
  const paged = !!cursor || !!clusters.data?.nextCursor
  const create = (
    <Button size="sm" onClick={() => setCreating(true)}>
      <PlusIcon />
      Create cluster
    </Button>
  )
  return (
    <section className="grid gap-2.5" aria-label="Clusters">
      <CloudSectionHead
        title="Kubernetes clusters"
        count={clusters.data ? all.length : undefined}
        description="Clusters this Cloud runs services on. Loomplane creates managed ones and connects to ones you run yourself."
      >
        <Input
          aria-label="Filter clusters"
          placeholder="Filter clusters"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="h-[30px] w-48"
        />
        {create}
      </CloudSectionHead>
      {clusters.status === "error" ? (
        statusOf(clusters.error) === 403 ? (
          <CloudCallout tone="info" title="Cluster access is restricted">
            Ask an organization manager to review your permissions.
          </CloudCallout>
        ) : (
          <QueryError
            error={clusters.error}
            retry={clusters.refetch}
            fallback="Clusters couldn't be loaded."
          />
        )
      ) : !clusters.data ? (
        <Skeleton className="h-32 rounded-xl" />
      ) : !items.length ? (
        <CloudEmpty
          kind="list"
          icon={ClustersIcon}
          title={search ? `No clusters match "${search}"` : "No clusters yet"}
          action={
            search ? (
              <Button size="sm" variant="outline" onClick={() => setSearch("")}>
                Clear filter
              </Button>
            ) : (
              create
            )
          }
        >
          {search
            ? "The filter checks names, providers, regions and statuses on this page."
            : "Create a cluster in a connected account, or connect one you already run. Each opens a review first."}
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Kubernetes clusters"
          columns={clusterColumns}
          head={["Cluster", "Where", "Version", "Status", ""]}
          footer={
            <>
              <span>
                {search
                  ? `${items.length} of ${all.length} on this page match`
                  : `Showing ${all.length}`}
              </span>
              {paged && (
                <span className="flex gap-1.5">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={!cursor}
                    onClick={() => setCursor("")}
                  >
                    Newest
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={!clusters.data?.nextCursor}
                    onClick={() => setCursor(clusters.data?.nextCursor ?? "")}
                  >
                    Older
                  </Button>
                </span>
              )}
            </>
          }
        >
          {items.map((cluster) => {
            const name = cluster.observation?.name || cluster.externalId
            return (
              <CloudTableRow key={cluster.id}>
                <NameCell
                  mono
                  tile={<ClustersIcon />}
                  name={
                    <button
                      type="button"
                      className="text-left underline-offset-2 hover:underline"
                      onClick={() => onOpenCluster(cluster.id)}
                    >
                      {name}
                    </button>
                  }
                  meta={ownershipPhrase(cluster.ownership)}
                />
                <Cell className="text-[12.5px]">
                  {providerName(cluster.provider)} ·{" "}
                  {cluster.observation?.ref.region || "Region unknown"}
                </Cell>
                <Cell className="font-mono text-[12.5px]">
                  {cluster.observation?.version || "Not observed yet"}
                </Cell>
                <Cell>
                  <StatePill status={cluster.status} />
                </Cell>
                <ActionsCell>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => onOpenCluster(cluster.id)}
                  >
                    Open
                  </Button>
                </ActionsCell>
              </CloudTableRow>
            )
          })}
        </CloudTable>
      )}
      {creating && (
        <ClusterCreate
          cloud={cloud}
          onClose={() => setCreating(false)}
          onSaved={(id) => {
            setCreating(false)
            void clusters.refetch()
            onOpenOperation(id)
          }}
        />
      )}
    </section>
  )
}

const providerNames: Record<string, string> = {
  aws: "AWS",
  gcp: "Google Cloud",
  azure: "Azure",
  digitalocean: "DigitalOcean",
  kubernetes: "Kubernetes",
}

/** providerName is a provider as its brand, never its key. */
export function providerName(provider: string): string {
  return providerNames[provider] ?? provider
}

/**
 * ownershipPhrase says who runs a cluster or a resource, never the raw
 * ownership key.
 */
export function ownershipPhrase(ownership: string): string {
  if (ownership === "managed" || ownership === "owned")
    return "Managed by Loomplane"
  if (ownership === "unknown" || !ownership) return "Owner unknown"
  return "Connected, you run it"
}
