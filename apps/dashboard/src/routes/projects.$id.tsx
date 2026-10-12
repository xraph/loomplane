import { createFileRoute } from "@tanstack/react-router"

import { ProjectDetail } from "@/pages/project-detail"

export const Route = createFileRoute("/projects/$id")({
  component: ProjectRoute,
})

function ProjectRoute() {
  const { id } = Route.useParams()
  return <ProjectDetail id={id} />
}
