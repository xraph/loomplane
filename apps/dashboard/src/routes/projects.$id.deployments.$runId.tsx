import { createFileRoute } from "@tanstack/react-router"

import {
  DeploymentPage,
  type DeploymentTab,
  deploymentTabOf,
} from "@/pages/deployment"

type Search = { tab?: DeploymentTab }

// One deployment of a project, inside the project's frame. The tab opens
// the page on one view, such as Logs from a failed deploy; an unknown one
// is replaced with undefined, so the page opens on its default.
export const Route = createFileRoute("/projects/$id/deployments/$runId")({
  component: ProjectDeploymentRoute,
  staticData: { projectTab: "deployments" },
  validateSearch: (search: Record<string, unknown>): Search => ({
    tab: deploymentTabOf(search.tab),
  }),
})

function ProjectDeploymentRoute() {
  const { id, runId } = Route.useParams()
  const { tab } = Route.useSearch()
  // Keyed so tabs and the log source start fresh for each deployment.
  return <DeploymentPage key={runId} id={runId} projectId={id} tab={tab} />
}
