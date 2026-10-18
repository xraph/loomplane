import { createFileRoute } from "@tanstack/react-router"
import { NewDeploymentPage } from "@/pages/new-deployment"

export const Route = createFileRoute("/deployments/new")({
  validateSearch: (search: Record<string, unknown>) => ({
    project: typeof search.project === "string" ? search.project : undefined,
    environment:
      typeof search.environment === "string" ? search.environment : undefined,
  }),
  component: NewDeploymentRoute,
})

function NewDeploymentRoute() {
  const preset = Route.useSearch()
  return <NewDeploymentPage preset={preset} />
}
