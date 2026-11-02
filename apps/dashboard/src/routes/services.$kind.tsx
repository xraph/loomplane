import { createFileRoute } from "@tanstack/react-router"

import { isDatabaseKind } from "@/lib/catalog-recipes"
import { CatalogKind } from "@/pages/catalog-kind"
import { ServiceCatalog } from "@/pages/service-catalog"

export const Route = createFileRoute("/services/$kind")({
  component: ServiceRoute,
})

function ServiceRoute() {
  const { kind } = Route.useParams()
  // Keyed so switching kinds from the catalog starts the form fresh. The
  // five databases keep their own service project; every other kind is
  // added to a project that already exists.
  if (isDatabaseKind(kind)) return <ServiceCatalog key={kind} kind={kind} />
  return <CatalogKind key={kind} kind={kind} />
}
