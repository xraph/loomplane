import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import type { Environment, Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { Resources } from "@/pages/project-detail"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
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
