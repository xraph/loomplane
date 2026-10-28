import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ProjectService } from "@/pages/project-service"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    className,
  }: {
    children: React.ReactNode
    to: string
    className?: string
  }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
  useNavigate: () => () => undefined,
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/shell", () => ({
  useShell: () => ({ openDeploy: () => undefined }),
}))
vi.mock("@/lib/use-runs", () => ({ useRuns: () => ({ runs: [] }) }))
const toasts = vi.hoisted(() => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }),
}))
vi.mock("sonner", () => ({ toast: toasts.toast }))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  toasts.toast.success.mockReset()
})

type Json = Record<string, unknown>
type Write = { method: string; path: string; body: unknown }

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

const zero = "0001-01-01T00:00:00Z"

function route(hostname: string, over: Json = {}): Json {
  return {
    id: `route_${hostname}`,
    operation_id: "routeop_old",
    org_id: "org",
    project_id: "p",
    revision: 3,
    status: "active",
    updated_at: zero,
    input: {
      service: "web",
      hostname,
      environment_id: "env_prod",
      port: 8080,
      zone_id: "zone_dev",
      cloud_id: "cloud_1",
    },
    ...over,
  }
}

function operation(status: string, over: Json = {}): Json {
  return {
    id: "routeop_1",
    kind: "apply",
    status,
    review: "review-token",
    attempt: 1,
    revision: 1,
    generation: 0,
    message: "",
    created_at: zero,
    expires_at: zero,
    not_before: zero,
    dns_attempted: false,
    desired_record: {
      id: "",
      zone_id: "zone_dev",
      type: "A",
      name: "shop.acme.dev",
      value: "203.0.113.10",
      ttl: 300,
    },
    route: { id: "route_new", revision: 1 },
    ...over,
  }
}

const verified = [
  {
    zone_id: "zone_dev",
    name: "acme.dev",
    mode: "managed",
    provider: "cloudflare",
    verified: true,
    wildcards: true,
    hostnames: [],
  },
]

type Fixture = {
  ports?: number[]
  deployed?: boolean
  domains?: Json[]
  routes?: () => Json[]
  write?: (w: Write) => Response | undefined
}

function serve(fixture: Fixture = {}) {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET"
      const url = new URL(String(input), "http://localhost")
      const path = url.pathname
      if (method !== "GET") {
        const w = {
          method,
          path,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        }
        writes.push(w)
        const answer = fixture.write?.(w)
        if (answer) return answer
        if (path.endsWith("/domain-routes/preview"))
          return json(operation("preview"))
        if (/\/route-operations\/[^/]+\/apply$/.test(path))
          return json(operation("pending"))
        if (/\/domain-routes\/[^/]+\/remove$/.test(path))
          return json(operation("pending", { kind: "remove" }))
        throw new Error(`Unexpected request ${method} ${path}`)
      }
      if (path === "/api/v1/projects/p")
        return json({ id: "p", name: "acme-shop", status: "ready" })
      if (path === "/api/v1/projects/p/environments")
        return json({
          environments: [
            { id: "env_prod", name: "production", kind: "production" },
          ],
        })
      if (path === "/api/v1/projects/p/graph")
        return json({
          project_id: "p",
          sources: [],
          graph: {
            version: 2,
            services: [
              {
                id: "svc:web",
                name: "web",
                path: "cmd/web",
                entrypoint: "cmd/web",
                kind: "http-service",
                deployable: true,
                ecosystem: "go",
                confidence: "certain",
                evidence: [],
                commands: {},
                ports: fixture.ports ?? [8080],
              },
            ],
            packages: [],
            resources: [],
            edges: [],
            workspaces: [],
            build_systems: [],
            notes: [],
          },
          excluded: [],
        })
      if (path === "/api/v1/projects/p/bindings")
        return json({
          bindings:
            (fixture.deployed ?? true)
              ? [
                  {
                    id: "b1",
                    name: "web",
                    subject: "service",
                    provider: "server",
                    commit_sha: "e7163cd0000",
                    updated_at: "2026-10-26T18:12:00Z",
                    drift: "",
                  },
                ]
              : [],
        })
      if (path === "/api/v1/projects/p/domain-routes")
        return json({ routes: fixture.routes?.() ?? [] })
      if (path === "/api/v1/projects/p/domains")
        return json({ domains: fixture.domains ?? verified })
      return json({})
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ProjectService projectId="p" name="web" environment="production" />
    </ClientProvider>
  )
  return writes
}

const posts = (writes: Write[]) => writes.filter((w) => w.method === "POST")

async function openDialog() {
  const card = await screen.findByRole("region", { name: "Domains" })
  await userEvent.click(
    await within(card).findByRole("button", { name: "Add a domain" })
  )
  return screen.findByRole("dialog", { name: "Add a domain" })
}

