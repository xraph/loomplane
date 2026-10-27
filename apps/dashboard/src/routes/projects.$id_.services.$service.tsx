import { createFileRoute } from "@tanstack/react-router"

import { ProjectService } from "@/pages/project-service"

type Search = { environment?: string }

// Not nested under /projects/$id, which renders a whole page of its own.
export const Route = createFileRoute("/projects/$id_/services/$service")({
  component: ProjectServiceRoute,
  // The environment's name. An invalid one is replaced with undefined, not
  // left out: the router keeps search keys a route leaves out.
  validateSearch: (search: Record<string, unknown>): Search => ({
    environment:
      typeof search.environment === "string" &&
      search.environment.length > 0 &&
      search.environment.length <= 64
        ? search.environment
        : undefined,
  }),
})

function ProjectServiceRoute() {
  const { id, service } = Route.useParams()
  const { environment } = Route.useSearch()
  return (
    <ProjectService
      key={`${id}:${service}`}
      projectId={id}
      name={service}
      environment={environment}
    />
  )
}
