// Octopus, the API gateway: its service page shows the routes on each
// domain, wherever they came from, the domains attached to it, what FARP
// discovered, its settings and the button that opens its admin.
import { useOctopusBoard } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { DiscoveryPanel } from "@/components/octopus/discovery-panel"
import { DomainsPanel } from "@/components/octopus/domains-panel"
import { OpenAdminButton } from "@/components/octopus/open-admin-button"
import { RoutesPanel } from "@/components/octopus/routes-panel"
import { SettingsPanel } from "@/components/octopus/settings-panel"
import { useOctopusSubtitle } from "@/components/octopus/use-octopus-subtitle"
import { defineCatalogKind, type CatalogPanelProps } from "@/lib/catalog-kinds"
import { attachedHosts } from "@/lib/octopus"

/** useBoard reads the board the tabs and their counts share. */
function useBoard({ projectId, environmentId, resource }: CatalogPanelProps) {
  return useQuery(useOctopusBoard, {
    path: { projectId, environmentId, resource },
  }).data
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
  subtitle: useOctopusSubtitle,
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
      component: DomainsPanel,
      useCount: (props) => {
        const board = useBoard(props)
        return board && attachedHosts(board).length
      },
    },
    {
      id: "discovery",
      title: "Discovery",
      order: 3,
      component: DiscoveryPanel,
    },
    { id: "settings", title: "Settings", order: 4, component: SettingsPanel },
  ],
})
