import { ClientProvider } from "@forge-go/client-react"
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type {
  CatalogconnectEndpoint,
  KindInfo,
} from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import {
  CatalogDependencyPicker,
  type ManifestResource,
} from "@/components/catalog-dependency-picker"
import { initialDependencyPicks, type DependencyPicks } from "@/lib/catalog-add"
import { defineCatalogKind } from "@/lib/catalog-kinds"
import { CatalogKind } from "@/pages/catalog-kind"
import { Services } from "@/pages/services"
import { createDashboardRouter } from "@/router"

const navigate = vi.hoisted(() => vi.fn())
vi.mock("@/App", async () => {
  const { Outlet } = await import("@tanstack/react-router")
  return { default: Outlet, Problem: () => null }
})
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    ...props
  }: {
    children: React.ReactNode
    to: string
  }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
  useNavigate: () => navigate,
}))

beforeEach(() => {
  // jsdom has no scrolling; the router restores it in the browser.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  navigate.mockReset()
  forgeClient.clear()
})

const sample: KindInfo = {
  name: "sample",
  title: "Sample",
  description: "A composed kind for tests.",
  placement: "app-project",
  targets: ["server", "kubernetes"],
  versions: ["1"],
  engine: false,
  dependencies: [
    {
      name: "postgres",
      title: "PostgreSQL",
      kinds: ["postgres"],
      optional: false,
      default: false,
      external: true,
    },
    {
      name: "cache",
      title: "Cache",
      kinds: ["redis", "valkey"],
      optional: true,
      default: false,
      external: false,
    },
    {
      name: "search",
      title: "Search",
      kinds: ["postgres"],
      optional: true,
      default: true,
      external: false,
    },
  ],
  components: [
    { name: "api", role: "process", stateful: false, protection: "none" },
    { name: "migrate", role: "job", stateful: false, protection: "none" },
  ],
}

// A kind with its own add dialog, and one kept out of the catalog, the
// way their own modules would register them.
defineCatalogKind({
  kind: "dialoged",
  title: "Dialoged",
  group: "gateway",
  blurb: "A kind that brings its own add dialog.",
  features: [],
  panels: [],
  addDialog: ({ projectId, projectName, onClose, onPlanned }) => (
    <div role="dialog" aria-label="Add Dialoged">
      <p>
        Adding to {projectName} ({projectId})
      </p>
      <button type="button" onClick={onClose}>
        Cancel
      </button>
      <button type="button" onClick={() => onPlanned("plan_9")}>
        Review plan
      </button>
    </div>
  ),
})
defineCatalogKind({
  kind: "unlisted",
  title: "Unlisted",
  group: "gateway",
  blurb: "A kind that never gets a card.",
  features: [],
  panels: [],
  hidden: true,
})

const appKind = (name: string, title: string): KindInfo => ({
  name,
  title,
  placement: "app-project",
  targets: ["server"],
  versions: ["1"],
  engine: false,
})

const postgresKind: KindInfo = {
  name: "postgres",
  title: "PostgreSQL",
  placement: "service-project",
  targets: ["server", "kubernetes"],
  versions: ["17"],
  engine: true,
}

function endpoint(
  id: string,
  name: string,
  engine: string
): CatalogconnectEndpoint {
  return {
    id,
    name,
    engine,
    host: `${name}.example.com`,
    port: engine === "redis" ? 6379 : 5432,
    tls: "require",
    orgId: "org",
    createdBy: "u",
    createdAt: "2026-10-21T12:00:00Z",
    revision: 1,
  }
}

const resources: ManifestResource[] = [
  { name: "db", kind: "postgres" },
  { name: "sessions", kind: "redis" },
]
const endpoints = [
  endpoint("ep_1", "prod-pg", "postgres"),
  endpoint("ep_2", "prod-redis", "redis"),
]

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

type Write = { method: string; path: string; body: unknown }

