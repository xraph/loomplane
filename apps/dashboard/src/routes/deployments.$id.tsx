import { createFileRoute } from "@tanstack/react-router"

import { DeploymentPage } from "@/pages/deployment"

export const Route = createFileRoute("/deployments/$id")({
  component: DeploymentRoute,
})

function DeploymentRoute() {
  const { id } = Route.useParams()
  // Keyed so tabs and the step filter start fresh for each deployment.
  return <DeploymentPage key={id} id={id} />
}
