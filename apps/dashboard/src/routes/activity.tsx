import { createFileRoute } from "@tanstack/react-router"

import { Activity } from "@/pages/activity"

export const Route = createFileRoute("/activity")({
  component: Activity,
})
