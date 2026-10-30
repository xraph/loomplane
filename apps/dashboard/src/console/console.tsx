import { ForgeDashboard } from "@forge-go/dashboard-host"
import authsomePlugin, {
  authsomeSubPlugins,
} from "@forge-go/dashboard-plugin-authsome"
import chroniclePlugin from "@forge-go/dashboard-plugin-chronicle"
import corePlugin from "@forge-go/dashboard-plugin-core"
import ctrlplanePlugin from "@forge-go/dashboard-plugin-ctrlplane"
import heraldPlugin from "@forge-go/dashboard-plugin-herald"
import keysmithPlugin from "@forge-go/dashboard-plugin-keysmith"
import relayPlugin from "@forge-go/dashboard-plugin-relay"
import trovePlugin from "@forge-go/dashboard-plugin-trove"
import wardenPlugin from "@forge-go/dashboard-plugin-warden"

import "./console.css"

// Module scope: the dashboard memoizes on object identity. A plugin shows
// only while the server registers a contributor of the same name.
//
// Two are left out on purpose. Vault: Loomplane keeps six Vault apps, all
// holding secrets, and the plugin can show only one, so which one (if any)
// is the owner's decision, not something to add back casually. Dispatch:
// its operator reads need its durable delivery audit, which Loomplane
// doesn't wire yet, so every page would answer 503.
const plugins = [
  corePlugin,
  authsomePlugin,
  ctrlplanePlugin,
  trovePlugin,
  wardenPlugin,
  chroniclePlugin,
  heraldPlugin,
  relayPlugin,
  keysmithPlugin,
]

// The Forge console, for platform admins. Loomplane serves its contract at
// /api/_console and answers 404 to anyone else.
export default function Console({ basePath }: { basePath: string }) {
  return (
    <ForgeDashboard
      config={{ basePath: "/api/_console", shellBase: basePath }}
      basename={basePath}
      plugins={plugins}
      subPlugins={authsomeSubPlugins}
    />
  )
}
