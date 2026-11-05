import { createFileRoute } from "@tanstack/react-router"
import { FleetDetail } from "@/pages/fleet-detail"
export const Route = createFileRoute("/projects/$id_/fleet")({
  component: FleetRoute,
})
function FleetRoute() {
  const { id } = Route.useParams()
  return <FleetDetail key={id} projectID={id} />
}