test("adds a domain to a service", async () => {
  let routes: Json[] = []
  const writes = serve({ routes: () => routes })
  const card = await screen.findByRole("region", { name: "Domains" })
  // Granting a domain is still one click away.
  expect(
    within(card).getByRole("link", { name: "Manage domains" })
  ).toHaveAttribute("href", "/domains")

  const dialog = await openDialog()
  await userEvent.type(
    await within(dialog).findByRole("textbox", { name: "Subdomain" }),
    "shop"
  )
  expect(within(dialog).getByText("Port 8080")).toBeVisible()
  await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }))

  await vi.waitFor(() =>
    expect(posts(writes)).toEqual([
      {
        method: "POST",
        path: "/api/v1/projects/p/domain-routes/preview",
        body: {
          zone_id: "zone_dev",
          cloud_id: "",
          environment_id: "env_prod",
          hostname: "shop.acme.dev",
          port: 8080,
          service: "web",
        },
      },
    ])
  )
  const record = await within(dialog).findByRole("group", {
    name: "DNS record",
  })
  expect(record).toHaveTextContent("shop.acme.dev")
  expect(record).toHaveTextContent("A")
  expect(record).toHaveTextContent("203.0.113.10")

  routes = [route("shop.acme.dev", { status: "pending" })]
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add domain" })
  )
  await vi.waitFor(() =>
    expect(posts(writes)[1]).toEqual({
      method: "POST",
      path: "/api/v1/projects/p/route-operations/routeop_1/apply",
      body: { review: "review-token" },
    })
  )
  await vi.waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Add a domain" })).toBeNull()
  )
  expect(toasts.toast.success).toHaveBeenCalledWith(
    "shop.acme.dev now points at web in production."
  )
  expect(await within(card).findByText("shop.acme.dev")).toBeVisible()
})

test("picks the port when the service has several", async () => {
  const writes = serve({ ports: [8080, 9090] })
  const dialog = await openDialog()
  await within(dialog).findByRole("textbox", { name: "Subdomain" })
  await userEvent.click(within(dialog).getByRole("combobox", { name: "Port" }))
  await userEvent.click(await screen.findByRole("option", { name: "9090" }))
  await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }))
  await vi.waitFor(() =>
    expect(posts(writes).map((w) => (w.body as Json).port)).toEqual([9090])
  )
})

test("says a service with no port can't take a hostname", async () => {
  const writes = serve({ ports: [] })
  const dialog = await openDialog()
  expect(dialog).toHaveTextContent(
    "web doesn't expose a port, so it can't take a hostname."
  )
  expect(within(dialog).queryByRole("button", { name: "Preview" })).toBeNull()
  expect(writes).toEqual([])
})

test("shows why a preview was refused", async () => {
  serve({
    write: (w) =>
      w.path.endsWith("/preview")
        ? json(
            {
              message: "That hostname already routes to api in production.",
            },
            409
          )
        : undefined,
  })
  const dialog = await openDialog()
  await userEvent.type(
    await within(dialog).findByRole("textbox", { name: "Subdomain" }),
    "shop"
  )
  await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }))
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    "That hostname already routes to api in production."
  )
  // Nothing was previewed, so the button still previews.
  expect(within(dialog).getByRole("button", { name: "Preview" })).toBeVisible()
})

test("removes a hostname from the service", async () => {
  let routes = [route("api.acme.dev")]
  const writes = serve({ routes: () => routes })
  const card = await screen.findByRole("region", { name: "Domains" })
  expect(await within(card).findByText("api.acme.dev")).toBeVisible()
  await userEvent.click(
    within(card).getByRole("button", { name: "Remove api.acme.dev" })
  )
  const confirm = await screen.findByRole("alertdialog", {
    name: "Remove api.acme.dev?",
  })
  routes = []
  await userEvent.click(within(confirm).getByRole("button", { name: "Remove" }))
  await vi.waitFor(() =>
    expect(posts(writes)).toEqual([
      {
        method: "POST",
        path: "/api/v1/projects/p/domain-routes/route_api.acme.dev/remove",
        body: { revision: 3 },
      },
    ])
  )
  await vi.waitFor(() =>
    expect(within(card).queryByText("api.acme.dev")).toBeNull()
  )
})

test("points to Domains when the project has no domain yet", async () => {
  const writes = serve({ domains: [] })
  const dialog = await openDialog()
  expect(
    await within(dialog).findByText("No domain is granted to this project yet.")
  ).toBeVisible()
  expect(
    within(dialog).getByRole("link", { name: "Open Domains" })
  ).toHaveAttribute("href", "/domains")
  expect(within(dialog).queryByRole("button", { name: "Preview" })).toBeNull()
  expect(writes).toEqual([])
})

test("says a service not deployed here gets a hostname once it deploys", async () => {
  const writes = serve({ deployed: false })
  const dialog = await openDialog()
  expect(dialog).toHaveTextContent(
    "web isn't deployed in production yet. You can give it a hostname once it deploys there."
  )
  expect(within(dialog).queryByRole("button", { name: "Preview" })).toBeNull()
  expect(writes).toEqual([])
})
