import { createFileRoute } from "@tanstack/react-router"

import { serviceTabOf, type ServiceTab } from "@/lib/project-services"
import { ProjectService } from "@/pages/project-service"

type Search = { environment?: string; tab?: ServiceTab }

// Not nested under /projects/$id, which renders a whole page of its own.
export const Route = createFileRoute("/projects/$id_/services/$service")({
  component: ProjectServiceRoute,
  // The environment's name and the tab. An invalid value is replaced with
  // undefined, not left out: the router keeps search keys a route leaves
  // out. An unknown tab opens Overview.
  validateSearch: (search: Record<string, unknown>): Search => ({
    environment:
      typeof search.environment === "string" &&
      search.environment.length > 0 &&
      search.environment.length <= 64
        ? search.environment
        : undefined,
    tab: serviceTabOf(search.tab),
  }),
})

function ProjectServiceRoute() {
  const { id, service } = Route.useParams()
  const { environment, tab } = Route.useSearch()
  return (
    <ProjectService
      key={`${id}:${service}`}
      projectId={id}
      name={service}
      environment={environment}
      tab={tab}
    />
  )
}
