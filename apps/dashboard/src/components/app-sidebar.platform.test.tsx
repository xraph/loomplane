import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"
import { setInstance } from "@/lib/instance"
import { AgentProvider, FeaturesProvider } from "@/lib/features"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: () => true,
  }),
}))
vi.mock("@/lib/router", () => ({
  usePath: () => "/members",
  useNavigate: () => () => {},
  Link: ({ children, to }: { children?: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@tanstack/react-router", () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string; search: object } }) => unknown
  }) => select({ location: { pathname: "/members", search: {} } }),
}))
vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: null, signOut: async () => {} }),
}))
vi.mock("@/components/shell", () => ({
  useShell: () => ({ openNewProject: () => {}, openShortcuts: () => {} }),
}))
vi.mock("@/lib/use-runs", () => ({ useRuns: () => ({ runs: [] }) }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function show(platformAdmin: boolean) {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin,
  })
  vi.stubGlobal("fetch", async () =>
    Response.json({ projects: [], members: [] })
  )
  render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai: false }}>
        <AgentProvider>
          <SidebarProvider>
            <AppSidebar />
          </SidebarProvider>
        </AgentProvider>
      </FeaturesProvider>
    </ClientProvider>
  )
}

it("shows Cloud sign-in to a platform admin", async () => {
  show(true)
  const link = await screen.findByRole("link", { name: "Cloud sign-in" })
  expect(link).toHaveAttribute("href", "/platform/cloud-sign-in")
})

it("hides Cloud sign-in from everyone else", async () => {
  show(false)
  await screen.findByRole("link", { name: "Integrations" })
  expect(screen.queryByRole("link", { name: "Cloud sign-in" })).toBeNull()
})
