import { ForgeDashboard } from "@forge-go/dashboard-host"
import authsomePlugin, {
  authsomeSubPlugins,
} from "@forge-go/dashboard-plugin-authsome"
import chroniclePlugin from "@forge-go/dashboard-plugin-chronicle"
import corePlugin from "@forge-go/dashboard-plugin-core"
import ctrlplanePlugin from "@forge-go/dashboard-plugin-ctrlplane"
import dispatchPlugin from "@forge-go/dashboard-plugin-dispatch"
import heraldPlugin from "@forge-go/dashboard-plugin-herald"
import keysmithPlugin from "@forge-go/dashboard-plugin-keysmith"
import relayPlugin from "@forge-go/dashboard-plugin-relay"
import trovePlugin from "@forge-go/dashboard-plugin-trove"
import wardenPlugin from "@forge-go/dashboard-plugin-warden"

import "./console.css"

// Module scope: the dashboard memoizes on object identity. A plugin shows
// only while the server registers a contributor of the same name. Vault
// isn't here: Loomplane keeps several Vault apps and the console has none
// to show yet.
const plugins = [
  corePlugin,
  authsomePlugin,
  ctrlplanePlugin,
  dispatchPlugin,
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
