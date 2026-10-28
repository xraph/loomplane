import { createFileRoute } from "@tanstack/react-router"

import type { CloudTab } from "@/components/cloud-detail/overview"
import { CloudDetail } from "@/pages/cloud-detail"

type Search = { project?: string; tab?: CloudTab }

const tabs: CloudTab[] = [
  "overview",
  "targets",
  "projects",
  "servers",
  "capacity",
  "changes",
  "settings",
]

export const Route = createFileRoute("/clouds/$id")({
  component: CloudRoute,
  // The project the Cloud belongs to; the list links with it. The tab opens
  // the page on one of its views, such as Capacity from a paused deploy.
  validateSearch: (search: Record<string, unknown>): Search => ({
    ...(typeof search.project === "string" && search.project.length <= 64
      ? { project: search.project }
      : {}),
    ...(tabs.includes(search.tab as CloudTab)
      ? { tab: search.tab as CloudTab }
      : {}),
  }),
})

function CloudRoute() {
  const { id } = Route.useParams()
  const { project, tab } = Route.useSearch()
  return <CloudDetail key={id} id={id} project={project} tab={tab} />
}
