import { createFileRoute } from "@tanstack/react-router"

import { PlatformCloud } from "@/pages/platform-cloud"

export const Route = createFileRoute("/platform/cloud")({
  component: PlatformCloud,
})
