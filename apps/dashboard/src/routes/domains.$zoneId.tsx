import { createFileRoute } from "@tanstack/react-router"

import { DomainDetailPage } from "@/pages/domain-detail"

export const Route = createFileRoute("/domains/$zoneId")({
  component: DomainRoute,
})

function DomainRoute() {
  const { zoneId } = Route.useParams()
  // IDs are short; a longer one names no zone, so nothing is read for it.
  const id = zoneId.length <= 64 ? zoneId : ""
  // Keyed so each domain's page starts fresh.
  return <DomainDetailPage key={id} zoneId={id} />
}
