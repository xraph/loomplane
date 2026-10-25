import { LayersIcon } from "lucide-react"
import { FabriqLibraryPanel } from "@/components/fabriq/fabriq-library-panel"
import { FabriqEngines } from "@/components/fabriq/fabriq-engines"
import { FabriqRecovery } from "@/components/fabriq/fabriq-recovery"
import { FabriqSettings } from "@/components/fabriq/fabriq-settings"
import { defineCatalogKind } from "@/lib/catalog-kinds"

export default defineCatalogKind({
  kind: "fabriq-library",
  title: "Fabriq library",
  group: "data",
  icon: LayersIcon,
  hidden: true,
  blurb:
    "Engines, migrations and the stock Fabriq worker for a Go app that embeds Fabriq.",
  features: [
    "Application database role",
    "Dynamic entity worker",
    "Optional projections",
  ],
  panels: [
    {
      id: "library",
      title: "Library",
      order: 1,
      component: FabriqLibraryPanel,
    },
    { id: "engines", title: "Engines", order: 2, component: FabriqEngines },
    { id: "recovery", title: "Recovery", order: 3, component: FabriqRecovery },
    { id: "settings", title: "Settings", order: 4, component: FabriqSettings },
  ],
})
