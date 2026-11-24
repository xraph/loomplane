import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"

import { ServiceConnections } from "./service-connections"

vi.mock("@/lib/project-permissions", () => ({
  useProjectCan: () => () => true,
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const endpointID = "endpoint_01jd8x5k2m3n4p5q6r7s8t9v0w"

const manifest = {
  version: 1,
  services: {
    api: {
      source: "cmd/api",
      needs: ["orders", "cache"],
      wiring: { orders: { env: { PGHOST: "host", PGUSER: "user" } } },
    },
  },
  resources: {
    orders: { kind: "postgres", existing_resource: endpointID },
    cache: { kind: "redis", provider: "server" },
  },
}

let writes: Record<string, unknown>[] = []
beforeEach(() => {
  writes = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        writes.push(JSON.parse(String(init.body)) as Record<string, unknown>)
        return Response.json({ revision: 2, yaml: "", manifest })
      }
      switch (path) {
        case "/api/v1/projects/p/manifest":
          return Response.json({ revision: 1, yaml: "", manifest })
        case "/api/v1/catalog/endpoints":
          return Response.json({
            endpoints: [
              {
                id: endpointID,
                org_id: "org",
                name: "orders-db",
                engine: "postgres",
                host: "db.example.com",
                port: 5432,
                username: "app",
                database: "orders",
                tls: "require",
                created_by: "u",
                created_at: "2026-11-01T00:00:00Z",
                revision: 1,
                origin: "registered",
                description: "",
                environment_label: "",
                restricted: false,
                projects: [],
                admin_set: true,
                last_check: {
                  at: "2026-11-01T00:00:00Z",
                  ok: true,
                  capabilities: { create_database: true, create_user: false },
                  admin: { create_database: true, create_user: true },
                },
              },
            ],
          })
        case "/api/v1/projects/p/variables":
          return Response.json({
            variables: [
              {
                id: "v1",
                org_id: "org",
                project_id: "p",
                scope: "project",
                environment: "",
                key: "DB_URL",
                value: "x",
                secret: false,
                created_at: "2026-11-01T00:00:00Z",
                created_by: "u",
                updated_at: "2026-11-01T00:00:00Z",
                updated_by: "u",
              },
            ],
          })
      }
      return Response.json({})
    }
  )
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const project = { id: "p", name: "Shop", slug: "shop" } as never

function show() {
  return render(
    <ClientProvider client={forgeClient}>
      <ServiceConnections project={project} service="api" />
    </ClientProvider>
  )
}

it("lists how a service reads what it needs, and changes one", async () => {
  const user = userEvent.setup()
  show()
  const table = await screen.findByRole("table", { name: "How it connects" })
  expect(table).toHaveTextContent("Same credentials, in PGHOST, PGUSER")
  expect(table).toHaveTextContent("In REDIS_URL")

  await user.click(
    within(table).getByRole("button", { name: "Change how api reads orders" })
  )
  const dialog = await screen.findByRole("dialog")
  // It opens on what loomplane.yaml has.
  expect(
    within(dialog).getByRole("textbox", { name: "Host variable" })
  ).toHaveValue("PGHOST")
  await user.click(within(dialog).getByRole("radio", { name: "One variable" }))
  const name = within(dialog).getByRole("textbox", {
    name: "Variable api reads orders in",
  })
  await user.clear(name)
  await user.type(name, "DB_URL")
  // The project sets DB_URL itself, which would win.
  expect(
    within(dialog).getByText(/DB_URL is set by hand in every environment/)
  ).toBeInTheDocument()
  await user.click(within(dialog).getByRole("radio", { name: /Its own user/ }))
  await user.click(within(dialog).getByRole("button", { name: "Save" }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({
    wire: {
      service: "api",
      need: "orders",
      access: "own_user",
      database: "api",
      env: { DB_URL: "url" },
    },
  })
})

it("goes back to the defaults", async () => {
  const user = userEvent.setup()
  show()
  await user.click(
    await screen.findByRole("button", { name: "Change how api reads orders" })
  )
  await user.click(
    within(await screen.findByRole("dialog")).getByRole("button", {
      name: "Use the defaults",
    })
  )
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toEqual({ wire: { service: "api", need: "orders" } })
})