// serve stubs the API a kind's page reads and writes, recording every
// write it receives.
function serve(
  options: {
    kinds?: () => Response
    projects?: { id: string; name: string; icon?: string }[]
    edit?: () => Response
    created?: CatalogconnectEndpoint
  } = {}
) {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET"
      const path = new URL(String(input), "http://localhost").pathname
      if (method !== "GET") {
        writes.push({
          method,
          path,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        })
        if (path.endsWith("/manifest/edits"))
          return options.edit?.() ?? json({ id: "rev_2", revision: 2 })
        if (path.endsWith("/plans"))
          return json({ id: "plan_1", status: "planning" })
        if (path.endsWith("/catalog/endpoints"))
          return json(
            {
              ...(options.created ?? endpoint("ep_9", "new-pg", "postgres")),
              org_id: "org",
              created_by: "u",
              created_at: "2026-10-21T12:00:00Z",
            },
            201
          )
        if (path.endsWith("/projects"))
          return json({ message: "Loomplane couldn't finish that." }, 500)
        throw new Error(`Unexpected request ${method} ${path}`)
      }
      if (path.endsWith("/catalog/kinds"))
        return options.kinds?.() ?? json({ kinds: [postgresKind, sample] })
      if (path.endsWith("/catalog/endpoints"))
        return json({ endpoints: endpoints })
      if (path.endsWith("/projects"))
        return json({
          projects: options.projects ?? [{ id: "p", name: "Shop" }],
        })
      if (path.endsWith("/projects/p/environments"))
        return json({
          environments: [
            { id: "env_prod", name: "production" },
            { id: "env_stage", name: "staging" },
          ],
        })
      if (path.endsWith("/projects/p/manifest"))
        return json({
          id: "rev_1",
          revision: 1,
          yaml: "version: 1",
          manifest: {
            version: 1,
            resources: {
              db: { kind: "postgres" },
              sessions: { kind: "redis" },
            },
          },
        })
      if (path.endsWith("/connections"))
        return json({
          connections: [
            {
              id: "conn_1",
              name: "edge-1",
              cloud: "server",
              status: "ready",
              account: "edge-1",
              details: { architecture: "amd64" },
            },
            {
              id: "conn_down",
              name: "broken",
              cloud: "server",
              status: "failed",
              account: "broken",
              details: { architecture: "amd64" },
            },
          ],
        })
      if (path.endsWith("/providers")) return json({ providers: [] })
      throw new Error(`Unexpected request ${method} ${path}`)
    }
  )
  return writes
}

function Picker({
  initial,
  onPick,
}: {
  initial?: DependencyPicks
  onPick?: (picks: DependencyPicks) => void
}) {
  const [value, setValue] = useState<DependencyPicks>(
    initial ?? initialDependencyPicks(sample)
  )
  return (
    <CatalogDependencyPicker
      kind={sample}
      resources={resources}
      endpoints={endpoints}
      value={value}
      onChange={(next) => {
        setValue(next)
        onPick?.(next)
      }}
    />
  )
}

function mountPicker(props: Parameters<typeof Picker>[0] = {}) {
  return render(
    <ClientProvider client={forgeClient}>
      <Picker {...props} />
    </ClientProvider>
  )
}

function mountKind(kind = "sample") {
  return render(
    <ClientProvider client={forgeClient}>
      <CatalogKind kind={kind} />
    </ClientProvider>
  )
}

const options = (group: HTMLElement) =>
  within(group)
    .getAllByRole("radio")
    .map((radio) => radio.getAttribute("aria-label"))

test("dependency picker offers provision new, a matching resource, or an external endpoint", async () => {
  const onPick = vi.fn()
  mountPicker({ onPick })
  const postgres = screen.getByRole("radiogroup", { name: "PostgreSQL" })
  expect(options(postgres)).toEqual([
    "Provision new",
    "db (PostgreSQL)",
    "prod-pg (external)",
  ])
  // The redis resource and endpoint are for other kinds.
  expect(within(postgres).queryByText(/sessions/)).not.toBeInTheDocument()
  expect(within(postgres).queryByText(/prod-redis/)).not.toBeInTheDocument()
  expect(
    screen.queryByText("Not managed by Loomplane: no backups or restores")
  ).not.toBeInTheDocument()

  await userEvent.click(
    within(postgres).getByRole("radio", { name: "prod-pg (external)" })
  )
  expect(
    within(postgres).getByRole("radio", { name: "prod-pg (external)" })
  ).toHaveAttribute("aria-checked", "true")
  expect(
    screen.getByText("Not managed by Loomplane: no backups or restores")
  ).toBeInTheDocument()
  expect(onPick).toHaveBeenLastCalledWith(
    expect.objectContaining({
      postgres: { mode: "external", external: "ep_1", kind: "postgres" },
    })
  )
})

