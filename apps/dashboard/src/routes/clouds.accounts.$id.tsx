import { createFileRoute } from "@tanstack/react-router"

import { CloudAccount } from "@/pages/cloud-account"

export const Route = createFileRoute("/clouds/accounts/$id")({
  component: AccountRoute,
})

function AccountRoute() {
  const { id } = Route.useParams()
  return <CloudAccount key={id} id={id} />
}
