import { createFileRoute } from "@tanstack/react-router"

import { type ProjectSearch, projectSearchOf } from "@/lib/project-nav"
import { ProjectViews } from "@/pages/project-detail"

// The project's own page: one of its views, chosen by ?tab=, and the
// part of Settings by ?section=. An old tab opens where its content went.
export const Route = createFileRoute("/projects/$id/")({
  component: ProjectIndexRoute,
  // An unknown value is replaced with undefined, not left out: the router
  // keeps search keys a route leaves out.
  validateSearch: (search: Record<string, unknown>): ProjectSearch => {
    const { tab, section } = projectSearchOf(search)
    return { tab, section }
  },
})

function ProjectIndexRoute() {
  const { tab, section } = Route.useSearch()
  return <ProjectViews tab={tab} section={section} />
}
