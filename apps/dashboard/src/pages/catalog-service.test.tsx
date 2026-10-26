import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { defineCatalogKind } from "@/lib/catalog-kinds"
import { CatalogService } from "@/pages/catalog-service"

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
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

// Two test kinds, registered the way a kind's own module would.
defineCatalogKind({
  kind: "sample",
  title: "Sample",
  group: "gateway",
  blurb: "A composed kind for tests.",
  features: [],
  panels: [
    {
      id: "routes",
      title: "Routes",
      component: (props) => (
        <p>
          Routes of {props.resource} in {props.environmentId} with{" "}
          {props.components.map((c) => c.name).join(", ")}
        </p>
      ),
    },
  ],
})
defineCatalogKind({
  kind: "ordered",
  title: "Ordered",
  group: "data",
  blurb: "Panels out of order, one of them hidden.",
  features: [],
  panels: [
    { id: "late", title: "Late", order: 2, component: () => null },
    { id: "early", title: "Early", order: 1, component: () => null },
    {
      id: "never",
      title: "Never",
      when: (props) => props.config.provider === "kubernetes",
      component: () => null,
    },
  ],
})

// A kind with everything a module can add: counts, actions, a subtitle
// and its own icon.
defineCatalogKind({
  kind: "counted",
  title: "Counted",
  group: "gateway",
  blurb: "The kind's catalog blurb.",
  features: [],
  icon: (props) => <svg data-testid="counted-icon" {...props} />,
  actions: (props) => (
    <button type="button">Open {props.resource} admin</button>
  ),
  subtitle: (props) => `Hosted gateway · ${props.config.provider}`,
  panels: [
    {
      id: "routes",
      title: "Routes",
      order: 1,
      // A count may come from a hook.
      useCount: () => useState(6)[0],
      component: () => null,
    },
    {
      id: "domains",
      title: "Domains",
      order: 2,
      useCount: (props) => (props.resource === "edge" ? 3 : 0),
      component: () => null,
    },
    {
      id: "discovery",
      title: "Discovery",
      order: 3,
      useCount: () => undefined,
      component: () => null,
    },
    { id: "settings", title: "Settings", order: 4, component: () => null },
    {
      id: "kubernetes",
      title: "Kubernetes",
      when: (props) => props.config.provider === "kubernetes",
      useCount: () => useState(9)[0],
      component: () => null,
    },
  ],
})
defineCatalogKind({
  kind: "logoed",
  title: "Logoed",
  logo: "redis",
  group: "cache",
  blurb: "A kind with a product logo and an icon.",
  features: [],
  icon: () => <svg data-testid="logoed-icon" />,
  panels: [],
})

const environments = [
  { id: "env_prod", name: "production" },
  { id: "env_stage", name: "staging" },
]

function binding(
  name: string,
  config: Record<string, unknown>,
  environment = "env_prod",
  provider = "server"
) {
  return {
    id: `binding-${name}`,
    name,
    subject: "resource",
    provider,
    environment_id: environment,
    drift: "",
    drift_message: "",
    updated_at: "2026-10-21T12:00:00Z",
    config,
  }
}

const composed = [
  binding("app", {
    kind: "sample",
    provider: "server",
    composition: {
      kind: "sample",
      version: "1.4",
      dependencies: { postgres: { mode: "external", external: "ep_1" } },
    },
  }),
  binding("app--api", {
    kind: "sample",
    provider: "server",
    component: {
      kind: "sample",
      resource: "app",
      protection: "none",
      component: { name: "api", role: "process" },
    },
  }),
]

const databases = [
  binding(
    "db",
    {
      kind: "postgres",
      provider: "server",
      catalog: { id: "postgresql-17" },
    },
    "env_stage"
  ),
  binding(
    "other",
    {
      kind: "postgres",
      provider: "server",
      catalog: { id: "postgresql-17" },
    },
    "env_stage"
  ),
]

type Fixture = {
  bindings?: Record<string, unknown[]>
  status?: number
  projectEnvironments?: { id: string; name: string }[]
}

