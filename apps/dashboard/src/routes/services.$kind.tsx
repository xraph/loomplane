import { createFileRoute } from "@tanstack/react-router"

import { PageNotFound } from "@/components/route-feedback"
import { isDatabaseKind } from "@/lib/catalog-recipes"
import { ServiceCatalog } from "@/pages/service-catalog"

export const Route = createFileRoute("/services/$kind")({
  component: ServiceRoute,
})

function ServiceRoute() {
  const { kind } = Route.useParams()
  if (!isDatabaseKind(kind)) return <PageNotFound />
  // Keyed so switching engines from the catalog starts the form fresh.
  return <ServiceCatalog key={kind} kind={kind} />
}
