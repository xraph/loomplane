import { createFileRoute } from "@tanstack/react-router"

import { Notifications } from "@/pages/notifications"

export const Route = createFileRoute("/notifications")({
  component: Notifications,
})