function serve({
  bindings = {},
  status = 200,
  projectEnvironments = environments,
}: Fixture) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    let body: unknown = {}
    if (url.pathname.endsWith("/projects/p"))
      body = { id: "p", name: "Shop", repositories: [] }
    if (url.pathname.endsWith("/environments"))
      body = { environments: projectEnvironments }
    if (url.pathname.endsWith("/bindings")) {
      if (status !== 200)
        return new Response(
          JSON.stringify({ message: "You can't read this project." }),
          { status, headers: { "Content-Type": "application/json" } }
        )
      body = {
        bindings: bindings[url.searchParams.get("environment") ?? ""] ?? [],
      }
    }
    if (url.pathname.endsWith("/catalog/kinds")) body = { kinds: [] }
    if (url.pathname.endsWith("/connections")) body = { connections: [] }
    if (url.pathname.endsWith("/fabriq/fabriq")) body = { reachable: false }
    if (url.pathname.endsWith("/tenants")) body = { tenants: [] }
    if (url.pathname.endsWith("/keys")) body = { keys: [] }
    if (url.pathname.endsWith("/service-backups")) body = { backups: [] }
    if (url.pathname.endsWith("/service-restores")) body = { restores: [] }
    if (url.pathname.endsWith("/backup-policies")) body = { policies: [] }
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    })
  })
}

function open(resource: string, environmentId: string) {
  return render(
    <ClientProvider client={forgeClient}>
      <CatalogService
        projectId="p"
        resource={resource}
        environmentId={environmentId}
      />
    </ClientProvider>
  )
}

async function tabNames() {
  const list = await screen.findByRole("tablist")
  return within(list)
    .getAllByRole("tab")
    .map((tab) => tab.textContent)
}

test("service page mounts the kind's panels as tabs after Overview", async () => {
  serve({ bindings: { production: composed } })
  open("app", "env_prod")
  expect(await tabNames()).toEqual(["Overview", "Routes"])
  await userEvent.click(screen.getByRole("tab", { name: "Routes" }))
  expect(
    await screen.findByText("Routes of app in env_prod with app--api")
  ).toBeInTheDocument()
})

test("panels follow their order and only show when they apply", async () => {
  serve({
    bindings: {
      production: [binding("lake", { kind: "ordered", provider: "server" })],
    },
  })
  open("lake", "env_prod")
  expect(await tabNames()).toEqual(["Overview", "Early", "Late"])
})

test("database resources keep the recovery panel", async () => {
  serve({ bindings: { staging: databases } })
  open("other", "env_stage")
  expect(await tabNames()).toEqual(["Overview", "Recovery"])
  await userEvent.click(screen.getByRole("tab", { name: "Recovery" }))
  expect(
    await screen.findByRole("combobox", { name: "Database" })
  ).toHaveTextContent("other")
  expect(
    screen.getByRole("combobox", { name: "Recovery environment" })
  ).toHaveTextContent("staging")
})

test("overview says what each component keeps", async () => {
  serve({ bindings: { production: composed } })
  open("app", "env_prod")
  const overview = await screen.findByRole("tabpanel")
  expect(
    await within(overview).findByText("Not backed up, nothing to restore")
  ).toBeInTheDocument()
  expect(
    within(overview).getByText(
      "Not managed by Loomplane: no backups or restores"
    )
  ).toBeInTheDocument()
  expect(within(overview).getByText("api")).toBeInTheDocument()
  expect(within(overview).getByText("postgres")).toBeInTheDocument()
})

test("unknown kind still renders Overview", async () => {
  serve({
    bindings: {
      production: [binding("cache", { kind: "mystery", provider: "server" })],
    },
  })
  open("cache", "env_prod")
  expect(await tabNames()).toEqual(["Overview"])
  expect(screen.getByRole("heading", { name: /cache/ })).toBeInTheDocument()
})

test("a resource that isn't deployed in the environment says so", async () => {
  serve({ bindings: { production: composed } })
  open("gone", "env_prod")
  expect(
    await screen.findByText("Nothing called gone runs in production")
  ).toBeInTheDocument()
  expect(screen.queryByRole("tablist")).not.toBeInTheDocument()
})

test("denied access and a failed request read differently", async () => {
  serve({ status: 403 })
  open("app", "env_prod")
  expect(
    await screen.findByText("You can't see this service")
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Retry" })
  ).not.toBeInTheDocument()
  cleanup()
  forgeClient.clear()
  serve({ status: 500 })
  open("app", "env_prod")
  expect(await screen.findByRole("button", { name: "Retry" })).toBeEnabled()
  expect(
    screen.queryByText("You can't see this service")
  ).not.toBeInTheDocument()
})

