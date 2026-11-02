import { createFileRoute } from "@tanstack/react-router"
import { ProviderResources } from "@/pages/provider-resources"

type Search = { connection?: string }
export const Route = createFileRoute("/resources/")({
  validateSearch: (search: Record<string, unknown>): Search =>
    typeof search.connection === "string" && search.connection.trim()
      ? { connection: search.connection.trim() }
      : {},
  component: ResourcesRoute,
})
function ResourcesRoute() {
  const { connection } = Route.useSearch()
  return <ProviderResources key={connection ?? "all"} connection={connection} />
}