test("optional dependency starts off unless its default is on", () => {
  expect(initialDependencyPicks(sample)).toEqual({
    postgres: { mode: "new", kind: "postgres" },
    cache: { mode: "off" },
    search: { mode: "new", kind: "postgres" },
  })
  mountPicker()
  expect(screen.getByRole("switch", { name: "Use Cache" })).toHaveAttribute(
    "aria-checked",
    "false"
  )
  expect(screen.getByRole("switch", { name: "Use Search" })).toHaveAttribute(
    "aria-checked",
    "true"
  )
  // A dependency that's off has nothing to choose.
  expect(
    screen.queryByRole("radiogroup", { name: "Cache" })
  ).not.toBeInTheDocument()
  // Only a dependency that takes an external endpoint offers to add one.
  expect(
    screen.getAllByRole("button", { name: "Add an external endpoint" })
  ).toHaveLength(1)
})

test("adding an external endpoint stores it and selects it", async () => {
  const writes = serve()
  const onPick = vi.fn()
  mountPicker({ onPick })
  await userEvent.click(
    screen.getByRole("button", { name: "Add an external endpoint" })
  )
  const dialog = await screen.findByRole("dialog")
  const dsn = within(dialog).getByLabelText("Connection string")
  expect(dsn).toHaveAttribute("type", "password")
  // Browsers don't fill in a saved password here.
  expect(dsn).toHaveAttribute("autocomplete", "new-password")
  await userEvent.type(within(dialog).getByLabelText("Name"), "new-pg")
  await userEvent.type(dsn, "postgres://app:secret@db.example.com/app")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Save endpoint" })
  )

  expect(
    await screen.findByRole("radio", { name: "new-pg (external)" })
  ).toHaveAttribute("aria-checked", "true")
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/catalog/endpoints",
      body: {
        name: "new-pg",
        engine: "postgres",
        dsn: "postgres://app:secret@db.example.com/app",
      },
    },
  ])
  expect(onPick).toHaveBeenLastCalledWith(
    expect.objectContaining({
      postgres: { mode: "external", external: "ep_9", kind: "postgres" },
    })
  )
  // The connection string isn't kept for the next endpoint.
  await userEvent.click(
    screen.getByRole("button", { name: "Add an external endpoint" })
  )
  expect(
    within(await screen.findByRole("dialog")).getByLabelText(
      "Connection string"
    )
  ).toHaveValue("")
})

test("database kinds keep the standalone flow", async () => {
  const writes = serve()
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/services/postgres"] })
  )
  await router.load()
  render(
    <ClientProvider client={forgeClient}>
      <RouterProvider router={router} />
    </ClientProvider>
  )
  // ServiceCatalog: a standalone project named after the engine.
  const deploy = await screen.findByRole("button", {
    name: "Deploy PostgreSQL",
  })
  expect(screen.getByLabelText("Project name")).toHaveValue("PostgreSQL")
  await userEvent.click(deploy)
  await screen.findByText(/Loomplane couldn't finish that/)
  expect(writes.map((w) => `${w.method} ${w.path}`)).toEqual([
    "POST /api/v1/projects",
  ])
})

test("app-project kinds open the add-to-project page", async () => {
  serve()
  const router = createDashboardRouter(
    createMemoryHistory({ initialEntries: ["/services/sample"] })
  )
  await router.load()
  render(
    <ClientProvider client={forgeClient}>
      <RouterProvider router={router} />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("heading", { level: 1, name: /Sample/ })
  ).toBeInTheDocument()
  expect(
    await screen.findByRole("button", { name: "Add Sample and plan" })
  ).toBeInTheDocument()
})

