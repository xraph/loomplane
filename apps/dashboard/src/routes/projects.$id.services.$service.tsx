import { createFileRoute } from "@tanstack/react-router"

import { serviceTabOf, type ServiceTab } from "@/lib/project-services"
import { ProjectService } from "@/pages/project-service"

type Search = { tab?: ServiceTab }

// One service of a project, inside the project's frame, which holds the
// environment it shows. An unknown tab is replaced with undefined, not
// left out: the router keeps search keys a route leaves out. It opens
// Overview.
export const Route = createFileRoute("/projects/$id/services/$service")({
  component: ProjectServiceRoute,
  staticData: { projectTab: "services", environmentScoped: true },
  validateSearch: (search: Record<string, unknown>): Search => ({
    tab: serviceTabOf(search.tab),
  }),
})

function ProjectServiceRoute() {
  const { id, service } = Route.useParams()
  const { tab } = Route.useSearch()
  return <ProjectService key={`${id}:${service}`} name={service} tab={tab} />
}
