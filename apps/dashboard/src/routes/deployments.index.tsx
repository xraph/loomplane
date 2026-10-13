import { createFileRoute } from "@tanstack/react-router"

import { Deployments, runFilters, type RunFilter } from "@/pages/deployments"

type Search = { status?: RunFilter }

export const Route = createFileRoute("/deployments/")({
  // An unknown status is dropped rather than shown as an empty list.
  validateSearch: (search: Record<string, unknown>): Search => {
    const status = runFilters.find((f) => f.value === search.status)?.value
    return status && status !== "all" ? { status } : {}
  },
  component: DeploymentsRoute,
})

function DeploymentsRoute() {
  const { status } = Route.useSearch()
  // The router can keep a search key the validator dropped, so check again.
  const known = runFilters.find((f) => f.value === status)?.value
  return <Deployments status={known ?? "all"} />
}