test("adds the service into the chosen project and opens the plan", async () => {
  const writes = serve()
  mountKind()
  expect(await screen.findByRole("radio", { name: /edge-1/ })).toHaveAttribute(
    "aria-checked",
    "true"
  )
  // A target that failed its check isn't offered.
  expect(
    screen.queryByRole("radio", { name: /broken/ })
  ).not.toBeInTheDocument()
  // Search takes PostgreSQL too, so the choice is made in its own group.
  await userEvent.click(
    within(
      await screen.findByRole("radiogroup", { name: "PostgreSQL" })
    ).getByRole("radio", { name: "db (PostgreSQL)" })
  )
  await userEvent.click(screen.getByRole("radio", { name: "staging" }))
  const name = screen.getByLabelText("Resource name")
  expect(name).toHaveValue("sample")
  await userEvent.clear(name)
  await userEvent.type(name, "gateway")
  await userEvent.click(
    screen.getByRole("button", { name: "Add Sample and plan" })
  )

  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "plan_1" },
    })
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/projects/p/manifest/edits",
      body: {
        add_resource: {
          name: "gateway",
          kind: "sample",
          provider: "server",
          target: "conn_1",
          version: "1",
          uses: { postgres: { resource: "db" } },
        },
      },
    },
    {
      method: "POST",
      path: "/api/v1/projects/p/plans",
      body: { environment: "staging" },
    },
  ])
})

test("saving an endpoint on the page doesn't add the service", async () => {
  // The dialog is portalled, but React still bubbles its submit to the
  // page's form.
  const writes = serve()
  mountKind()
  await screen.findByRole("radio", { name: /edge-1/ })
  await userEvent.click(
    await screen.findByRole("button", { name: "Add an external endpoint" })
  )
  const dialog = await screen.findByRole("dialog")
  await userEvent.type(within(dialog).getByLabelText("Name"), "new-pg")
  await userEvent.type(
    within(dialog).getByLabelText("Connection string"),
    "postgres://app:secret@db.example.com/app"
  )
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Save endpoint" })
  )
  expect(
    await screen.findByRole("radio", { name: "new-pg (external)" })
  ).toHaveAttribute("aria-checked", "true")
  expect(writes.map((w) => w.path)).toEqual(["/api/v1/catalog/endpoints"])
  expect(navigate).not.toHaveBeenCalled()
})

test("the page says what the manifest refused and doesn't plan", async () => {
  const writes = serve({
    edit: () => json({ message: "gateway is taken" }, 422),
  })
  mountKind()
  await screen.findByRole("radio", { name: /edge-1/ })
  await userEvent.click(
    await screen.findByRole("button", { name: "Add Sample and plan" })
  )
  expect(await screen.findByText("Gateway is taken.")).toBeInTheDocument()
  expect(writes.map((w) => w.path)).toEqual([
    "/api/v1/projects/p/manifest/edits",
  ])
  expect(navigate).not.toHaveBeenCalled()
})

test("a name the manifest already has is refused before saving", async () => {
  serve()
  mountKind()
  const name = await screen.findByLabelText("Resource name")
  await userEvent.clear(name)
  await userEvent.type(name, "db")
  expect(
    screen.getByText("The manifest already has something called db.")
  ).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Add Sample and plan" })
  ).toBeDisabled()
})

test("kinds the role can't read and kinds that failed read differently", async () => {
  serve({ kinds: () => json({ message: "Forbidden" }, 403) })
  const first = mountKind()
  expect(
    await screen.findByText("You can't add catalog services")
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Retry" })
  ).not.toBeInTheDocument()
  first.unmount()
  forgeClient.clear()

  serve({ kinds: () => json({ message: "The catalog is down" }, 500) })
  mountKind()
  expect(await screen.findByRole("button", { name: "Retry" })).toBeVisible()
})

test("a kind the API doesn't know says so", async () => {
  serve()
  mountKind("mystery")
  expect(
    await screen.findByText("The catalog has no kind called mystery")
  ).toBeInTheDocument()
})

test("registered application kinds follow the native service recipes", async () => {
  serve({
    kinds: () =>
      json({
        kinds: [
          postgresKind,
          sample,
          {
            name: "helper",
            title: "Helper",
            placement: "dependency",
            targets: ["server"],
            versions: ["1"],
            engine: false,
          },
        ],
      }),
  })
  render(
    <ClientProvider client={forgeClient}>
      <Services />
    </ClientProvider>
  )
  const catalog = screen.getByRole("region", { name: "Catalog" })
  await within(catalog).findByRole("button", { name: "Add Sample" })
  const cards = within(catalog)
    .getAllByRole("article")
    .map((card) => within(card).getAllByRole("button")[0].textContent)
  expect(cards).toEqual([
    "Deploy PostgreSQL",
    "Deploy MySQL",
    "Deploy MariaDB",
    "Deploy Redis",
    "Deploy Valkey",
    "Deploy NATS JetStream",
    "Add Sample",
  ])
  expect(within(catalog).queryByText(/Helper/)).not.toBeInTheDocument()
  await userEvent.click(
    within(catalog).getByRole("radio", { name: /^Data\s*1$/ })
  )
  expect(
    within(catalog).getByRole("button", { name: "Deploy NATS JetStream" })
  ).toBeInTheDocument()
  expect(
    within(catalog).queryByRole("button", { name: "Deploy PostgreSQL" })
  ).not.toBeInTheDocument()
})

