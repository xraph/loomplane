import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ProviderResources } from "./provider-resources"

let denied = false
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => !denied }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  useNavigate: () => vi.fn(),
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/lib/use-runs", () => ({
  useNow: () => new Date("2026-10-31T12:15:00Z").getTime(),
  useRunning: () => ({ bindings: [], loaded: true }),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  denied = false
})
function fixture(failed = false) {
  const calls: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    calls.push(path)
    if (path.endsWith("resource-syncs"))
      return Response.json({ message: "Account sync failed" }, { status: 503 })
    if (path.endsWith("provider-resources"))
      return failed
        ? Response.json({ message: "Inventory access denied" }, { status: 403 })
        : Response.json({
            resources: [
              {
                id: "r",
                name: "orders",
                provider: "digitalocean",
                connection_id: "c",
                account_identity: "Production",
                kind: "postgres",
                region: "nyc3",
                status: "online",
                ownership: "external",
                observed_at: "2026-10-31T12:00:00Z",
                endpoint_id: "",
                external_id: "db",
                revision: 1,
                consumers: [
                  {
                    id: "consumer",
                    binding_id: "binding",
                    project_id: "p",
                    environment_id: "env",
                    name: "orders-api",
                  },
                ],
              },
            ],
            syncs: [
              {
                id: "s",
                connection_id: "c",
                status: "failed",
                error: "Read permission denied",
                started_at: "2026-10-31T12:00:00Z",
              },
            ],
            supported_providers: ["digitalocean"],
          })
    if (path.endsWith("connections"))
      return Response.json({
        connections: [
          {
            id: "c",
            name: "Production",
            cloud: "digitalocean",
            status: "ready",
          },
          { id: "x", name: "Unsupported", cloud: "future", status: "ready" },
        ],
      })
    if (path.endsWith("providers")) return Response.json({ providers: [] })
    throw new Error(path)
  })
  return calls
}
test("failed discovery retains inventory and exposes the account retry", async () => {
  fixture()
  render(
    <ClientProvider client={forgeClient}>
      <ProviderResources />
    </ClientProvider>
  )
  expect(
    await screen.findByRole("button", { name: "orders" })
  ).toBeInTheDocument()
  expect(screen.getByRole("alert")).toHaveTextContent("Read permission denied")
  await userEvent.click(screen.getByRole("button", { name: "Review syncs" }))
  expect(
    screen.getByRole("button", { name: "Sync Production" })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Sync Unsupported" })
  ).not.toBeInTheDocument()
})
test("inventory denial never renders the empty inventory action", async () => {
  fixture(true)
  render(
    <ClientProvider client={forgeClient}>
      <ProviderResources />
    </ClientProvider>
  )
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Inventory access denied"
  )
  expect(screen.queryByText("Sync your first account")).not.toBeInTheDocument()
})
test("without inventory read permission no account facts are requested", () => {
  denied = true
  const calls = fixture()
  render(
    <ClientProvider client={forgeClient}>
      <ProviderResources />
    </ClientProvider>
  )
  expect(screen.getByText("Resource access is restricted")).toBeInTheDocument()
  expect(calls).toEqual([])
})
test("failed account sync refreshes retained inventory and durable failure history", async () => {
  const calls = fixture()
  render(
    <ClientProvider client={forgeClient}>
      <ProviderResources />
    </ClientProvider>
  )
  await screen.findByRole("button", { name: "orders" })
  await userEvent.click(screen.getByRole("button", { name: "Sync accounts" }))
  await userEvent.click(screen.getByRole("button", { name: "Sync Production" }))
  expect(await screen.findByText(/Account sync failed/)).toBeInTheDocument()
  expect(
    calls.filter((path) => path === "/api/v1/provider-resources").length
  ).toBeGreaterThan(1)
})

test("usage comes from authorized server consumer relationships", async () => {
  const calls = fixture()
  render(
    <ClientProvider client={forgeClient}>
      <ProviderResources />
    </ClientProvider>
  )
  expect(await screen.findByText("1 deployment")).toBeInTheDocument()
  expect(
    calls.some(
      (path) => path.endsWith("/projects") || path.endsWith("/bindings")
    )
  ).toBe(false)
})
