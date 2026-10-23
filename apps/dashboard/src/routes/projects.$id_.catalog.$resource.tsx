import { createFileRoute } from "@tanstack/react-router"

import { CatalogService } from "@/pages/catalog-service"

type Search = { environment?: string }

// Not nested under /projects/$id, which renders a whole page of its own.
export const Route = createFileRoute("/projects/$id_/catalog/$resource")({
  component: CatalogServiceRoute,
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