test("hidden modules and dependency-only kinds stay out of the catalog", async () => {
  serve({
    kinds: () =>
      json({
        kinds: [
          postgresKind,
          sample,
          appKind("unlisted", "Unlisted"),
          {
            ...appKind("helper", "Helper"),
            placement: "dependency",
          },
        ],
      }),
  })
  render(
    <ClientProvider client={forgeClient}>
      <Services />
    </ClientProvider>
  )
  const catalog = screen.getByRole("region", { name: "Catalog" })
  await within(catalog).findByRole("button", { name: "Add Sample" })
  expect(within(catalog).queryByText(/Unlisted/)).not.toBeInTheDocument()
  expect(within(catalog).queryByText(/Helper/)).not.toBeInTheDocument()
  // The hidden kind's group doesn't show for it either.
  expect(
    within(catalog).queryByRole("radio", { name: /Gateways/ })
  ).not.toBeInTheDocument()
})

test("a module's add dialog replaces the generic flow", async () => {
  const writes = serve({
    kinds: () => json({ kinds: [appKind("dialoged", "Dialoged")] }),
    projects: [
      { id: "p", name: "Shop" },
      { id: "p2", name: "Billing" },
    ],
  })
  mountKind("dialoged")
  const project = await screen.findByRole("combobox", { name: "Project" })
  // The generic steps after the project aren't there.
  expect(screen.queryByLabelText("Resource name")).not.toBeInTheDocument()
  expect(
    screen.queryByRole("radiogroup", { name: "Environment" })
  ).not.toBeInTheDocument()
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()

  await userEvent.click(project)
  await userEvent.click(await screen.findByRole("option", { name: "Billing" }))
  await userEvent.click(
    screen.getByRole("button", { name: "Add Dialoged to Billing" })
  )
  const dialog = await screen.findByRole("dialog", { name: "Add Dialoged" })
  expect(dialog).toHaveTextContent("Adding to Billing (p2)")

  await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()

  await userEvent.click(
    screen.getByRole("button", { name: "Add Dialoged to Billing" })
  )
  await userEvent.click(
    within(await screen.findByRole("dialog")).getByRole("button", {
      name: "Review plan",
    })
  )
  expect(navigate).toHaveBeenCalledWith({
    to: "/deployments/$id",
    params: { id: "plan_9" },
  })
  expect(writes).toEqual([])
})

test("database service projects aren't offered for an app-project kind", async () => {
  serve({
    projects: [
      { id: "db", name: "PostgreSQL", icon: "database" },
      { id: "p", name: "Shop" },
    ],
  })
  mountKind()
  const project = await screen.findByRole("combobox", { name: "Project" })
  expect(project).toHaveTextContent("Shop")
  await userEvent.click(project)
  expect(
    (await screen.findAllByRole("option")).map((o) => o.textContent)
  ).toEqual(["Shop"])
})

test("disposable catalog services don't inherit blanket durability claims", async () => {
  serve({ kinds: () => json({ kinds: [appKind("memcached", "Memcached")] }) })
  render(
    <ClientProvider client={forgeClient}>
      <Services />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("button", { name: "Add Memcached" })
  ).toBeEnabled()
  expect(
    screen.queryByText(/Every service gets a private endpoint and a volume/)
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText(
      /Databases and caches Loomplane runs for you, with persistent storage/
    )
  ).not.toBeInTheDocument()
  await userEvent.click(
    await screen.findByRole("button", { name: "Browse catalog" })
  )
  const search = screen.getByRole("searchbox", { name: "Search the catalog" })
  expect(search).toHaveFocus()
  await userEvent.type(search, "not-a-real-service")
  expect(screen.getByText("Nothing in the catalog matches")).toBeVisible()
  await userEvent.click(screen.getByRole("button", { name: "Clear filters" }))
  expect(search).toHaveValue("")
  expect(screen.getByRole("button", { name: "Add Memcached" })).toBeEnabled()
})
