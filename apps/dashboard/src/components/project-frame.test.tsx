import { ClientProvider } from "@forge-go/client-react"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ProjectFrame } from "@/components/project-frame"
import type { Run } from "@/lib/runs"
import { ProjectPage } from "@/test-fixtures/project-page"

const state = vi.hoisted(() => ({ runs: [] as unknown[] }))

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/components/shell", () => ({
  useShell: () => ({ openDeploy: () => undefined }),
  useAgentDock: () => null,
}))
vi.mock("@/lib/use-runs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/use-runs")>()),
  useRuns: () => ({ runs: state.runs }),
}))
// Links say where they go, with their parameters and search filled in.
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    params,
    search,
    ...rest
  }: {
    children: React.ReactNode
    to: string
    params?: Record<string, string>
    search?: Record<string, string | undefined>
  } & React.AnchorHTMLAttributes<HTMLAnchorElement>) => {
    const path = to.replace(/\$(\w+)/g, (_, k: string) => params?.[k] ?? k)
    const query = new URLSearchParams(
      Object.entries(search ?? {}).flatMap(([k, v]) => (v ? [[k, v]] : []))
    ).toString()
    return (
      <a {...rest} href={query ? `${path}?${query}` : path}>
        {children}
      </a>
    )
  },
  useNavigate: () => () => undefined,
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  state.runs = []
})

const environments = [
  { id: "env_prod", name: "production", kind: "production" },
  { id: "env_stage", name: "staging", kind: "staging" },
]

const graph = {
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
      ports: [8080],
    },
  ],
  packages: [],
  resources: [],
  edges: [],
  workspaces: [],
  build_systems: [],
  notes: [],
}

const web = {
  id: "b_web",
  name: "web",
  subject: "service",
  provider: "server",
  commit_sha: "e7163cd0000",
  drift: "",
}

const db = {
  id: "b_db",
  name: "db",
  subject: "resource",
  provider: "server",
  drift: "",
  config: { kind: "postgres", provider: "server", catalog: { id: "pg" } },
}

type Fixture = {
  repositories?: string[]
  environments?: typeof environments
  /** What runs in each environment, by name. */
  bindings?: Record<string, unknown[]>
  graph?: typeof graph
}

function serve(fixture: Fixture = {}) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    const path = url.pathname
    const json = (body: unknown) => Response.json(body)
    if (path === "/api/v1/projects/p")
      return json({
        id: "p",
        name: "Shop",
        status: "ready",
        repositories: fixture.repositories ?? [],
        exclusions: [],
        created_at: "2026-10-03T00:00:00Z",
        updated_at: "2026-10-03T00:00:00Z",
      })
    if (path === "/api/v1/projects") return json({ projects: [] })
    if (path.endsWith("/environments"))
      return json({ environments: fixture.environments ?? environments })
    if (path.endsWith("/bindings"))
      return json({
        bindings:
          fixture.bindings?.[url.searchParams.get("environment") ?? ""] ?? [],
      })
    if (path.endsWith("/graph"))
      return json({
        project_id: "p",
        excluded: [],
        sources: [],
        ...(fixture.graph ? { graph: fixture.graph } : {}),
      })
    if (path.endsWith("/analyses")) return json({ analyses: [] })
    if (path.endsWith("/variables")) return json({ variables: [] })
    if (path.endsWith("/plans")) return json({ plans: [] })
    if (path.endsWith("/deployments")) return json({ deployments: [] })
    if (path.endsWith("/sources")) return json({ sources: [] })
    if (path.endsWith("/domain-routes")) return json({ routes: [] })
    if (path.endsWith("/provider-environments"))
      return json({ provider_environments: [] })
    return json({})
  })
}

function wrap(children: React.ReactNode) {
  return render(
    <ClientProvider client={forgeClient}>{children}</ClientProvider>
  )
}

