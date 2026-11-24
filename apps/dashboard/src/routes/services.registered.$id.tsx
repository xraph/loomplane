import { createFileRoute } from "@tanstack/react-router"

import { WorkspaceService } from "@/pages/workspace-service"

// One workspace service a person registered by its connection string.
export const Route = createFileRoute("/services/registered/$id")({
  component: WorkspaceServiceRoute,
})

function WorkspaceServiceRoute() {
  const { id } = Route.useParams()
  // Keyed so its forms start fresh for each service.
  return <WorkspaceService key={id} id={id} />
}
