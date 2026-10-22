import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { DatabaseGateways } from "@/components/database-gateway"

const permissions = vi.hoisted(() => ({ admin: true }))

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (p: string) => p !== "infrastructure:modify" || permissions.admin,
  }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  permissions.admin = true
})

const seen = new Date().toISOString()

// fixture serves one gateway. After a pause or resume the list reports
// serverSays when it's set, as when someone else changed the route since.
function fixture({
  empty = false,
  serverSays,
}: { empty?: boolean; serverSays?: string } = {}) {
  const calls: string[] = []
  let listed = "active"
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = init?.method ?? "GET"
      calls.push(`${method} ${new URL(url, "http://x").pathname}`)
      if (method === "POST") {
        const state = url.endsWith("/pause") ? "paused" : "active"
        listed = serverSays ?? state
        return Response.json({
          id: "dbgr_1",
          gateway_id: "dbgw_1",
          resource: "orders",
          state,
        })
      }
      const state = listed
      return Response.json({
        gateways: empty
          ? []
          : [
              {
                id: "dbgw_1",
                environment_id: "env_1",
                name: "main",
                provider: "cloud-run",
                region: "us-central1",
                snapshot_version: 4,
                routes: [
                  {
                    id: "dbgr_1",
                    resource: "orders",
                    state,
                    reported: true,
                    reachable: true,
                    backend_conns: 7,
                    max_connections: 100,
                    pin_rate: 0.25,
                  },
                ],
                instances: [
                  {
                    id: "inst-current",
                    snapshot_version: 4,
                    last_seen_at: seen,
                    draining: false,
                    reporting: true,
                    behind: false,
                    routes: [],
                  },
                  {
                    id: "inst-old",
                    snapshot_version: 3,
                    last_seen_at: seen,
                    draining: false,
                    reporting: true,
                    behind: true,
                    routes: [],
                  },
                  {
                    id: "inst-quiet",
                    snapshot_version: 4,
                    last_seen_at: "2026-10-22T11:20:00Z",
                    draining: false,
                    reporting: false,
                    behind: false,
                    routes: [],
                  },
                ],
              },
            ],
      })
    }
  )
  return calls
}

function show() {
  return render(
    <ClientProvider client={forgeClient}>
      <DatabaseGateways project="proj_1" />
    </ClientProvider>
  )
}

it("marks a stale instance not reporting and an old one behind", async () => {
  fixture()
  const { container } = show()
  const quiet = (await screen.findByText("inst-quiet")).closest("li")!
  expect(within(quiet).getByText("Not reporting")).toBeInTheDocument()
  expect(within(quiet).queryByText("Behind")).not.toBeInTheDocument()
  const old = screen.getByText("inst-old").closest("li")!
  expect(within(old).getByText("Behind")).toBeInTheDocument()
  expect(within(old).queryByText("Not reporting")).not.toBeInTheDocument()
  const current = screen.getByText("inst-current").closest("li")!
  expect(within(current).queryByText(/Behind|Not reporting/)).toBeNull()
  expect(screen.getByText("7 of 100")).toBeInTheDocument()
  expect(container.textContent).not.toContain("—")
})

it("pauses a route through dbGateways.pauseRoute and shows it paused", async () => {
  const calls = fixture()
  const { container } = show()
  await userEvent.click(await screen.findByRole("button", { name: "Pause" }))
  expect(calls).toContain(
    "POST /api/v1/projects/proj_1/db-gateways/dbgw_1/routes/dbgr_1/pause"
  )
  expect(await screen.findByText("Paused")).toBeInTheDocument()
  await userEvent.click(await screen.findByRole("button", { name: "Resume" }))
  expect(calls).toContain(
    "POST /api/v1/projects/proj_1/db-gateways/dbgw_1/routes/dbgr_1/resume"
  )
  expect(await screen.findByRole("button", { name: "Pause" })).toBeVisible()
  expect(container.textContent).not.toContain("—")
})

it("shows the server's state after a pause, not the one it asked for", async () => {
  const calls = fixture({ serverSays: "active" })
  show()
  await userEvent.click(await screen.findByRole("button", { name: "Pause" }))
  expect(calls).toContain(
    "POST /api/v1/projects/proj_1/db-gateways/dbgw_1/routes/dbgr_1/pause"
  )
  // The pause answered paused, but the list read afterwards says active.
  await vi.waitFor(() =>
    expect(
      calls.filter((c) => c === "GET /api/v1/projects/proj_1/db-gateways")
        .length
    ).toBeGreaterThan(1)
  )
  expect(
    await screen.findByRole("button", { name: "Pause" })
  ).not.toBeDisabled()
  expect(screen.getByText("Active")).toBeInTheDocument()
  expect(screen.queryByText("Paused")).toBeNull()
})

it("offers pause only to admins", async () => {
  permissions.admin = false
  fixture()
  show()
  await screen.findByText("orders")
  expect(screen.queryByRole("button", { name: "Pause" })).toBeNull()
})

it("shows nothing for a project without a gateway", async () => {
  const calls = fixture({ empty: true })
  const { container } = show()
  await vi.waitFor(() => expect(calls.length).toBeGreaterThan(0))
  await new Promise((resolve) => setTimeout(resolve, 20))
  expect(container.textContent).toBe("")
})
