import { createFileRoute } from "@tanstack/react-router"

import { NotificationSettings } from "@/pages/notification-settings"

export const Route = createFileRoute("/notifications/settings")({
  component: NotificationSettings,
})
