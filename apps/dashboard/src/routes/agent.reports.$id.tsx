import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { AgentReport } from "@/pages/agent-report"

export const Route = createFileRoute("/agent/reports/$id")({
  beforeLoad: () => requireFeature("ai"),
  component: AgentReportRoute,
})

function AgentReportRoute() {
  const { id } = Route.useParams()
  return <AgentReport key={id} id={id} />
}
