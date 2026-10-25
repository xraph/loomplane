// Fabriq, added to a project in one click: the module brings its own add
// dialog, and its service page adds the Tenants and keys tab and the button
// that opens the admin console.
import { LayersIcon } from "lucide-react"

import { useConnectionsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { FabriqEngines } from "@/components/fabriq/fabriq-engines"
import { FabriqRecovery } from "@/components/fabriq/fabriq-recovery"
import { FabriqSettings } from "@/components/fabriq/fabriq-settings"
import { AddFabriqDialog } from "@/components/fabriq/add-fabriq-dialog"
import { FabriqServices } from "@/components/fabriq/fabriq-services"
import { FabriqStack } from "@/components/fabriq/fabriq-stack"
import { FabriqTenants } from "@/components/fabriq/fabriq-tenants"
import { OpenAdminButton } from "@/components/fabriq/open-admin-button"
import { defineCatalogKind, type CatalogPanelProps } from "@/lib/catalog-kinds"

/**
 * useFabriqSubtitle is the line under the resource's name: where this one
 * runs. The service page calls it on every render for this kind, always
 * from the same place, so it may read the names it needs.
 */
function useFabriqSubtitle(
  props: CatalogPanelProps,
  environment: string
): string {
  const connections = useQuery(useConnectionsList)
  const target = connections.data?.connections.find(
    (c) => c.id === props.binding.connectionId
  )?.name
  return ["Hosted Fabriq server", environment, target]
    .filter(Boolean)
    .join(" · ")
}

function KeysPanel(props: CatalogPanelProps) {
  return (
    <div className="grid gap-[18px]">
      <FabriqStack {...props} />
      <FabriqTenants {...props} />
      <FabriqServices {...props} />
    </div>
  )
}

export default defineCatalogKind({
  kind: "fabriq",
  title: "Fabriq",
  deployedLabel: "Running",
  serviceLayout: "compact",
  defaultPanel: "keys",
  group: "data",
  icon: ({ className }) => (
    <LayersIcon
      className={`${className ?? ""} text-[#2F5C78] dark:text-sky-300`}
    />
  ),
  blurb:
    "One click sets up the whole stack. Swap any engine for one you already run.",
  features: ["Hosted server or library", "Admin console", "Postgres recovery"],
  addDialog: AddFabriqDialog,
  panels: [
    { id: "keys", title: "Tenants and keys", order: 1, component: KeysPanel },
    { id: "engines", title: "Engines", order: 2, component: FabriqEngines },
    { id: "recovery", title: "Recovery", order: 3, component: FabriqRecovery },
    { id: "settings", title: "Settings", order: 4, component: FabriqSettings },
  ],
  actions: OpenAdminButton,
  subtitle: useFabriqSubtitle,
})