test("a new project lists the steps to its first deploy, the next one first", async () => {
  serve()
  wrap(<ProjectPage id="p" />)
  const steps = await screen.findByRole("region", { name: "Set up" })
  // The environments are there already; the repository is next.
  expect(steps).toHaveTextContent("1 of 5 done")
  const items = within(steps).getAllByRole("listitem")
  const current = items.find((i) => i.getAttribute("aria-current") === "step")
  expect(current).toHaveTextContent("Connect a repository")
  expect(items[3]).toHaveTextContent("Add an environment (done)")
  // Only the next step says what it's for.
  expect(steps).not.toHaveTextContent("waits for your approval")
})

test("once something runs, the Overview shows what runs where instead of the steps", async () => {
  serve({
    repositories: ["acme/shop"],
    graph,
    bindings: { production: [web, db] },
  })
  wrap(<ProjectPage id="p" />)
  const table = await screen.findByRole("table", { name: "What runs where" })
  // The rows fill in as each environment answers.
  await within(table).findByRole("link", { name: "db" })
  expect(screen.queryByRole("region", { name: "Set up" })).toBeNull()
  const rows = within(table).getAllByRole("row")
  expect(rows[0]).toHaveTextContent("production")
  expect(rows[0]).toHaveTextContent("staging")
  // The service, by the commit that runs in each environment.
  const service = rows.find((r) => r.textContent?.includes("web"))!
  expect(
    within(service).getByRole("link", { name: /e7163cd/ })
  ).toHaveAttribute("href", "/projects/p/services/web?environment=production")
  // Nothing runs in staging, once staging answers.
  expect(await within(service).findByText("Not deployed")).toBeInTheDocument()
  // The database it uses, opening on its own page in that environment.
  const database = rows.find((r) => r.textContent?.includes("db"))!
  expect(within(database).getByRole("link", { name: "db" })).toHaveAttribute(
    "href",
    "/projects/p/catalog/db?environment=env_prod"
  )
})

test("a plan waiting for approval is one item to review", async () => {
  state.runs = [
    {
      id: "plan_9",
      projectId: "p",
      environment: "staging",
      status: "waiting",
      createdAt: "2026-11-01T00:00:00Z",
    } as unknown as Run,
  ]
  serve({ repositories: ["acme/shop"], graph, bindings: { production: [web] } })
  wrap(<ProjectPage id="p" />)
  const attention = await screen.findByRole("region", {
    name: "Needs attention",
  })
  expect(attention).toHaveTextContent("1 plan waits for approval")
  expect(attention).toHaveTextContent("In staging.")
  expect(
    within(attention).getByRole("button", { name: "Review" })
  ).toHaveAttribute("href", "/projects/p/deployments/plan_9")
})

test("a page under the project marks its view and offers the environment", async () => {
  serve()
  wrap(
    <ProjectFrame
      id="p"
      environment="staging"
      place={{ tab: "services", environmentScoped: true }}
    >
      <p>A service</p>
    </ProjectFrame>
  )
  expect(await screen.findByText("A service")).toBeInTheDocument()
  const nav = screen.getByRole("navigation", { name: "Project views" })
  expect(within(nav).getByRole("link", { name: "Services" })).toHaveAttribute(
    "aria-current",
    "page"
  )
  // The project is named smaller, above the page's own heading.
  expect(screen.queryByRole("heading", { level: 1 })).toBeNull()
  expect(screen.getByRole("link", { name: "Shop" })).toHaveAttribute(
    "href",
    "/projects/p"
  )
  expect(
    await screen.findByRole("combobox", { name: "Environment" })
  ).toHaveTextContent("staging")
})

test("views that show every environment offer no choice of one", async () => {
  serve()
  wrap(<ProjectPage id="p" tab="services" />)
  await screen.findByRole("heading", { level: 1, name: "Shop" })
  expect(screen.queryByRole("combobox", { name: "Environment" })).toBeNull()
})

test("Deploy's menu deploys to one environment", async () => {
  serve()
  wrap(<ProjectPage id="p" />)
  await userEvent.click(
    await screen.findByRole("button", { name: "More ways to deploy" })
  )
  expect(
    await screen.findByRole("menuitem", { name: "production" })
  ).toHaveAttribute("href", "/deployments/new?project=p&environment=production")
  expect(screen.getByRole("menuitem", { name: "staging" })).toHaveAttribute(
    "href",
    "/deployments/new?project=p&environment=staging"
  )
})
