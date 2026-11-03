import { createFileRoute } from "@tanstack/react-router"

import { NodePage } from "@/pages/node"

export const Route = createFileRoute("/nodes/$id")({ component: NodeRoute })

function NodeRoute() {
  const { id } = Route.useParams()
  return <NodePage key={id} id={id} />
}
