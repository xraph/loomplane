import { ActivityIcon, KeyRoundIcon } from "@forge-go/dashboard-kit/icons"
import { definePlugin } from "@forge-go/dashboard-plugin"

import { ConnectionsPage } from "./pages/connections"
import { IntegrationPage } from "./pages/integration"
import { IntegrationsPage } from "./pages/integrations"

/**
 * The OAuth integrations pages in the console: the apps people sign in to
 * clouds and repository hosts with, and the health of every signed-in cloud
 * connection.
 *
 * `extension` is the backend contributor's name ("integrations", in
 * extensions/connections/console_manifest.yaml), which is also this plugin's
 * URL segment.
 */
export const integrationsPlugin = definePlugin({
  extension: "integrations",
  namespace: "integrations",
  label: "Integrations",
  nav: [
    {
      label: "OAuth apps",
      to: "/",
      priority: 0,
      icon: <KeyRoundIcon />,
      group: "Integrations",
    },
    {
      label: "Sign-in health",
      to: "/connections",
      priority: 10,
      icon: <ActivityIcon />,
      group: "Integrations",
    },
  ],
  routes: [
    { path: "/", element: IntegrationsPage },
    // No nav entry: reached from a row of the list.
    { path: "/integrations/:key", element: IntegrationPage },
    { path: "/connections", element: ConnectionsPage },
    // A plugin can't read a query string, so a cloud filter arrives as a
    // path segment, from an integration's own page.
    { path: "/connections/cloud/:cloud", element: ConnectionsPage },
  ],
})

export default integrationsPlugin
