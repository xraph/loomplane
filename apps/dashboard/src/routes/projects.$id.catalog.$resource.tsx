import { createFileRoute } from "@tanstack/react-router"

import { CatalogService } from "@/pages/catalog-service"

type Search = { environment?: string }

// One database, cache or other resource of a project, inside the
// project's frame. Its ?environment= is an environment's ID, not a name,
// so the frame offers no environment of its own.
export const Route = createFileRoute("/projects/$id/catalog/$resource")({
  component: CatalogServiceRoute,
  staticData: { projectTab: "services" },
  // The ID of the environment the resource runs in. An invalid one is
  // replaced with undefined, not left out: the router keeps search keys a
  // route leaves out.
  validateSearch: (search: Record<string, unknown>): Search => ({
    environment:
      typeof search.environment === "string" &&
      search.environment.length > 0 &&
      search.environment.length <= 64
        ? search.environment
        : undefined,
  }),
})

function CatalogServiceRoute() {
  const { id, resource } = Route.useParams()
  const { environment } = Route.useSearch()
  // Keyed so tabs start fresh for each resource and environment.
  return (
    <CatalogService
      key={`${id}:${resource}:${environment ?? ""}`}
      projectId={id}
      resource={resource}
      environmentId={environment ?? ""}
    />
  )
}
