import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { AIModelEntry } from "@/pages/ai-model-entry"

// One model in the registry, by its Hugging Face repo:
// /ai/models/Qwen/Qwen2.5-7B-Instruct.
export const Route = createFileRoute("/ai/models/$")({
  beforeLoad: () => requireFeature("ai"),
  component: AIModelEntryRoute,
})

function AIModelEntryRoute() {
  const { _splat } = Route.useParams()
  const id = _splat ?? ""
  return <AIModelEntry key={id} id={id} />
}
