import { ClientProvider } from "@forge-go/client-react"
import type { StreamConnection } from "@forge-go/client-core"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AddNode } from "@/components/nodes/add-node"
import { LoomplaneRealtime } from "@/lib/realtime"
import { serve, wireNode, type WireNode } from "@/test-fixtures/nodes"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: () => true,
    session: { userId: "u1", orgId: "org_one" },
  }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const command =
  "curl -fsSL https://loomplane.example.com/install/node.sh | sh -s -- --token lpj_live_8f2c"
const expires = new Date(2026, 10, 2, 15, 42).toISOString()

class Connection implements StreamConnection {
  message: (frame: unknown) => void = () => {}
  onMessage(handler: (frame: unknown) => void) {
    this.message = handler
  }
  onClose() {}
  send() {}
  close() {}
}

function mount() {
  return render(
    <ClientProvider client={forgeClient}>
      <AddNode open onOpenChange={() => {}} />
    </ClientProvider>
  )
}

const state = (nodes: WireNode[] = []) => ({
  nodes,
  token: { token: "lpj_live_8f2c", expires_at: expires, command },
})

async function makeToken(user: ReturnType<typeof userEvent.setup>) {
  const button = await screen.findByRole("button", { name: "Make the token" })
  await vi.waitFor(() => expect(button).toBeEnabled())
  await user.click(button)
}

it("shows the command the server made, and copies it exactly", async () => {
  const calls = serve(state())
  const user = userEvent.setup()
  mount()
  await user.type(await screen.findByLabelText("Labels"), "region=home")
  await makeToken(user)

  expect(await screen.findByText(command)).toBeInTheDocument()
  expect(screen.getByText(/expires at 15:42/)).toBeInTheDocument()
  expect(
    screen.getByText("Waiting for the machine to connect…")
  ).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Copy command" }))
  expect(await navigator.clipboard.readText()).toBe(command)

  const mint = calls.find((c) => c.url.endsWith("/nodes/join-tokens"))
  expect(mint?.body).toMatchObject({
    labels: { region: "home" },
    endpoints: ["docker", "probe"],
  })
})

async function connect(over: Record<string, unknown>) {
  const nodes: WireNode[] = [wireNode({ id: "n_old", name: "old-one" })]
  serve(state(nodes))
  const connection = new Connection()
  const live = new LoomplaneRealtime({
    cache: forgeClient,
    orgId: "org_one",
    connect: () => connection,
  })
  live.start()
  const user = userEvent.setup()
  mount()
  await makeToken(user)
  await screen.findByText(command)
  // The machine runs the command: a new node is stored, and the stream says so.
  const joined = wireNode({ id: "n_new", name: "home-nuc", ...over })
  nodes.push(joined)
  connection.message({
    event: "node.updated",
    cursor: "org_one:7",
    data: {
      id: "n_new",
      name: "home-nuc",
      status: "online",
      version: "v1.9.0",
      endpoints: ["docker", "probe"],
      ingress_mode: "direct",
      labels: {},
      role: "node",
      identity_generation: 1,
    },
  })
  return live
}

it("flips to the inventory when a node.updated arrives for a new node", async () => {
  const live = await connect({})
  expect(await screen.findByText("home-nuc is online")).toBeInTheDocument()
  expect(screen.getByText("linux, 6.8, amd64")).toBeInTheDocument()
  expect(screen.getByText("8 CPU · 32 GiB")).toBeInTheDocument()
  expect(screen.getByText("Docker 27.3")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Open node" })).toHaveAttribute(
    "href",
    "/nodes/$id"
  )
  // A public address answered, so there is no router to explain.
  expect(screen.queryByText(/sits behind a router/)).not.toBeInTheDocument()
  live.stop()
})

it("explains the router only when no public address answered", async () => {
  const live = await connect({
    inventory: {
      os: "linux",
      kernel: "6.8",
      arch: "amd64",
      cpus: 12,
      memory_mib: 65536,
      disk_gib: 100,
    },
  })
  expect(await screen.findByText("home-nuc is online")).toBeInTheDocument()
  expect(screen.getByText(/sits behind a router/)).toBeInTheDocument()
  live.stop()
})

it("ignores a node that was already there", async () => {
  serve(state([wireNode({ id: "n_old", name: "old-one" })]))
  const user = userEvent.setup()
  mount()
  await makeToken(user)
  await screen.findByText(command)
  expect(screen.queryByText(/is online/)).not.toBeInTheDocument()
})
