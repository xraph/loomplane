import { createFileRoute } from "@tanstack/react-router"

import {
  catalogFilters,
  ServicesCatalog,
  type CatalogFilter,
} from "@/pages/services-catalog"

type Search = { group?: CatalogFilter }

// The catalog has its own page beside Services. A link can open it on one
// group, such as the provider offerings under Managed.
export const Route = createFileRoute("/services/catalog")({
  validateSearch: (search: Record<string, unknown>): Search =>
    catalogFilters.includes(search.group as CatalogFilter) &&
    search.group !== "all"
      ? { group: search.group as CatalogFilter }
      : {},
  component: CatalogRoute,
})

function CatalogRoute() {
  const { group } = Route.useSearch()
  // Keyed so following a link to another group starts the filter there.
  return <ServicesCatalog key={group ?? "all"} initialGroup={group} />
}
