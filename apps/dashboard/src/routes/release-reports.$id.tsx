import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { ReleaseReportPage } from "@/pages/release-report"

export const Route = createFileRoute("/release-reports/$id")({
  beforeLoad: () => requireFeature("ai"),
  component: ReleaseReportRoute,
})

function ReleaseReportRoute() {
  const { id } = Route.useParams()
  return <ReleaseReportPage key={id} id={id} />
}
