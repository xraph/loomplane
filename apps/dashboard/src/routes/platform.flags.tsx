import { createFileRoute } from "@tanstack/react-router"

import { PlatformFlags } from "@/pages/platform-flags"

export const Route = createFileRoute("/platform/flags")({
  component: PlatformFlags,
})
