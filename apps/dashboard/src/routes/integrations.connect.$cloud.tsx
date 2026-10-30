import { createFileRoute } from "@tanstack/react-router"

import { ConnectCloudPage } from "@/pages/connect-cloud"

// Connecting one integration, any the API lists.
export const Route = createFileRoute("/integrations/connect/$cloud")({
  component: ConnectRoute,
})

function ConnectRoute() {
  const { cloud } = Route.useParams()
  return <ConnectCloudPage key={cloud} cloud={cloud} />
}
