import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { AgentReports } from "@/pages/agent-reports"

export const Route = createFileRoute("/agent/reports/")({
  beforeLoad: () => requireFeature("ai"),
  component: AgentReports,
})
