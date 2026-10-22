import { createFileRoute } from "@tanstack/react-router"

import { CloudDetail } from "@/pages/cloud-detail"

type Search = { project?: string }

export const Route = createFileRoute("/clouds/$id")({
  component: CloudRoute,
  // The project the Cloud belongs to; the list links with it.
  validateSearch: (search: Record<string, unknown>): Search =>
    typeof search.project === "string" && search.project.length <= 64
      ? { project: search.project }
      : {},
})

function CloudRoute() {
  const { id } = Route.useParams()
  const { project } = Route.useSearch()
  return <CloudDetail key={id} id={id} project={project} />
}
