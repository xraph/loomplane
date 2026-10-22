import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
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

const environments = [
  { id: "env_prod", name: "production" },
  { id: "env_stage", name: "staging" },
]

function binding(
  name: string,
  config: Record<string, unknown>,
  environment = "env_prod"
) {
  return {
    id: `binding-${name}`,
    name,
    subject: "resource",
    provider: "server",
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
}

function serve({ bindings = {}, status = 200 }: Fixture) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    let body: unknown = {}
    if (url.pathname.endsWith("/projects/p"))
      body = { id: "p", name: "Shop", repositories: [] }
    if (url.pathname.endsWith("/environments")) body = { environments }
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
    if (url.pathname.endsWith("/service-backups")) body = { backups: [] }
    if (url.pathname.endsWith("/service-restores")) body = { restores: [] }
    if (url.pathname.endsWith("/backup-policies")) body = { policies: [] }
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    })
  })
}

function open(resource: string, environmentId: string) {
  render(
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
