import { createFileRoute } from "@tanstack/react-router"

import { Assistant } from "@/pages/assistant"

type Search = { c?: string; project?: string }

// No requireFeature here: with AI off, or the workspace's agent off, the
// page answers as an unknown address would, and asks the API nothing.
export const Route = createFileRoute("/assistant")({
  validateSearch: (search: Record<string, unknown>): Search => ({
    ...(typeof search.c === "string" && search.c ? { c: search.c } : {}),
    ...(typeof search.project === "string" && search.project
      ? { project: search.project }
      : {}),
  }),
  component: AssistantRoute,
})

function AssistantRoute() {
  const { c, project } = Route.useSearch()
  return <Assistant conversationId={c} projectId={project} />
}
