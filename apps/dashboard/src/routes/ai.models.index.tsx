import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { modelsSearch, type ModelsSearch } from "@/lib/models"
import { AIModels } from "@/pages/ai-models"

export const Route = createFileRoute("/ai/models/")({
  beforeLoad: () => requireFeature("ai"),
  // Filters live in the URL. Anything the page doesn't know is dropped.
  validateSearch: (search: Record<string, unknown>): ModelsSearch =>
    modelsSearch(search),
  component: AIModelsRoute,
})

function AIModelsRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AIModels
      // The router can keep a search key the validator dropped, so check
      // again.
      search={modelsSearch(search)}
      // A new tab is a step Back returns from; a filter replaces the last.
      onSearch={(next) =>
        void navigate({ search: next, replace: next.tab === search.tab })
      }
    />
  )
}
