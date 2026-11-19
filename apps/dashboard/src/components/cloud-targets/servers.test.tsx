import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { wireNode } from "@/test-fixtures/nodes"
import { Servers } from "./servers"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const cloud = { id: "c", revision: 4, name: "Shared" }
const seen = new Date(2026, 10, 1, 14, 2).toISOString()

const health = {
  checked_at: seen,
  effective_cpu_millis: 8000,
  effective_memory_bytes: 32 * 2 ** 30,
}

function fixture(nodeStatus = "online", extra: object[] = []) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("/usage"))
      return Response.json({ workloads: [], retained_storage: [] })
    if (path.endsWith("/servers"))
      return Response.json({
        servers: [
          {
            id: "s_node",
            name: "home-box",
            status: "ready",
            ownership: "adopted",
            connection_id: "conn_node",
            health,
          },
          {
            id: "s_ssh",
            name: "rack-1",
            status: "ready",
            ownership: "adopted",
            connection_id: "conn_ssh",
            health,
          },
          ...extra,
        ],
      })
    if (path.endsWith("/connections"))
      return Response.json({
        connections: [
          {
            id: "conn_node",
            name: "home-box",
            cloud: "server",
            method: "node",
            status: "ready",
            details: { node_id: "n_one" },
          },
          {
            id: "conn_ssh",
            name: "rack-1",
            cloud: "server",
            method: "ssh",
            status: "ready",
            details: {},
          },
        ],
      })
    if (path === "/api/v1/nodes")
      return Response.json({
        nodes: [
          wireNode({
            name: "home-box",
            status: nodeStatus,
            last_seen_at: seen,
          }),
        ],
      })
    if (path.endsWith("/providers")) return Response.json({ providers: [] })
    if (path.endsWith("/pools")) return Response.json({ pools: [] })
    return Response.json({})
  })
}

const wrap = (ui: React.ReactNode) =>
  render(<ClientProvider client={forgeClient}>{ui}</ClientProvider>)

it("marks a server that runs on a node, and says its owner connected it", async () => {
  fixture()
  wrap(<Servers cloud={cloud as never} />)
  const row = (await screen.findByText("home-box")).closest("tr, [role=row]")!
  expect(within(row as HTMLElement).getByText("Node")).toBeInTheDocument()
  expect(
    within(row as HTMLElement).getByText(/Connected by its owner/)
  ).toBeInTheDocument()
  // An SSH server keeps its own wording and has no badge.
  const ssh = screen.getByText("rack-1").closest("tr, [role=row]")!
  expect(within(ssh as HTMLElement).queryByText("Node")).toBeNull()
  expect(
    within(ssh as HTMLElement).getByText(/Connected over SSH/)
  ).toBeInTheDocument()
})

it("says when the node went offline", async () => {
  fixture("offline")
  wrap(<Servers cloud={cloud as never} />)
  expect(await screen.findByText("Offline since 14:02")).toBeInTheDocument()
})

it("never offers to delete a server that belongs to a node", async () => {
  fixture()
  const user = userEvent.setup()
  wrap(<Servers cloud={cloud as never} />)
  await screen.findByText("home-box")
  await user.click(
    screen.getByRole("button", { name: "More actions for home-box" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Review disconnect" })
  ).toBeInTheDocument()
  expect(screen.queryByText(/Review deletion/)).toBeNull()
  expect(screen.queryByText(/Delete/)).toBeNull()
})

const failed = {
  id: "s_failed",
  name: "droplet-1",
  status: "failed",
  message:
    "The server was created but never finished enrolling: Loomplane couldn't confirm it over SSH on port 22 within 45 minutes.",
  ownership: "managed",
  provider: "digitalocean",
  connection_id: "conn_ssh",
  resource_id: "998877",
  health: {},
}

it("says why a server failed to start, and offers no health check", async () => {
  fixture("online", [failed])
  const user = userEvent.setup()
  wrap(<Servers cloud={cloud as never} />)
  const row = (await screen.findByText("droplet-1")).closest("tr, [role=row]")!
  const pill = within(row as HTMLElement).getByText("Failed")
  expect(pill.closest("[title]")).toHaveAttribute("title", failed.message)

  await user.click(
    screen.getByRole("button", { name: "More actions for droplet-1" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Review drain" })
  ).toBeInTheDocument()
  expect(screen.queryByRole("menuitem", { name: "Check health" })).toBeNull()
  await user.keyboard("{Escape}")

  await user.click(screen.getByRole("button", { name: "Open droplet-1" }))
  const dialog = await screen.findByRole("dialog")
  expect(
    within(dialog).getByText("This server failed to start")
  ).toBeInTheDocument()
  expect(within(dialog).getByText(failed.message)).toBeInTheDocument()
  expect(
    within(dialog).queryByRole("button", { name: "Check health" })
  ).toBeNull()
})

it("still offers a health check on a ready server", async () => {
  fixture()
  const user = userEvent.setup()
  wrap(<Servers cloud={cloud as never} />)
  await screen.findByText("rack-1")
  await user.click(
    screen.getByRole("button", { name: "More actions for rack-1" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "Check health" })
  ).toBeInTheDocument()
})
