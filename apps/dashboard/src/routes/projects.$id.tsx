import { createFileRoute } from "@tanstack/react-router"

import { type ProjectTab, ProjectDetail } from "@/pages/project-detail"

type Search = {
  tab?: ProjectTab
  environment?: string
}

const projectTabs: ProjectTab[] = [
  "overview",
  "services",
  "deployments",
  "variables",
  "cloud",
  "analyses",
  "backups",
  "settings",
]

// Older links name tabs the page has since renamed.
const renamed: Record<string, ProjectTab> = {
  graph: "overview",
  deploy: "deployments",
}

export const Route = createFileRoute("/projects/$id")({
  component: ProjectRoute,
  // The tab opens the page on one view, and the environment names the one
  // a tab was opened on, such as Variables for staging. An unknown tab is
  // dropped, so the page opens on its default.
  validateSearch: (search: Record<string, unknown>): Search => {
    const out: Search = {}
    const raw = typeof search.tab === "string" ? search.tab : ""
    const tab = renamed[raw] ?? raw
    if ((projectTabs as string[]).includes(tab)) out.tab = tab as ProjectTab
    if (
      typeof search.environment === "string" &&
      search.environment.length <= 64
    )
      out.environment = search.environment
    return out
  },
})

function ProjectRoute() {
  const { id } = Route.useParams()
  const { tab, environment } = Route.useSearch()
  return <ProjectDetail id={id} tab={tab} environment={environment} />
}