test("recovery stays off databases Loomplane doesn't back up there", async () => {
  serve({
    bindings: {
      production: [
        binding(
          "cache",
          {
            kind: "valkey",
            provider: "kubernetes",
            catalog: { id: "valkey-8.1-kubernetes" },
          },
          "env_prod",
          "kubernetes"
        ),
        binding("db", {
          kind: "postgres",
          provider: "server",
          catalog: { id: "postgresql-17" },
        }),
      ],
    },
  })
  open("cache", "env_prod")
  expect(await tabNames()).toEqual(["Overview"])
})

test("a project without environments says so", async () => {
  serve({ projectEnvironments: [] })
  open("app", "")
  expect(
    await screen.findByText("This project has no environments yet")
  ).toBeInTheDocument()
})

test("tabs show counts from useCount", async () => {
  serve({
    bindings: {
      production: [binding("edge", { kind: "counted", provider: "server" })],
    },
  })
  open("edge", "env_prod")
  expect(await tabNames()).toEqual([
    "Overview",
    "Routes 6",
    "Domains 3",
    "Discovery",
    "Settings",
  ])
  expect(screen.getByRole("tab", { name: "Routes 6" })).toBeInTheDocument()
})

test("actions render at the right of the header", async () => {
  serve({
    bindings: {
      production: [binding("edge", { kind: "counted", provider: "server" })],
    },
  })
  open("edge", "env_prod")
  const header = await screen.findByRole("banner")
  const action = within(header).getByRole("button", {
    name: "Open edge admin",
  })
  // After the environment and state, at the end of the header.
  const status = within(header).getByText("Deployed")
  expect(
    status.compareDocumentPosition(action) & Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  cleanup()
  forgeClient.clear()
  serve({ bindings: { production: composed } })
  open("app", "env_prod")
  expect(
    within(await screen.findByRole("banner")).queryByRole("button")
  ).not.toBeInTheDocument()
})

test("subtitle renders under the name", async () => {
  serve({
    bindings: {
      production: [binding("edge", { kind: "counted", provider: "server" })],
    },
  })
  open("edge", "env_prod")
  const header = await screen.findByRole("banner")
  const name = within(header).getByRole("heading", { level: 1 })
  const subtitle = within(header).getByText("Hosted gateway · server")
  expect(
    name.compareDocumentPosition(subtitle) & Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  // The subtitle says which instance it is, so the catalog blurb gives way.
  expect(
    within(header).queryByText("The kind's catalog blurb.")
  ).not.toBeInTheDocument()
})

test("icon is used when there is no product logo", async () => {
  serve({
    bindings: {
      production: [binding("edge", { kind: "counted", provider: "server" })],
    },
  })
  open("edge", "env_prod")
  const header = await screen.findByRole("banner")
  expect(within(header).getByTestId("counted-icon")).toBeInTheDocument()
  cleanup()
  forgeClient.clear()
  serve({
    bindings: {
      production: [binding("cache", { kind: "logoed", provider: "server" })],
    },
  })
  open("cache", "env_prod")
  const logoed = await screen.findByRole("banner")
  expect(within(logoed).queryByTestId("logoed-icon")).not.toBeInTheDocument()
  expect(within(logoed).getByTitle("Logoed")).toBeInTheDocument()
})

defineCatalogKind({
  kind: "running-state",
  title: "Running state",
  group: "data",
  blurb: "A service with a running-state label.",
  features: [],
  panels: [],
  deployedLabel: "Running",
})

test("a module's deployed label preserves drift warnings", async () => {
  for (const [drift, label] of [
    ["", "Running"],
    ["missing", "Deleted outside Loomplane"],
    ["changed", "Changed outside Loomplane"],
  ]) {
    serve({
      bindings: {
        production: [
          {
            ...binding("fabriq", { kind: "running-state", provider: "server" }),
            drift,
          },
        ],
      },
    })
    open("fabriq", "env_prod")
    expect(
      within(await screen.findByRole("banner")).getByText(label)
    ).toBeInTheDocument()
    cleanup()
    forgeClient.clear()
  }
})

function fabriqBindings(
  kind: "fabriq" | "fabriq-library",
  process: "server" | "worker" | undefined,
  drift = ""
) {
  const parent = binding("fabriq", {
    kind,
    provider: "server",
    composition: { kind, version: "1.9", dependencies: {} },
  })
  if (!process) return [parent]
  return [
    parent,
    {
      ...binding(`fabriq--${process}`, {
        kind,
        provider: "server",
        component: {
          kind,
          resource: "fabriq",
          protection: "none",
          component: { name: process, role: "process" },
        },
      }),
      drift,
    },
  ]
}

test("Fabriq puts the environment in its instance subtitle once", async () => {
  serve({ bindings: { production: fabriqBindings("fabriq", "server") } })
  open("fabriq", "env_prod")
  const header = await screen.findByRole("banner")
  expect(
    await within(header).findByText("Hosted Fabriq server · production")
  ).toBeVisible()
  expect(
    within(header).queryByText("production", { exact: true })
  ).not.toBeInTheDocument()
  expect(within(header).getByText("Running")).toBeVisible()
  expect(
    within(header).getByRole("button", { name: "Open Fabriq admin" })
  ).toBeVisible()
})

test.each([
  ["fabriq", "server"],
  ["fabriq-library", "worker"],
] as const)(
  "a %s header says Running only while its %s is deployed",
  async (kind, process) => {
    for (const [bindings, label] of [
      [fabriqBindings(kind, process), "Running"],
      [fabriqBindings(kind, undefined), "Not running"],
      [fabriqBindings(kind, process, "missing"), "Not running"],
    ] as const) {
      serve({ bindings: { production: bindings } })
      open("fabriq", "env_prod")
      const header = await screen.findByRole("banner")
      expect(await within(header).findByText(label)).toBeVisible()
      expect(
        within(header).queryByText(
          label === "Running" ? "Not running" : "Running"
        )
      ).toBeNull()
      cleanup()
      forgeClient.clear()
    }
  }
)

test("the default catalog header retains its environment badge", async () => {
  serve({ bindings: { production: composed } })
  open("app", "env_prod")
  const header = await screen.findByRole("banner")
  expect(within(header).getByText("production", { exact: true })).toBeVisible()
})

defineCatalogKind({
  kind: "preferred-panel",
  title: "Preferred panel",
  group: "data",
  blurb: "A kind with a preferred landing panel.",
  features: [],
  defaultPanel: "details",
  panels: [
    {
      id: "details",
      title: "Details",
      component: () => <p>Instance details</p>,
      when: (props) => props.config.provider === "server",
    },
  ],
})

test("Fabriq opens Tenants and keys and still lets you select Overview", async () => {
  serve({
    bindings: {
      production: [binding("fabriq", { kind: "fabriq", provider: "server" })],
    },
  })
  open("fabriq", "env_prod")
  expect(
    await screen.findByRole("tab", { name: "Tenants and keys", selected: true })
  ).toBeVisible()
  expect(
    within(screen.getByRole("tabpanel")).getByRole("heading", { name: "Stack" })
  ).toBeVisible()
  await userEvent.click(screen.getByRole("tab", { name: "Overview" }))
  expect(
    screen.getByRole("tab", { name: "Overview", selected: true })
  ).toBeVisible()
})

test.each([
  ["server", "Details"],
  ["kubernetes", "Overview"],
])(
  "the preferred panel resolves only when available on %s",
  async (provider, selected) => {
    serve({
      bindings: {
        production: [binding("service", { kind: "preferred-panel", provider })],
      },
    })
    open("service", "env_prod")
    expect(
      await screen.findByRole("tab", { name: selected, selected: true })
    ).toBeVisible()
    expect(screen.getByRole("tabpanel")).toBeVisible()
  }
)

test("changing resources selects the destination's default tab", async () => {
  serve({
    bindings: {
      production: [
        binding("service", { kind: "preferred-panel", provider: "server" }),
        ...composed,
      ],
    },
  })
  const view = open("service", "env_prod")
  expect(
    await screen.findByRole("tab", { name: "Details", selected: true })
  ).toBeVisible()
  view.rerender(
    <ClientProvider client={forgeClient}>
      <CatalogService projectId="p" resource="app" environmentId="env_prod" />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("tab", { name: "Overview", selected: true })
  ).toBeVisible()
  expect(screen.getByRole("tabpanel")).toBeVisible()
})

test("Fabriq library opens its worker panel and keeps the environment in the subtitle", async () => {
  serve({
    bindings: {
      production: [
        binding("fabriq", { kind: "fabriq-library", provider: "server" }),
      ],
    },
  })
  open("fabriq", "env_prod")
  expect(
    await screen.findByRole("tab", { name: "Library", selected: true })
  ).toBeVisible()
  const header = screen.getByRole("banner")
  expect(
    await within(header).findByText("Fabriq library · production")
  ).toBeVisible()
  expect(within(header).queryByText("production", { exact: true })).toBeNull()
  expect(screen.queryByRole("tab", { name: "Tenants and keys" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Open Fabriq admin" })).toBeNull()
})
