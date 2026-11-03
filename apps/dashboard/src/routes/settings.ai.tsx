import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { AISettings } from "@/pages/ai-settings"

export const Route = createFileRoute("/settings/ai")({
  beforeLoad: () => requireFeature("ai"),
  component: AISettings,
})
