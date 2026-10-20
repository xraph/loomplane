import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ServiceCatalog } from "@/pages/service-catalog"

const permissions = vi.hoisted(() => ({ allowed: true }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => permissions.allowed }),
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
  permissions.allowed = true
})
function mount() {
  return render(
    <ClientProvider client={forgeClient}>
      <ServiceCatalog />
    </ClientProvider>
  )
}
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}
it("keeps empty projects distinct from a failed read and offers retry", async () => {
  let failed = true
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) =>
    String(input).endsWith("/projects")
      ? response(
          failed ? { error: "Projects unavailable" } : { projects: [] },
          failed ? 503 : 200
        )
      : response({ connections: [] })
  )
  mount()
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Projects unavailable"
  )
  expect(screen.queryByText("No service projects yet")).not.toBeInTheDocument()
  failed = false
  await userEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("No service projects yet")).toBeInTheDocument()
  expect(screen.getByText("Connect a Docker server")).toBeInTheDocument()
  expect(
    screen.getByRole("combobox", { name: "Database engine" })
  ).toBeEnabled()
})
it("does not request recovery data or show deployment controls without grants", async () => {
  permissions.allowed = false
  const requests: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input)
    requests.push(url)
    return response(
      url.endsWith("/projects")
        ? { projects: [{ id: "p", name: "Private database" }] }
        : { connections: [] }
    )
  })
  mount()
  expect(
    await screen.findByText(
      "You need service backup read access to view recovery points."
    )
  ).toBeInTheDocument()
  expect(requests.some((url) => url.includes("service-backups"))).toBe(false)
  expect(
    screen.queryByRole("button", { name: "Deploy PostgreSQL" })
  ).not.toBeInTheDocument()
})

it("selects the database engine in the shared deployment form", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) =>
    response(
      String(input).endsWith("/projects")
        ? { projects: [] }
        : {
            connections: [
              {
                id: "target",
                name: "Docker acceptance",
                cloud: "server",
                status: "ready",
                details: { architecture: "arm64" },
              },
            ],
          }
    )
  )
  mount()
  expect(
    await screen.findByRole("button", { name: "Deploy PostgreSQL" })
  ).toBeEnabled()
  await userEvent.click(
    screen.getByRole("combobox", { name: "Database engine" })
  )
  await userEvent.click(screen.getByRole("option", { name: "MySQL 8.4" }))
  expect(screen.getByRole("button", { name: "Deploy MySQL" })).toBeEnabled()
  expect(screen.getByLabelText("Project name")).toHaveValue("MySQL")
})
