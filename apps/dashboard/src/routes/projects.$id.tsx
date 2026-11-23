import { createFileRoute, Outlet, useMatches } from "@tanstack/react-router"

import { type FramePlace, ProjectFrame } from "@/components/project-frame"
import { environmentSearchOf, projectSearchOf } from "@/lib/project-nav"

type Search = { environment?: string }

// Every page of a project opens inside its frame: the project's header,
// the nav between its views, and the environment a page shows. The
// project's own views are the index route; a service, a deployment, a
// database and the fleet are the routes under it.
export const Route = createFileRoute("/projects/$id")({
  component: ProjectLayout,
  // The environment's name, which every page under the project shares.
  // An invalid one is replaced with undefined, not left out: the router
  // keeps search keys a route leaves out.
  validateSearch: (search: Record<string, unknown>): Search => ({
    environment: environmentSearchOf(search.environment),
  }),
})

function ProjectLayout() {
  const { id } = Route.useParams()
  const { environment } = Route.useSearch()
  // The deepest page says where in the project it is; the project's own
  // page says it with ?tab=.
  const leaf = useMatches({ select: (matches) => matches[matches.length - 1] })
  let place: FramePlace
  if (leaf.routeId === "/projects/$id/") {
    const tab =
      projectSearchOf(leaf.search as Record<string, unknown>).tab ?? "overview"
    place = { tab, index: true, environmentScoped: tab === "variables" }
  } else {
    place = {
      tab: leaf.staticData.projectTab,
      environmentScoped: leaf.staticData.environmentScoped,
    }
  }
  return (
    <ProjectFrame key={id} id={id} environment={environment} place={place}>
      <Outlet />
    </ProjectFrame>
  )
}
