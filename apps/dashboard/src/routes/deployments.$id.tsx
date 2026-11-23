import { createFileRoute } from "@tanstack/react-router"

import {
  DeploymentRedirect,
  type DeploymentTab,
  deploymentTabOf,
} from "@/pages/deployment"

type Search = { tab?: DeploymentTab }

// A deployment's page is under its project now. Links and bookmarks that
// name only the deployment find its project and move there, keeping the
// tab they asked for.
export const Route = createFileRoute("/deployments/$id")({
  component: DeploymentRoute,
  validateSearch: (search: Record<string, unknown>): Search => ({
    tab: deploymentTabOf(search.tab),
  }),
})

function DeploymentRoute() {
  const { id } = Route.useParams()
  const { tab } = Route.useSearch()
  return <DeploymentRedirect key={id} id={id} tab={tab} />
}
