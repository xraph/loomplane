import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { AppSidebar } from "@/components/app-sidebar"
import { ShellProvider, useShell } from "@/components/shell"
import { ThemeProvider } from "@/components/theme-provider"
import { SidebarProvider } from "@/components/ui/sidebar"
import { FeaturesProvider } from "@/lib/features"

const { navigate, access } = vi.hoisted(() => ({
  navigate: vi.fn(),
  access: { denied: [] as string[] },
}))
vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  usePath: () => "/",
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: (permission: string) => !access.denied.includes(permission),
  }),
}))
// What the sidebar reads besides the router and the organization.
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string; search: object } }) => unknown
  }) => select({ location: { pathname: "/", search: {} } }),
}))
vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: null, signOut: async () => {} }),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  navigate.mockReset()
  forgeClient.clear()
  access.denied = []
})

function stubEmpty() {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify({ projects: [], installations: [] }), {
        headers: { "Content-Type": "application/json" },
      })
  )
}

function Wrapped({ children }: { children: ReactNode }) {
  return (
    <ClientProvider client={forgeClient}>
      <ThemeProvider>
        <ShellProvider>{children}</ShellProvider>
      </ThemeProvider>
    </ClientProvider>
  )
}

function Entry() {
  const { openDeploy } = useShell()
  return (
    <button
      onClick={() => openDeploy({ project: "p", environment: "preview" })}
    >
      Deploy this project
    </button>
  )
}
it("opens the full deployment configuration with the project and environment preset", async () => {
  stubEmpty()
  render(
    <Wrapped>
      <Entry />
    </Wrapped>
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Deploy this project" })
  )
  expect(navigate).toHaveBeenCalledWith({
    to: "/deployments/new",
    search: { project: "p", environment: "preview" },
  })
})

function sidebar(ai: boolean) {
  stubEmpty()
  render(
    <Wrapped>
      <FeaturesProvider features={{ ai }}>
        <SidebarProvider>
          <AppSidebar />
        </SidebarProvider>
      </FeaturesProvider>
    </Wrapped>
  )
}

it("shows no AI group when AI is off", () => {
  sidebar(false)
  expect(screen.queryByText("AI")).toBeNull()
  expect(document.querySelector('a[href^="/ai/"]')).toBeNull()
})

it("shows no AI group without ai:read", () => {
  access.denied = ["ai:read", "ai:manage"]
  sidebar(true)
  expect(screen.queryByText("AI")).toBeNull()
  expect(document.querySelector('a[href^="/ai/"]')).toBeNull()
})

it("shows Settings under AI for members", () => {
  // A developer reads AI and manages none of it.
  access.denied = ["ai:manage"]
  sidebar(true)
  const label = screen.getByText("AI")
  const group = label.closest('[data-sidebar="group"]') as HTMLElement
  expect(group).not.toBeNull()
  const links = Array.from(group.querySelectorAll("a")).map((a) => [
    a.textContent?.trim(),
    a.getAttribute("href"),
  ])
  expect(links).toEqual([["Settings", "/ai/settings"]])
  // The workspace's own Settings stay in the main group.
  expect(screen.getAllByText("Settings")).toHaveLength(2)
})
