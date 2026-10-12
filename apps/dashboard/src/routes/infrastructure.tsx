import { createFileRoute } from "@tanstack/react-router"

import { Infrastructure } from "@/pages/infrastructure"

export const Route = createFileRoute("/infrastructure")({
  component: Infrastructure,
})
