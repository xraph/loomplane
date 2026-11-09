import { createFileRoute } from "@tanstack/react-router"
import { NewDeploymentPage } from "@/pages/new-deployment"

type Search = {
  project?: string
  environment?: string
  /** service preselects only this service on the deploy screen. */
  service?: string
}

const text = (v: unknown) => (typeof v === "string" && v ? v : undefined)

export const Route = createFileRoute("/deployments/new")({
  validateSearch: (search: Record<string, unknown>): Search => ({
    project: text(search.project),
    environment: text(search.environment),
    service: text(search.service),
  }),
  component: NewDeploymentRoute,
})

function NewDeploymentRoute() {
  const preset = Route.useSearch()
  return <NewDeploymentPage preset={preset} />
}
