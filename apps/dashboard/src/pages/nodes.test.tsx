import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import type { StreamConnection } from "@forge-go/client-core"
import { LoomplaneRealtime } from "@/lib/realtime"
import { forgeClient } from "@/client/runtime"
import { Nodes } from "@/pages/nodes"
import { NodePage } from "@/pages/node"
import { serve, wireNode } from "@/test-fixtures/nodes"

const permission = vi.hoisted(() => ({ manage: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (name: string) => name !== "nodes:manage" || permission.manage,
    session: { userId: "u1", orgId: "org_one" },
  }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    params,
  }: {
    children: React.ReactNode
    to: string
    params?: { id: string }
  }) => <a href={to.replace("$id", params?.id ?? "")}>{children}</a>,
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
afterEach(() => {
  permission.manage = true
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const wrap = (ui: React.ReactNode) =>
  render(<ClientProvider client={forgeClient}>{ui}</ClientProvider>)

const seen = new Date(2026, 10, 1, 14, 2).toISOString()

it("lists each node with its state", async () => {
  serve({
    nodes: [
      wireNode({ name: "hetzner-1" }),
      wireNode({
        id: "n_two",
        name: "staging-2",
        status: "offline",
        last_seen_at: seen,
      }),
      wireNode({
        id: "n_three",
        name: "ci-runner-7",
        status: "pending",
        last_seen_at: null,
        version: "",
        inventory: {},
        endpoints: [],
      }),
      wireNode({ id: "n_four", name: "old-vps", status: "revoked" }),
    ],
  })
  wrap(<Nodes />)
  expect(await screen.findByText("hetzner-1")).toBeInTheDocument()
  expect(screen.getByText("Offline since 14:02")).toBeInTheDocument()
  expect(screen.getAllByText("Waiting to connect")).toHaveLength(2)
  expect(screen.getByText("Revoked", { selector: "span" })).toBeInTheDocument()
  expect(screen.getByText("Set when it connects")).toBeInTheDocument()
  const row = screen.getByText("hetzner-1").closest("tr")!
  expect(
    within(row).getByText("linux · amd64 · 8 CPU · 32 GiB")
  ).toBeInTheDocument()
  expect(within(row).getByText("docker")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "staging-2" })).toHaveAttribute(
    "href",
    "/nodes/n_two"
  )
})

it("offers Add node only to someone who can manage nodes", async () => {
  serve({ nodes: [wireNode()] })
  const { unmount } = wrap(<Nodes />)
  await screen.findByText("hetzner-1")
  expect(screen.getByRole("button", { name: "Add node" })).toBeInTheDocument()
  unmount()
  permission.manage = false
  wrap(<Nodes />)
  await screen.findByText("hetzner-1")
  expect(
    screen.queryByRole("button", { name: "Add node" })
  ).not.toBeInTheDocument()
})

it("shows an empty state with a way in", async () => {
  serve({ nodes: [] })
  wrap(<Nodes />)
  expect(await screen.findByText("No nodes yet")).toBeInTheDocument()
})

it("shows a node and narrows its endpoints without offering to add one", async () => {
  const calls = serve({ nodes: [wireNode({ labels: { region: "fsn1" } })] })
  const user = userEvent.setup()
  wrap(<NodePage id="n_one" />)
  expect(await screen.findByText("sha256:3f9c1e04")).toBeInTheDocument()
  expect(screen.getByText("region=fsn1")).toBeInTheDocument()
  expect(screen.getByText("Docker 27.3")).toBeInTheDocument()
  expect(screen.getByText("sudo loomctl node allow dial")).toBeInTheDocument()
  expect(screen.queryByText("Activity")).not.toBeInTheDocument()

  await user.click(screen.getByRole("button", { name: "Narrow endpoints" }))
  const dialog = await screen.findByRole("dialog")
  expect(within(dialog).getAllByRole("checkbox")).toHaveLength(2)
  await user.click(within(dialog).getByRole("checkbox", { name: "probe" }))
  await user.click(within(dialog).getByRole("button", { name: "Take away" }))
  await vi.waitFor(() =>
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      endpoints: ["docker"],
    })
  )
})

