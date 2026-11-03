import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { AgentRequests } from "@/pages/agent-requests"

type Search = { id?: string }

export const Route = createFileRoute("/agent/requests")({
  beforeLoad: () => requireFeature("ai"),
  // A notice links to /agent/requests?id=<request>, which is shown marked.
  validateSearch: (search: Record<string, unknown>): Search =>
    typeof search.id === "string" && search.id ? { id: search.id } : {},
  component: AgentRequestsRoute,
})

function AgentRequestsRoute() {
  const { id } = Route.useSearch()
  return <AgentRequests highlight={id} />
}
