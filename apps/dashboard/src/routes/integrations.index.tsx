import { createFileRoute } from "@tanstack/react-router"

import { Integrations, type IntegrationsTab } from "@/pages/integrations"

type Search = { tab?: IntegrationsTab }

export const Route = createFileRoute("/integrations/")({
  component: IntegrationsRoute,
  // The catalog is the default, so only "connected" lands in the URL.
  validateSearch: (search: Record<string, unknown>): Search =>
    search.tab === "connected" ? { tab: "connected" } : {},
})

function IntegrationsRoute() {
  const { tab } = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <Integrations
      tab={tab ?? "catalog"}
      onTab={(t) =>
        void navigate({ search: t === "connected" ? { tab: t } : {} })
      }
    />
  )
}
