import { ClientProvider } from "@forge-go/client-react"
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AgentProvider, FeaturesProvider } from "@/lib/features"
import { createDashboardRouter } from "@/router"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org", userId: "usr_1" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: () => true,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
const shell = vi.hoisted(() => ({
  openNewProject: () => {},
  openShortcuts: () => {},
  openAssistant: vi.fn(),
}))
vi.mock("@/components/shell", () => ({
  useShell: () => shell,
  useAgentDock: () => null,
}))
vi.mock("@/lib/use-runs", async (original) => ({
  ...(await original<typeof import("@/lib/use-runs")>()),
  useRuns: () => ({ runs: [] }),
}))
vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: null, signOut: async () => {} }),
}))
// The routes and their pages, beside the sidebar; not the rest of the shell.
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  const { AppSidebar } = await import("@/components/app-sidebar")
  const { SidebarProvider } = await import("@/components/ui/sidebar")
  return {
    default: () => (
      <SidebarProvider>
        <AppSidebar />
        <main>
          <Outlet />
        </main>
      </SidebarProvider>
    ),
    Problem: () => null,
  }
})
vi.mock("@/pages/overview", () => ({ Overview: () => <h1>Overview</h1> }))

type Write = { method: string; path: string; body: unknown }
type Route = (body: unknown) => Response

let writes: Write[] = []
let reads: string[] = []

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  forgeClient.clear()
  writes = []
  reads = []
})

function serve(agentOn: boolean, routes: Record<string, Route> = {}) {
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method === "GET") reads.push(url.pathname)
      else writes.push({ method, path: url.pathname, body })
      const route = routes[`${method} ${url.pathname}`]
      if (route) return route(body)
      if (url.pathname === "/api/v1/agent/status")
        return Response.json({
          server: true,
          enabled: agentOn,
          knowledge: false,
        })
      if (url.pathname === "/api/v1/agent/conversations")
        return Response.json({ conversations: [] })
      if (url.pathname === "/api/v1/agent/requests")
        return Response.json({ requests: [] })
      return Response.json({ projects: [], members: [], installations: [] })
    }
  )
}

function open(path: string, ai = true) {
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: [path] })
  )
  render(
    <ClientProvider client={forgeClient}>
      <FeaturesProvider features={{ ai }}>
        <AgentProvider>
          <RouterProvider router={router} />
        </AgentProvider>
      </FeaturesProvider>
    </ClientProvider>
  )
  return router
}

it("hides the assistant when the server runs no agent", async () => {
  serve(true)
  open("/assistant", false)
  // The first route to load pays for the page's modules.
  expect(
    await screen.findByText("Page not found", {}, { timeout: 15_000 })
  ).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Overview" })).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Assistant" })).toBeNull()
  expect(reads.filter((p) => p.includes("/agent/"))).toEqual([])
})

it("hides the assistant when the workspace's agent is off", async () => {
  serve(false)
  open("/assistant")
  expect(
    await screen.findByText("Page not found", {}, { timeout: 15_000 })
  ).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Assistant" })).toBeNull()
  expect(
    reads.filter((p) => p.startsWith("/api/v1/agent/conversations"))
  ).toEqual([])
})

it("opens the agent's panel at full width and leaves for the Overview", async () => {
  serve(true)
  const router = open("/assistant?c=conv_1")
  expect(
    await screen.findByRole(
      "heading",
      { name: "Overview" },
      { timeout: 15_000 }
    )
  ).toBeInTheDocument()
  expect(router.state.location.pathname).toBe("/")
  expect(shell.openAssistant).toHaveBeenCalledTimes(1)
  expect(shell.openAssistant).toHaveBeenCalledWith({
    conversationId: "conv_1",
    projectId: undefined,
    mode: "focus",
  })
})
