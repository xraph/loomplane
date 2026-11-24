import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { WorkspaceService } from "@/pages/workspace-service"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    params,
  }: {
    children: React.ReactNode
    to: string
    params?: Record<string, string>
  }) => (
    <a href={to.replace(/\$(\w+)/g, (_, k: string) => params?.[k] ?? k)}>
      {children}
    </a>
  ),
  useNavigate: () => () => undefined,
}))
vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const endpoint = (over: Record<string, unknown> = {}) => ({
  id: "endpoint_orders",
  org_id: "org",
  name: "orders",
  engine: "postgres",
  host: "db.example.com",
  port: 5432,
  database: "orders",
  username: "app",
  tls: "require",
  created_by: "u",
  created_at: "2026-11-01T00:00:00Z",
  revision: 2,
  origin: "registered",
  description: "Orders database",
  environment_label: "production",
  restricted: false,
  projects: [],
  admin_set: false,
  last_check: {
    at: "2026-11-23T09:00:00Z",
    ok: true,
    version: "16.4",
    target: "conn_box",
    capabilities: { create_database: true, create_user: false },
  },
  ...over,
})

type Write = { method: string; path: string; body: unknown }

let ownUsers: unknown[] = []
afterEach(() => {
  ownUsers = []
})

function serve(
  over: Record<string, unknown> = {},
  uses: unknown[] = [],
  answer?: (w: Write) => Response | undefined
) {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      if (method !== "GET") {
        const w = {
          method,
          path,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        }
        writes.push(w)
        return answer?.(w) ?? Response.json(endpoint(over))
      }
      if (path === "/api/v1/catalog/endpoints/endpoint_orders")
        return Response.json(endpoint(over))
      if (path === "/api/v1/catalog/endpoints/endpoint_orders/uses")
        return Response.json({ uses })
      if (path === "/api/v1/catalog/endpoints/endpoint_orders/users")
        return Response.json({ users: ownUsers })
      if (path.endsWith("/connections"))
        return Response.json({
          connections: [
            {
              id: "conn_box",
              name: "hetzner-box",
              cloud: "server",
              status: "ready",
              details: { architecture: "amd64" },
              account: "",
              created_at: "",
              created_by: "",
              generation: 1,
              message: "",
              method: "token",
              missing: [],
              org_id: "org",
            },
          ],
        })
      if (path.endsWith("/projects"))
        return Response.json({
          projects: [
            { id: "project_shop", name: "shop", kind: "app" },
            { id: "project_billing", name: "billing", kind: "app" },
          ],
        })
      return Response.json({})
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <WorkspaceService id="endpoint_orders" />
    </ClientProvider>
  )
  return writes
}

test("the overview says where it is and what its last check found", async () => {
  serve({}, [
    {
      project_id: "project_shop",
      project: "shop",
      environment_id: "env_p",
      environment: "production",
      resource: "orders",
    },
  ])
  expect(
    await screen.findByRole("heading", { level: 1, name: "orders" })
  ).toBeInTheDocument()
  expect(screen.getByText("Checks out")).toBeInTheDocument()
  const connection = screen.getByRole("region", { name: "Connection" })
  expect(connection).toHaveTextContent("db.example.com:5432")
  expect(connection).toHaveTextContent("Replaced 1 time")
  const check = screen.getByRole("region", { name: "Last check" })
  expect(await within(check).findByText(/from hetzner-box/)).toBeInTheDocument()
  expect(check).toHaveTextContent(
    "These credentials can create databases, but not users."
  )

  await userEvent.click(screen.getByRole("tab", { name: /Used by/ }))
  const used = await screen.findByRole("region", { name: "Used by" })
  expect(within(used).getByRole("link", { name: "shop" })).toHaveAttribute(
    "href",
    "/projects/project_shop"
  )
  expect(used).toHaveTextContent("orders in production")
})

test("sharing narrows to the projects picked", async () => {
  const writes = serve()
  const user = userEvent.setup()
  await screen.findByRole("heading", { level: 1, name: "orders" })
  await user.click(screen.getByRole("tab", { name: "Access" }))
  const sharing = screen.getByRole("region", { name: "Sharing" })
  const save = within(sharing).getByRole("button", { name: "Save sharing" })
  expect(save).toBeDisabled()
  await user.click(
    within(sharing).getByRole("radio", { name: "Only some projects" })
  )
  await user.click(
    await within(sharing).findByRole("checkbox", { name: "billing" })
  )
  await user.click(save)
  await waitFor(() =>
    expect(writes).toContainEqual({
      method: "PUT",
      path: "/api/v1/catalog/endpoints/endpoint_orders/sharing",
      body: { restricted: true, projects: ["project_billing"] },
    })
  )
})