it("revokes a node only after a confirm", async () => {
  const calls = serve({ nodes: [wireNode()] })
  const user = userEvent.setup()
  wrap(<NodePage id="n_one" />)
  await user.click(await screen.findByRole("button", { name: "Revoke" }))
  expect(calls.some((c) => c.url.endsWith("/revoke"))).toBe(false)
  const dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Revoke" }))
  await vi.waitFor(() =>
    expect(calls.some((c) => c.url.endsWith("/n_one/revoke"))).toBe(true)
  )
})

it("hides the actions from someone who can only read", async () => {
  permission.manage = false
  serve({ nodes: [wireNode()] })
  wrap(<NodePage id="n_one" />)
  await screen.findByText("sha256:3f9c1e04")
  expect(
    screen.queryByRole("button", { name: "Rename" })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Revoke" })
  ).not.toBeInTheDocument()
})

it("shows the edge and macOS notes, and no traffic for a node still waiting", async () => {
  serve({
    nodes: [
      wireNode({ id: "e1", name: "edge-fra", role: "edge", edge_scope: "org" }),
      wireNode({
        id: "m1",
        name: "studio-mac",
        inventory: { os: "darwin", arch: "arm64", cpus: 8, memory_mib: 16384 },
      }),
      wireNode({ id: "p1", name: "ci-runner-7", status: "pending" }),
    ],
  })
  wrap(<Nodes />)
  await screen.findByText("edge-fra")
  expect(screen.getByText("Edge for this organization")).toBeInTheDocument()
  expect(screen.getByText("macOS, for development")).toBeInTheDocument()
  const pending = screen.getByText("ci-runner-7").closest("tr")!
  expect(within(pending).getByText("None")).toBeInTheDocument()
  expect(within(pending).queryByText("Direct")).not.toBeInTheDocument()
})

it("names the API key a node joined with, falling back to its id", async () => {
  serve({
    nodes: [wireNode({ joined_by_key_id: "key_ci" })],
    keys: [{ id: "key_ci", name: "ci-deploy", hint: "ab12", state: "active" }],
  })
  const { unmount } = wrap(<NodePage id="n_one" />)
  expect(await screen.findByText("ci-deploy")).toBeInTheDocument()
  unmount()
  forgeClient.clear()
  serve({ nodes: [wireNode({ joined_by_key_id: "key_gone" })], keys: [] })
  wrap(<NodePage id="n_one" />)
  expect(await screen.findByText("key_gone")).toBeInTheDocument()
})

it("keeps what the node page shows when a partial node.updated arrives", async () => {
  const seenAt = new Date(2026, 10, 1, 14, 2).toISOString()
  serve({
    nodes: [wireNode({ status: "online", last_seen_at: seenAt })],
  })
  let deliver: (frame: unknown) => void = () => {}
  const connection: StreamConnection = {
    onMessage: (fn) => {
      deliver = fn
    },
    onClose: () => {},
    send: () => {},
    close: () => {},
  }
  const live = new LoomplaneRealtime({
    cache: forgeClient,
    orgId: "org_one",
    connect: () => connection,
  })
  live.start()
  wrap(<NodePage id="n_one" />)
  expect(await screen.findByText("sha256:3f9c1e04")).toBeInTheDocument()
  // Exactly what the database trigger sends: no inventory, fingerprint or
  // last_seen_at.
  deliver({
    event: "node.updated",
    cursor: "org_one:9",
    data: {
      id: "n_one",
      name: "hetzner-1",
      status: "offline",
      version: "v1.9.0",
      endpoints: ["docker", "probe"],
      ingress_mode: "direct",
      labels: {},
      role: "node",
      identity_generation: 1,
    },
  })
  expect(await screen.findByText("Offline since 14:02")).toBeInTheDocument()
  expect(screen.getByText("sha256:3f9c1e04")).toBeInTheDocument()
  expect(screen.getByText("Docker 27.3")).toBeInTheDocument()
  expect(screen.getByText("203.0.113.24")).toBeInTheDocument()
  live.stop()
})
