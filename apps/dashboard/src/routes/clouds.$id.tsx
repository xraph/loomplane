import { createFileRoute } from "@tanstack/react-router"

import type { InfrastructureView } from "@/components/cloud-detail/clusters"
import type { CloudTab } from "@/components/cloud-detail/overview"
import { CloudDetail } from "@/pages/cloud-detail"

type Search = {
  project?: string
  tab?: CloudTab
  view?: InfrastructureView
  clusterOperation?: string
  cluster?: string
}

const tabs: CloudTab[] = [
  "overview",
  "targets",
  "projects",
  "infrastructure",
  "capacity",
  "changes",
  "settings",
]
const views: InfrastructureView[] = ["servers", "pools", "clusters"]

const id = (v: unknown, max: number) =>
  typeof v === "string" && v.length <= max ? v : undefined

export const Route = createFileRoute("/clouds/$id")({
  component: CloudRoute,
  // The project the Cloud belongs to; the list links with it. The tab and
  // view open the page on one of its lists, such as Capacity from a paused
  // deploy, and a cluster or cluster operation opens that page.
  validateSearch: (search: Record<string, unknown>): Search => {
    // Older links name the Infrastructure tab by its first list.
    const tab = search.tab === "servers" ? "infrastructure" : search.tab
    const out: Search = {}
    const cluster = id(search.cluster, 128)
    const clusterOperation = id(search.clusterOperation, 128)
    const project = id(search.project, 64)
    if (cluster) out.cluster = cluster
    if (clusterOperation) out.clusterOperation = clusterOperation
    if (project) out.project = project
    if (tabs.includes(tab as CloudTab)) out.tab = tab as CloudTab
    if (views.includes(search.view as InfrastructureView))
      out.view = search.view as InfrastructureView
    return out
  },
})

function CloudRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  return <CloudDetail key={id} id={id} {...search} />
}
