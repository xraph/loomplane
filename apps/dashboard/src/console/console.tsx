import { ForgeDashboard } from "@forge-go/dashboard-host"
import authsomePlugin, {
  authsomeSubPlugins,
} from "@forge-go/dashboard-plugin-authsome"
import corePlugin from "@forge-go/dashboard-plugin-core"

import "./console.css"

// Module scope: the dashboard memoizes on object identity.
const plugins = [corePlugin, authsomePlugin]

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
