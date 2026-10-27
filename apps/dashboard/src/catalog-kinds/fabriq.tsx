// Fabriq, added to a project in one click: the module brings its own add
// dialog, and its service page adds the Tenants and keys tab and the button
// that opens the admin console.
import { useFabriqSubtitle } from "@/components/fabriq/use-fabriq-subtitle"
import { FabriqIcon } from "@/components/fabriq/fabriq-icon"
import { FabriqEngines } from "@/components/fabriq/fabriq-engines"
import { FabriqRecovery } from "@/components/fabriq/fabriq-recovery"
import { FabriqSettings } from "@/components/fabriq/fabriq-settings"
import { AddFabriqDialog } from "@/components/fabriq/add-fabriq-dialog"
import { FabriqServices } from "@/components/fabriq/fabriq-services"
import { FabriqStack } from "@/components/fabriq/fabriq-stack"
import { FabriqTenants } from "@/components/fabriq/fabriq-tenants"
import { OpenAdminButton } from "@/components/fabriq/open-admin-button"
import { defineCatalogKind, type CatalogPanelProps } from "@/lib/catalog-kinds"

function KeysPanel(props: CatalogPanelProps) {
  return (
    <div className="grid gap-5">
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
  defaultPanel: "keys",
  group: "data",
  icon: FabriqIcon,
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
