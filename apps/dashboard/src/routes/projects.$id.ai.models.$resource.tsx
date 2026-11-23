import { createFileRoute } from "@tanstack/react-router"

import { requireFeature } from "@/lib/features"
import { AIModel, type ModelTab } from "@/pages/ai-model"

type Search = { environment?: string; tab?: Exclude<ModelTab, "overview"> }

// One deployed model, in the environment it runs in:
// /projects/<id>/ai/models/<resource>?environment=<environment id>,
// inside the project's frame.
export const Route = createFileRoute("/projects/$id/ai/models/$resource")({
  beforeLoad: () => requireFeature("ai"),
  staticData: { projectTab: "services" },
  // An invalid value is replaced with undefined, not left out: the router
  // keeps search keys a route leaves out.
  validateSearch: (search: Record<string, unknown>): Search => ({
    environment:
      typeof search.environment === "string" &&
      search.environment.length > 0 &&
      search.environment.length <= 64
        ? search.environment
        : undefined,
    tab:
      search.tab === "example" || search.tab === "settings"
        ? search.tab
        : undefined,
  }),
  component: AIModelRoute,
})

function AIModelRoute() {
  const { id, resource } = Route.useParams()
  const { environment, tab } = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <AIModel
      // Keyed so a notice and the tabs start fresh for each model.
      key={`${id}:${resource}:${environment ?? ""}`}
      projectId={id}
      resource={resource}
      environmentId={environment ?? ""}
      tab={tab ?? "overview"}
      onTab={(next) =>
        void navigate({
          search: (s) => ({
            ...s,
            tab: next === "overview" ? undefined : next,
          }),
        })
      }
    />
  )
}
