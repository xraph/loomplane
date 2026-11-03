import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import type { Environment, Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { ProjectDetail, Resources } from "@/pages/project-detail"

// What the workspace role may do; the tests that change it say so.
const workspace = vi.hoisted(() => ({
  can: (permission: string): boolean => permission !== "",
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: workspace.can }),
}))
vi.mock("@/components/shell", () => ({
  useShell: () => ({ openDeploy: () => undefined }),
}))
vi.mock("@/lib/use-runs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/use-runs")>()),
  useRuns: () => ({ runs: [] }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
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

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  workspace.can = () => true
})

const project = { id: "p", name: "Shop" } as Project
const environments = [
  { id: "env_prod", name: "production" },
  { id: "env_stage", name: "staging" },
] as Environment[]

const database = {
  id: "binding-db",
  name: "db",
  subject: "resource",
  provider: "server",
  environment_id: "env_prod",
  drift: "",
  config: { kind: "postgres", provider: "server", catalog: { id: "pg" } },
}

// statuses maps an environment name to the status its bindings answer with.
function serve(statuses: Record<string, number>) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost")
    const environment = url.searchParams.get("environment") ?? ""
    const status = statuses[environment] ?? 200
    if (status !== 200)
      return new Response(JSON.stringify({}), {
        status,
        headers: { "Content-Type": "application/json" },
      })
    return new Response(
      JSON.stringify({
        bindings: environment === "production" ? [database] : [],
      }),
      { headers: { "Content-Type": "application/json" } }
    )
  })
  render(
    <ClientProvider client={forgeClient}>
      <Resources project={project} environments={environments} />
    </ClientProvider>
  )
}

test("resource rows open their service page", async () => {
  serve({})
  const row = await screen.findByRole("link", { name: /db/ })
  expect(row).toHaveAttribute("href", "/projects/$id/catalog/$resource")
  expect(
    screen.queryByRole("button", { name: "Retry" })
  ).not.toBeInTheDocument()
})

test("an environment that failed to load says so beside the rows", async () => {
  serve({ staging: 500 })
  expect(await screen.findByRole("button", { name: "Retry" })).toBeEnabled()
  expect(screen.getByRole("link", { name: /db/ })).toBeInTheDocument()
})

test("an environment the role can't see says so without a retry", async () => {
  serve({ staging: 403 })
  expect(
    await screen.findByText("Your role can't see what runs in staging.")
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Retry" })
  ).not.toBeInTheDocument()
})

test("failures don't read as nothing deployed", async () => {
  serve({ production: 500, staging: 500 })
  expect(await screen.findByRole("button", { name: "Retry" })).toBeEnabled()
  expect(screen.getByRole("region", { name: "Resources" })).toBeInTheDocument()
})

test("nothing deployed shows nothing once every environment answered", async () => {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify({ bindings: [] }), {
        headers: { "Content-Type": "application/json" },
      })
  )
  render(
    <ClientProvider client={forgeClient}>
      <Resources project={project} environments={environments} />
    </ClientProvider>
  )
  expect(
    screen.getByRole("status", { name: "Loading resources" })
  ).toBeInTheDocument()
  await vi.waitFor(() =>
    expect(
      screen.queryByRole("status", { name: "Loading resources" })
    ).not.toBeInTheDocument()
  )
  expect(
    screen.queryByRole("region", { name: "Resources" })
  ).not.toBeInTheDocument()
})

test("a resource shows its state in every environment", async () => {
  serve({})
  await screen.findByRole("link", { name: /db/ })
  const table = screen.getByRole("table", { name: "Resources" })
  expect(table).toHaveTextContent("Live")
  expect(table).toHaveTextContent("Not deployed")
  expect(screen.getByRole("link", { name: "Open" })).toHaveAttribute(
    "href",
    "/projects/$id/catalog/$resource"
  )
})

test("old and unknown tabs open the page somewhere it has", async () => {
  const { Route } = await import("@/routes/projects.$id")
  const validate = Route.options.validateSearch as (
    search: Record<string, unknown>
  ) => Record<string, unknown>
  expect(validate({ tab: "graph" })).toEqual({ tab: "overview" })
  expect(validate({ tab: "deploy" })).toEqual({ tab: "deployments" })
  expect(validate({ tab: "variables", environment: "staging" })).toEqual({
    tab: "variables",
    environment: "staging",
  })
  expect(validate({ tab: "nonsense" })).toEqual({})
})

test("a team grant on the project shows its buttons", async () => {
  // The workspace role can't deploy; a team gives this project the right.
  workspace.can = (permission) => permission !== "deployment:create"
  const asked: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    asked.push(path)
    if (path === "/api/v1/projects/p")
      return Response.json({
        id: "p",
        name: "Shop",
        status: "ready",
        repositories: [],
      })
    if (path === "/api/v1/projects/p/permissions")
      return Response.json({ permissions: ["deployment:create"] })
    return Response.json({ message: "no" }, { status: 404 })
  })
  render(
    <ClientProvider client={forgeClient}>
      <ProjectDetail id="p" />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("link", { name: "Deploy" })
  ).toBeInTheDocument()
  await waitFor(() =>
    expect(asked.filter((p) => p.endsWith("/permissions"))).toHaveLength(1)
  )
})

test("without the team grant the button stays hidden", async () => {
  workspace.can = (permission) => permission !== "deployment:create"
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path === "/api/v1/projects/p")
      return Response.json({
        id: "p",
        name: "Shop",
        status: "ready",
        repositories: [],
      })
    if (path === "/api/v1/projects/p/permissions")
      return Response.json({ permissions: ["project:read"] })
    return Response.json({ message: "no" }, { status: 404 })
  })
  render(
    <ClientProvider client={forgeClient}>
      <ProjectDetail id="p" />
    </ClientProvider>
  )
  await screen.findByRole("heading", { name: /Shop/ })
  await waitFor(() =>
    expect(screen.queryByRole("link", { name: "Deploy" })).toBeNull()
  )
})
