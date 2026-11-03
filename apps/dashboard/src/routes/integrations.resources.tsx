import { createFileRoute } from "@tanstack/react-router"

import { Integrations } from "@/pages/integrations"

type Search = { connection?: string }

// Provider resources sit with the integrations that reach them, as the
// Integrations page's third tab.
export const Route = createFileRoute("/integrations/resources")({
  validateSearch: (search: Record<string, unknown>): Search =>
    typeof search.connection === "string" && search.connection.trim()
      ? { connection: search.connection.trim() }
      : {},
  component: ResourcesRoute,
})

function ResourcesRoute() {
  const { connection } = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <Integrations
      tab="resources"
      connection={connection}
      onTab={(t) =>
        t !== "resources" &&
        void navigate({
          to: "/integrations",
          search: t === "connected" ? { tab: t } : {},
        })
      }
    />
  )
}
