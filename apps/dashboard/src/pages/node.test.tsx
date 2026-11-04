import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { NodePage } from "@/pages/node"
import { json, serve, wireNode, type WireNode } from "@/test-fixtures/nodes"

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
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const wrap = (ui: React.ReactNode) =>
  render(<ClientProvider client={forgeClient}>{ui}</ClientProvider>)

const workloads = [
  {
    binding_id: "b1",
    project_id: "shop",
    environment: "production",
    service: "web",
    kind: "service",
    has_backup_policy: false,
  },
  {
    binding_id: "b2",
    project_id: "shop",
    environment: "production",
    service: "orders-db",
    kind: "database",
    has_backup_policy: false,
  },
  {
    binding_id: "b3",
    project_id: "shop",
    environment: "production",
    service: "cache",
    kind: "database",
    has_backup_policy: true,
  },
]

/** mount serves the node page, its workloads and the drain review. */
function mount(node: WireNode = wireNode(), loads: unknown[] = workloads) {
  const calls = serve({ nodes: [node] })
  const base = globalThis.fetch
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = (init?.method ?? "GET").toUpperCase()
      if (path === `/api/v1/nodes/${node.id}/workloads`)
        return json({ workloads: loads })
      if (path === `/api/v1/nodes/${node.id}/drain` && method === "POST") {
        calls.push({ method, url: path })
        return json({
          server_id: "srv_one",
          server_revision: 3,
          reservations: 2,
          review: "review-1",
          usage: {
            workloads: [
              {
                project_id: "shop",
                environment_id: "production",
                service: "web",
                phase: "running",
              },
            ],
          },
        })
      }
      if (path === `/api/v1/nodes/${node.id}/drain/apply`) {
        calls.push({
          method,
          url: path,
          body: JSON.parse(String(init?.body ?? "{}")),
        })
        return json({ ...node, status: "draining" })
      }
      return base(input, init)
    }
  )
  return calls
}

it("lists what runs on the node and warns about a database with no backups", async () => {
  mount()
  wrap(<NodePage id="n_one" />)
  expect(await screen.findByText("orders-db")).toBeInTheDocument()
  expect(screen.getByText("web")).toBeInTheDocument()
  expect(
    screen.getAllByText(
      "This database has no backups. If this machine is lost, its data is lost."
    )
  ).toHaveLength(1)
})

it("says so when nothing runs on the node", async () => {
  mount(wireNode(), [])
  wrap(<NodePage id="n_one" />)
  expect(
    await screen.findByText(/Nothing runs on this node/)
  ).toBeInTheDocument()
})

it("reviews a drain before it applies it", async () => {
  const calls = mount()
  const user = userEvent.setup()
  wrap(<NodePage id="n_one" />)
  await user.click(await screen.findByRole("button", { name: "Drain" }))
  const dialog = await screen.findByRole("dialog")
  expect(await within(dialog).findByText(/2 reservations/)).toBeInTheDocument()
  expect(within(dialog).getByText("web")).toBeInTheDocument()
  expect(
    within(dialog).getByText(
      "This database has no backups. If this machine is lost, its data is lost."
    )
  ).toBeInTheDocument()
  expect(calls.some((c) => c.url.endsWith("/drain/apply"))).toBe(false)

  await user.click(
    within(dialog).getByRole("button", { name: "Apply reviewed drain" })
  )
  await vi.waitFor(() =>
    expect(calls.find((c) => c.url.endsWith("/drain/apply"))?.body).toEqual({
      review: "review-1",
    })
  )
})

it("shows how traffic reaches the node", async () => {
  mount(
    wireNode({
      ingress_mode: "direct",
      ingress_applied: "direct",
      public_address: "203.0.113.24",
      public_address_confirmed_at: new Date(2026, 10, 2, 14, 2).toISOString(),
    })
  )
  wrap(<NodePage id="n_one" />)
  expect(
    await screen.findByText(
      "Direct. 203.0.113.24 answered on port 80 at 14:02."
    )
  ).toBeInTheDocument()
})

it("shows why ingress isn't working when the node says so", async () => {
  mount(
    wireNode({
      ingress_mode: "direct",
      public_address: "203.0.113.24",
      ingress_detail: "203.0.113.24 did not answer on port 80",
    })
  )
  wrap(<NodePage id="n_one" />)
  expect(
    await screen.findByText("203.0.113.24 did not answer on port 80")
  ).toBeInTheDocument()
})
