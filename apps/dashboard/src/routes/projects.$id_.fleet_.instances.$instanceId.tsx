import { createFileRoute } from "@tanstack/react-router"
import { InstanceDetail } from "@/pages/instance-detail"
export const Route = createFileRoute(
  "/projects/$id_/fleet_/instances/$instanceId"
)({ component: InstanceRoute })
function InstanceRoute() {
  const { id, instanceId } = Route.useParams()
  return (
    <InstanceDetail
      key={`${id}:${instanceId}`}
      projectID={id}
      instanceID={instanceId}
    />
  )
}