test("replacing credentials sends the revision that was read", async () => {
  let stale = true
  const writes = serve({}, [], (w) =>
    w.path.endsWith("/credential") && stale
      ? Response.json(
          { message: "the endpoint changed since it was read; read it again" },
          { status: 409 }
        )
      : undefined
  )
  const user = userEvent.setup()
  await screen.findByRole("heading", { level: 1, name: "orders" })
  await user.click(screen.getByRole("tab", { name: "Settings" }))
  const replace = screen.getByRole("region", { name: "Replace credentials" })
  const next =
    "postgres://app:rotated@db.example.com:5432/orders?sslmode=require"
  await user.type(within(replace).getByLabelText("New connection string"), next)
  const button = within(replace).getByRole("button", {
    name: "Check and replace",
  })
  await waitFor(() => expect(button).toBeEnabled())
  await user.click(button)
  expect(
    await within(replace).findByText(
      /Someone changed it since this page loaded/
    )
  ).toBeInTheDocument()
  expect(writes[0]).toEqual({
    method: "PUT",
    path: "/api/v1/catalog/endpoints/endpoint_orders/credential",
    body: { dsn: next, target: "conn_box", revision: 2 },
  })
  stale = false
  await user.click(button)
  await waitFor(() =>
    expect(within(replace).getByLabelText("New connection string")).toHaveValue(
      ""
    )
  )
})

test("a service in use can't be deleted", async () => {
  serve({}, [
    {
      project_id: "project_shop",
      project: "shop",
      environment_id: "env_p",
      environment: "production",
      resource: "orders",
    },
  ])
  const user = userEvent.setup()
  await screen.findByRole("heading", { level: 1, name: "orders" })
  await user.click(screen.getByRole("tab", { name: "Settings" }))
  const zone = screen.getByRole("region", { name: "Danger zone" })
  await waitFor(() =>
    expect(zone).toHaveTextContent("One project environment uses it")
  )
  expect(within(zone).getByRole("button", { name: "Delete" })).toBeDisabled()
})

test("a provider resource's endpoint is read-only here", async () => {
  serve({ origin: "provider", name: "resource-abc-1" })
  expect(
    await screen.findByText("This belongs to a provider resource")
  ).toBeInTheDocument()
  expect(screen.queryByRole("tab", { name: "Settings" })).toBeNull()
  expect(
    screen.queryByRole("button", { name: "More service actions" })
  ).toBeNull()
  await userEvent.click(screen.getByRole("tab", { name: "Access" }))
  expect(screen.getByRole("region", { name: "Sharing" })).toHaveTextContent(
    "Every project in the workspace."
  )
  expect(screen.queryByRole("button", { name: "Save sharing" })).toBeNull()
})

test("a user Loomplane made is dropped once nothing uses it, by its name", async () => {
  ownUsers = [
    {
      endpoint_id: "endpoint_orders",
      user: "lp_shop_api",
      database: "api",
      created_database: true,
      project_id: "project_shop",
      service: "api",
      environments: [],
      created_at: "2026-11-01T00:00:00Z",
      revoked_at: "2026-11-20T00:00:00Z",
    },
    {
      endpoint_id: "endpoint_orders",
      user: "lp_billing_worker",
      database: "worker",
      created_database: true,
      project_id: "project_billing",
      service: "worker",
      environments: ["env_p"],
      created_at: "2026-11-01T00:00:00Z",
    },
  ]
  const writes = serve({}, [], () => Response.json({ users: [] }))
  const user = userEvent.setup()
  await user.click(await screen.findByRole("tab", { name: /Used by|Uses/ }))
  const made = await screen.findByRole("region", {
    name: "Users Loomplane made",
  })
  await waitFor(() =>
    expect(made).toHaveTextContent("api in shop, database api")
  )
  expect(made).toHaveTextContent("In use in 1 environment")
  // One still in use can't be dropped.
  expect(
    within(made).queryByRole("button", { name: "Drop lp_billing_worker" })
  ).toBeNull()
  await user.click(
    within(made).getByRole("button", { name: "Drop lp_shop_api" })
  )
  const dialog = await screen.findByRole("alertdialog")
  expect(dialog).toHaveTextContent("with all its data")
  const drop = within(dialog).getByRole("button", { name: "Drop" })
  expect(drop).toBeDisabled()
  await user.type(within(dialog).getByRole("textbox"), "lp_shop_api")
  await user.click(drop)
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({
    method: "POST",
    path: "/api/v1/catalog/endpoints/endpoint_orders/users/lp_shop_api/drops",
    body: { confirm: "lp_shop_api", target: "" },
  })
})
