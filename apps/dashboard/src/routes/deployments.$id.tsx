import { createFileRoute } from "@tanstack/react-router"

import { DeploymentPage, type DeploymentTab } from "@/pages/deployment"

type Search = { tab?: DeploymentTab }

const deploymentTabs: DeploymentTab[] = [
  "changes",
  "logs",
  "services",
  "details",
]

// Older links name tabs the page has since renamed.
const renamed: Record<string, DeploymentTab> = { plan: "changes" }

export const Route = createFileRoute("/deployments/$id")({
  component: DeploymentRoute,
  // The tab opens the page on one view, such as Logs from a failed deploy.
  // An unknown tab is dropped, so the page opens on its default.
  validateSearch: (search: Record<string, unknown>): Search => {
    const tab =
      typeof search.tab === "string"
        ? (renamed[search.tab] ?? search.tab)
        : undefined
    return deploymentTabs.includes(tab as DeploymentTab)
      ? { tab: tab as DeploymentTab }
      : {}
  },
})

function DeploymentRoute() {
  const { id } = Route.useParams()
  const { tab } = Route.useSearch()
  // Keyed so tabs and the log source start fresh for each deployment.
  return <DeploymentPage key={id} id={id} tab={tab} />
}
