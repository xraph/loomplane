import { AuthProvider } from "@authsome/ui-react"
import { ClientProvider } from "@forge-go/client-react"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import "./index.css"
import { RouterProvider } from "@tanstack/react-router"
import { Problem } from "./App.tsx"
import { router } from "./router"
// A query binding, not a React hook: renamed so nothing mistakes it for one.
import { usePublicConfigGet as publicConfig } from "@/client/forge/loomplane/hooks"
import { forgeClient, setAuthsomeApp } from "@/client/runtime"
import { SessionRefreshBridge } from "@/client/session-refresh-bridge"
import { authStorage, forgetStoredSession } from "@/client/auth-storage"
import { applyAccent } from "@/lib/accent"
import { FeaturesProvider, setFeatures } from "@/lib/features"
import { setInstance } from "@/lib/instance"
import { isUnder } from "@/console/path"
import { captureInvite } from "@/lib/invite"
import { ThemeProvider } from "@/components/theme-provider.tsx"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"

// The primary color, before the first paint.
applyAccent()

const root = createRoot(document.getElementById("root")!)

// Before anything navigates: signing in leaves the invitation's URL behind.
captureInvite()
forgetStoredSession()

// authsome's provider needs the app's publishable key before it mounts, and
// it differs per deployment, so it's read from Loomplane rather than built
// into the dashboard.
try {
  const config = await publicConfig().fetch()
  setAuthsomeApp(config)
  // Once, before the router's first load runs the routes' feature checks.
  setFeatures(config.features)
  setInstance({
    setupPending: config.setupPending,
    waitlist: config.waitlist,
    setupTokenRequired: config.setupTokenRequired,
    consolePath: config.consolePath,
  })
  // Only a platform admin is told the console's path, and the console is its
  // own app: its bundle loads here and nowhere else.
  if (
    config.consolePath &&
    isUnder(window.location.pathname, config.consolePath)
  ) {
    const { default: Console } = await import("./console/console")
    root.render(
      <StrictMode>
        <ThemeProvider>
          <Console basePath={config.consolePath} />
        </ThemeProvider>
      </StrictMode>
    )
  } else {
    root.render(
      <StrictMode>
        <ThemeProvider>
          <TooltipProvider>
            <AuthProvider
              baseURL={`${window.location.origin}${config.authBasePath}`}
              publishableKey={config.publishableKey}
              session="cookie"
              storage={authStorage}
            >
              <ClientProvider client={forgeClient}>
                <SessionRefreshBridge />
                <FeaturesProvider features={config.features}>
                  <RouterProvider router={router} />
                </FeaturesProvider>
              </ClientProvider>
            </AuthProvider>
            <Toaster />
          </TooltipProvider>
        </ThemeProvider>
      </StrictMode>
    )
  }
} catch {
  root.render(
    <Problem
      title="Loomplane isn't answering"
      message="The dashboard couldn't reach Loomplane's API. If it's restarting, try again in a moment. Otherwise check that it's running."
      address={`${window.location.host}/api`}
    />
  )
}
