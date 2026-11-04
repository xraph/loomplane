import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"
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
  usePath: () => "/agent/requests",
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
  }) => select({ location: { pathname: "/agent/requests", search: {} } }),
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

const pending = (id: string) => ({ id, status: "pending", kind: "answer" })

function serve(enabled: boolean) {
  const paths: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    paths.push(path)
    if (path === "/api/v1/agent/status")
      return Response.json({ server: true, enabled, knowledge: false })
    if (path === "/api/v1/agent/requests")
      return Response.json({
        requests: [
          pending("areq_1"),
          pending("areq_2"),
          { ...pending("areq_3"), status: "answered" },
        ],
      })
    if (path === "/api/v1/agent/reports")
      return Response.json({
        reports: [
          { id: "arep_1", status: "open" },
          { id: "arep_2", status: "open" },
          { id: "arep_3", status: "open" },
        ],
      })
    return Response.json({ projects: [], members: [] })
  })
  return paths
}

function show(ai: boolean) {
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

it("shows the pending count under Deployments when the agent is on", async () => {
  serve(true)
  show(true)
  const link = await screen.findByRole("link", { name: /^Agent/ })
  expect(link).toHaveAttribute("href", "/agent/requests")
  await waitFor(() => expect(link).toHaveTextContent("2"))
  // After "Needs approval".
  const order = screen
    .getAllByRole("link")
    .map((a) => a.textContent?.trim().replace(/\d+$/, "").trim())
    .filter((t) => ["Needs approval", "Agent", "Failed"].includes(t ?? ""))
  expect(order).toEqual(["Needs approval", "Agent", "Failed"])
})

it("has no Agent item, and asks for no requests, when the agent is off", async () => {
  const paths = serve(false)
  show(true)
  await waitFor(() => expect(paths).toContain("/api/v1/agent/status"))
  expect(screen.queryByRole("link", { name: /^Agent/ })).toBeNull()
  expect(paths).not.toContain("/api/v1/agent/requests")
})

it("shows no AI at all, and asks nothing of the API, when the server has AI off", async () => {
  const paths = serve(true)
  show(false)
  await screen.findByRole("link", { name: "Overview" })
  expect(screen.queryByRole("link", { name: /^Agent/ })).toBeNull()
  expect(screen.queryByRole("link", { name: "AI" })).toBeNull()
  expect(screen.queryByRole("link", { name: "Models" })).toBeNull()
  expect(
    paths.filter((p) => p.includes("/agent/") || p.includes("/ai/"))
  ).toEqual([])
})

it("shows the open report count under Agent only when the agent is on", async () => {
  serve(true)
  show(true)
  const link = await screen.findByRole("link", { name: /^Reports/ })
  expect(link).toHaveAttribute("href", "/agent/reports")
  await waitFor(() => expect(link).toHaveTextContent("3"))
  cleanup()
  forgeClient.clear()
  const paths = serve(false)
  show(true)
  await waitFor(() => expect(paths).toContain("/api/v1/agent/status"))
  expect(screen.queryByRole("link", { name: /^Reports/ })).toBeNull()
  expect(paths).not.toContain("/api/v1/agent/reports")
})

it("asks for no reports when the server has AI off", async () => {
  const paths = serve(true)
  show(false)
  await screen.findByRole("link", { name: "Overview" })
  expect(screen.queryByRole("link", { name: /^Reports/ })).toBeNull()
  expect(paths.filter((p) => p.includes("/agent/reports"))).toEqual([])
})
