// Octopus, the API gateway: its service page shows the routes on each
// domain, wherever they came from, and the button that opens its admin.
import { useOctopusBoard } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { OpenAdminButton } from "@/components/octopus/open-admin-button"
import { RoutesPanel } from "@/components/octopus/routes-panel"
import { defineCatalogKind, type CatalogPanelProps } from "@/lib/catalog-kinds"

/** useBoard reads the board the tabs and their counts share. */
function useBoard({ projectId, environmentId, resource }: CatalogPanelProps) {
  return useQuery(useOctopusBoard, {
    path: { projectId, environmentId, resource },
  }).data
}

// The Domains, Discovery and Settings tabs are drawn in their own change.
function Pending() {
  return null
}

export default defineCatalogKind({
  kind: "octopus",
  title: "Octopus",
  group: "gateway",
  blurb: "API gateway that routes your services by domain and path.",
  features: [
    "Routes discovered over FARP",
    "Routes you add by domain and path",
    "Domains from your organization",
  ],
  actions: OpenAdminButton,
  panels: [
    {
      id: "routes",
      title: "Routes",
      order: 1,
      component: RoutesPanel,
      useCount: (props) => useBoard(props)?.counts.all,
    },
    {
      id: "domains",
      title: "Domains",
      order: 2,
      component: Pending,
      useCount: (props) =>
        useBoard(props)?.hosts.filter((h) => h.hostname && h.attached).length,
    },
    { id: "discovery", title: "Discovery", order: 3, component: Pending },
    { id: "settings", title: "Settings", order: 4, component: Pending },
  ],
})
