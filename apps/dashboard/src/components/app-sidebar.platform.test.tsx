import userEvent from "@testing-library/user-event"
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
  Link: ({
    children,
    to,
    ...rest
  }: React.ComponentProps<"a"> & { children?: ReactNode; to: string }) => (
    <a {...rest} href={to}>
      {children}
    </a>
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
  useAgentDock: () => null,
}))
vi.mock("@/lib/use-runs", () => ({ useRuns: () => ({ runs: [] }) }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function show(
  platformAdmin: boolean,
  platformOwner = platformAdmin,
  ai = true
) {
  setInstance({
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
    platformAdmin,
    platformOwner,
  })
  vi.stubGlobal("fetch", async () =>
    Response.json({ projects: [], members: [] })
  )
  render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai }}>
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
  await userEvent.click(screen.getByText("Settings"))
  const link = await screen.findByRole("menuitem", { name: "Cloud sign-in" })
  expect(link).toHaveAttribute("href", "/platform/cloud-sign-in")
})

it("hides Cloud sign-in from everyone else", async () => {
  show(false)
  await userEvent.click(screen.getByText("Settings"))
  await screen.findByRole("menuitem", { name: "Integrations" })
  expect(screen.queryByRole("menuitem", { name: "Cloud sign-in" })).toBeNull()
})

it("shows Platform Cloud to a platform admin", async () => {
  show(true)
  await userEvent.click(screen.getByText("Settings"))
  const link = await screen.findByRole("menuitem", { name: "Platform Cloud" })
  expect(link).toHaveAttribute("href", "/platform/cloud")
})

it("hides Platform Cloud from everyone else", async () => {
  show(false)
  await userEvent.click(screen.getByText("Settings"))
  await screen.findByRole("menuitem", { name: "Integrations" })
  expect(screen.queryByRole("menuitem", { name: "Platform Cloud" })).toBeNull()
})

it("shows Flagged answers to a platform admin", async () => {
  show(true)
  await userEvent.click(screen.getByText("Settings"))
  const link = await screen.findByRole("menuitem", { name: "Flagged answers" })
  expect(link).toHaveAttribute("href", "/platform/flags")
})

it("hides Flagged answers from a platform admin who isn't the owner", async () => {
  show(true, false)
  await userEvent.click(screen.getByText("Settings"))
  await screen.findByRole("menuitem", { name: "Platform Cloud" })
  expect(screen.queryByRole("menuitem", { name: "Flagged answers" })).toBeNull()
})

it("hides Flagged answers from everyone else", async () => {
  show(false)
  await userEvent.click(screen.getByText("Settings"))
  await screen.findByRole("menuitem", { name: "Integrations" })
  expect(screen.queryByRole("menuitem", { name: "Flagged answers" })).toBeNull()
})

it("hides Flagged answers from the owner when the server's AI is off", async () => {
  show(true, true, false)
  await userEvent.click(screen.getByText("Settings"))
  await screen.findByRole("menuitem", { name: "Platform Cloud" })
  expect(screen.queryByRole("menuitem", { name: "Flagged answers" })).toBeNull()
})
