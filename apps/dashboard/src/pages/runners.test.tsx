import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { Runners } from "@/pages/runners"
import { json } from "@/test-fixtures/nodes"

const permission = vi.hoisted(() => ({ manage: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (name: string) => name !== "nodes:manage" || permission.manage,
    session: { userId: "u1", orgId: "org_one" },
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
afterEach(() => {
  permission.manage = true
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const wrap = (ui: React.ReactNode) =>
  render(<ClientProvider client={forgeClient}>{ui}</ClientProvider>)

const pool = {
  id: "rpool_1",
  org_id: "org_one",
  scope: "organization",
  name: "builds-amd64",
  connection_id: "conn_pool",
  mode: "self_hosted",
  architecture: "amd64",
  runtime_identity: "a".repeat(64),
  settings: {},
  revision: 2,
  created_at: "2026-11-23T09:00:00Z",
  updated_at: "2026-11-23T09:00:00Z",
  runners: [
    {
      node_id: "node_1",
      pool_id: "rpool_1",
      org_id: "org_one",
      name: "runner-1",
      status: "online",
      takes_builds: true,
      builds: 7,
      created_at: "2026-11-23T09:00:00Z",
    },
    {
      node_id: "node_2",
      pool_id: "rpool_1",
      org_id: "org_one",
      name: "runner-2",
      status: "online",
      takes_builds: false,
      reason: "the runner's Firecracker toolchain differs",
      builds: 0,
      created_at: "2026-11-23T09:00:00Z",
    },
  ],
}

function serve(calls: { method: string; url: string }[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input)
      const method = (
        init?.method ?? (input instanceof Request ? input.method : "GET")
      ).toUpperCase()
      calls.push({ method, url })
      if (url.endsWith("/join-tokens")) {
        return json(
          {
            token: "lpj_live_x",
            expires_at: "2026-11-23T10:00:00Z",
            command:
              "sudo LOOMPLANE_JOIN_TOKEN=lpj_live_x loomplane runner start --url https://lp.example.com",
          },
          201
        )
      }
      return json({ pools: [pool] })
    })
  )
}

it("shows each pool, the toolchain it pins and why a runner takes no builds", async () => {
  serve([])
  wrap(<Runners />)
  expect(await screen.findByText("builds-amd64")).toBeInTheDocument()
  expect(screen.getByText("Ready")).toBeInTheDocument()
  expect(screen.getByText(/build\.builder: conn_pool/)).toBeInTheDocument()
  expect(screen.getByText("Takes builds in microVMs")).toBeInTheDocument()
  expect(
    screen.getByText("the runner's Firecracker toolchain differs")
  ).toBeInTheDocument()
})

it("makes a runner's join command", async () => {
  const calls: { method: string; url: string }[] = []
  serve(calls)
  wrap(<Runners />)
  await userEvent.click(
    await screen.findByRole("button", { name: /add runner/i })
  )
  await userEvent.click(
    screen.getByRole("button", { name: /make a join token/i })
  )
  expect(
    await screen.findByText(/loomplane runner start --url/)
  ).toBeInTheDocument()
  expect(
    calls.some(
      (c) =>
        c.method === "POST" &&
        c.url.endsWith("/api/v1/runner-pools/rpool_1/join-tokens")
    )
  ).toBe(true)
})

it("hides managing pools from those who can't", async () => {
  permission.manage = false
  serve([])
  wrap(<Runners />)
  await screen.findByText("builds-amd64")
  expect(screen.queryByRole("button", { name: /new pool/i })).toBeNull()
  expect(screen.queryByRole("button", { name: /add runner/i })).toBeNull()
})

it("creates an on-demand default pool", async () => {
  const bodies: unknown[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = (
        init?.method ?? (input instanceof Request ? input.method : "GET")
      ).toUpperCase()
      if (method === "POST") {
        const raw =
          input instanceof Request
            ? await input.text()
            : String(init?.body ?? "")
        bodies.push(JSON.parse(raw))
        return json({ ...pool, runners: [] }, 201)
      }
      return json({ pools: [] })
    })
  )
  wrap(<Runners />)
  await userEvent.click(
    await screen.findByRole("button", { name: /new pool/i })
  )
  await userEvent.type(screen.getByLabelText("Name"), "burst")
  await userEvent.click(screen.getByLabelText(/^Default/))
  await userEvent.click(screen.getByLabelText(/^On demand/))
  await userEvent.type(screen.getByLabelText("Cloud connection"), "conn_do")
  await userEvent.type(screen.getByLabelText("Region"), "nyc3")
  await userEvent.type(
    screen.getByLabelText("Sizes, smallest first"),
    "s-2vcpu-4gb-amd"
  )
  await userEvent.type(
    screen.getByLabelText("Toolchain bundle URL"),
    "https://x.example/t.tgz"
  )
  await userEvent.type(
    screen.getByLabelText("Toolchain bundle sha256"),
    "b".repeat(64)
  )
  await userEvent.click(screen.getByRole("button", { name: /create pool/i }))
  await vi.waitFor(() => expect(bodies).toHaveLength(1))
  const body = bodies[0] as { mode: string; settings: Record<string, unknown> }
  expect(body.mode).toBe("on_demand")
  expect(body.settings).toMatchObject({
    default: true,
    provider: "digitalocean",
    cloud_connection_id: "conn_do",
    region: "nyc3",
    sizes: ["s-2vcpu-4gb-amd"],
    max_runners: 2,
    toolchain_sha256: "b".repeat(64),
  })
})

it("makes a pool the default from its card", async () => {
  const sent: { method: string; url: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = input instanceof Request ? input : undefined
      const method = (init?.method ?? req?.method ?? "GET").toUpperCase()
      const url = String(req ? req.url : input)
      if (method === "PATCH") {
        const raw = req ? await req.text() : String(init?.body ?? "")
        sent.push({ method, url, body: JSON.parse(raw) })
        return json({ ...pool, settings: { default: true } })
      }
      return json({ pools: [pool] })
    })
  )
  wrap(<Runners />)
  await userEvent.click(
    await screen.findByRole("button", { name: /edit pool builds-amd64/i })
  )
  await userEvent.click(screen.getByLabelText(/^Default for services/))
  await userEvent.click(screen.getByRole("button", { name: /^save$/i }))
  await vi.waitFor(() => expect(sent).toHaveLength(1))
  expect(sent[0].url).toMatch(/\/api\/v1\/runner-pools\/rpool_1$/)
  expect(sent[0].body).toMatchObject({ default: true })
})

it("shows global runners to platform admins only", async () => {
  const { setInstance } = await import("@/lib/instance")
  const base = {
    setupPending: false,
    waitlist: false,
    setupTokenRequired: false,
  }
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input)
      if (url.includes("/platform/runner-pools")) {
        return json({
          pools: [
            {
              ...pool,
              id: "rpool_g",
              name: "global-amd64",
              scope: "platform",
              connection_id: "",
            },
          ],
        })
      }
      return json({ pools: [] })
    })
  )
  setInstance({ ...base, platformAdmin: false })
  const first = wrap(<Runners />)
  await screen.findByText("No runner pools yet")
  expect(screen.queryByText("Global runners")).toBeNull()
  first.unmount()
  forgeClient.clear()
  setInstance({ ...base, platformAdmin: true })
  wrap(<Runners />)
  expect(await screen.findByText("global-amd64")).toBeInTheDocument()
  expect(screen.getByText("Every enabled workspace")).toBeInTheDocument()
  setInstance(base)
})
