import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { Services } from "./services"
import { ServicesCatalog } from "./services-catalog"

let denied = false
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ session: { orgId: "org" }, can: () => !denied }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  useNavigate: () => vi.fn(),
  Link: ({ children, to }: { children?: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/lib/use-runs", () => ({
  useNow: () => new Date("2026-10-31T12:15:00Z").getTime(),
}))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  denied = false
})

function fixture(
  projects: Record<string, unknown>[] = [{ id: "p", name: "shop" }]
) {
  const calls: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    calls.push(path)
    if (path.endsWith("/projects")) return Response.json({ projects })
    if (path.endsWith("/catalog/kinds")) return Response.json({ kinds: [] })
    if (path.endsWith("/providers"))
      return Response.json({
        providers: [
          {
            name: "cloudflare",
            title: "Cloudflare",
            cloud: "cloudflare",
            available: true,
            description: "",
            model: "",
            scaling: false,
            provisions: [
              { kind: "cloudflare-d1", creates: "Cloudflare D1 database" },
            ],
          },
        ],
      })
    if (path.endsWith("/connections"))
      return Response.json({
        connections: [
          { id: "c", name: "Edge", cloud: "cloudflare", status: "ready" },
        ],
      })
    if (path.endsWith("/provider-resources"))
      return Response.json({
        resources: [
          {
            id: "r",
            name: "orders-db",
            provider: "cloudflare",
            connection_id: "c",
            account_identity: "Edge",
            kind: "cloudflare-d1",
            region: "weur",
            status: "online",
            ownership: "external",
            observed_at: "2026-10-31T12:10:00Z",
            endpoint_id: "",
            external_id: "d1",
            revision: 1,
          },
        ],
        syncs: [],
        supported_providers: ["cloudflare"],
      })
    if (path.includes("/environments"))
      return Response.json({ environments: [{ name: "production" }] })
    if (path.includes("/manifest"))
      return Response.json({ manifest: { version: 1, services: {} } })
    return Response.json({}, { status: 404 })
  })
  return calls
}

function show() {
  render(
    <ClientProvider client={forgeClient}>
      <Services />
    </ClientProvider>
  )
}

function showCatalog(group?: "managed") {
  render(
    <ClientProvider client={forgeClient}>
      <ServicesCatalog initialGroup={group} />
    </ClientProvider>
  )
}

test("provider resources show under Services with the provider that manages them", async () => {
  fixture()
  show()
  const row = await screen.findByRole("button", { name: /orders-db/ })
  expect(row).toHaveTextContent("Cloudflare D1 database")
  expect(row).toHaveTextContent("Managed by Cloudflare")
  expect(
    screen.getByRole("link", { name: "All provider resources" })
  ).toHaveAttribute("href", "/integrations/resources")
})

test("Services lists what runs and sends adding to the catalog page", async () => {
  fixture()
  show()
  expect(await screen.findByText("None yet")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Browse catalog" })).toHaveAttribute(
    "href",
    "/services/catalog"
  )
  expect(screen.queryByRole("radio")).not.toBeInTheDocument()
  expect(screen.queryByText("Deploy PostgreSQL")).not.toBeInTheDocument()
})

test("Run by Loomplane lists service projects by kind, not by icon", async () => {
  fixture([
    { id: "app", name: "Shop", icon: "database", kind: "app" },
    {
      id: "svc",
      name: "Orders DB",
      icon: "rocket",
      kind: "service",
      description: "Standalone PostgreSQL service",
    },
  ])
  show()
  expect(await screen.findByText("1 deployed")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: /Orders DB/ })).toHaveAttribute(
    "href",
    "/projects/$id"
  )
  expect(screen.queryByRole("link", { name: /Shop/ })).not.toBeInTheDocument()
})

test("the catalog lists provider one-click offerings after what Loomplane runs", async () => {
  fixture()
  showCatalog()
  const catalog = screen.getByRole("region", { name: "Catalog" })
  const groups = await within(catalog).findAllByRole("heading", { level: 2 })
  expect(groups.map((h) => h.textContent)).toEqual([
    "Run by Loomplane",
    "One click from your providers",
  ])
  const card = (
    await within(catalog).findByText("Cloudflare D1 database", {
      selector: "span",
    })
  ).closest("article")!
  expect(card).toHaveTextContent("Managed by Cloudflare")
  expect(
    within(catalog).getByRole("link", { name: "Deploy PostgreSQL" })
  ).toBeInTheDocument()
})

test("a link into Managed opens only the provider offerings, one click from review", async () => {
  fixture()
  showCatalog("managed")
  const card = (
    await screen.findByText("Cloudflare D1 database", { selector: "span" })
  ).closest("article")!
  expect(screen.getByRole("radio", { name: /Managed/ })).toBeChecked()
  expect(screen.queryByText("Run by Loomplane")).not.toBeInTheDocument()
  expect(screen.queryByText("Deploy PostgreSQL")).not.toBeInTheDocument()
  await userEvent.click(
    within(card).getByRole("button", { name: "Create with Cloudflare" })
  )
  expect(
    await screen.findByRole("dialog", {
      name: "Create a provider-managed resource",
    })
  ).toBeInTheDocument()
  expect(
    await screen.findByRole("combobox", { name: "Resource kind" })
  ).toHaveTextContent("cloudflare-d1")
})

test("without inventory access the Services page never asks for provider resources", async () => {
  denied = true
  const calls = fixture()
  show()
  await screen.findByText("None yet")
  expect(calls).not.toContain("/api/v1/provider-resources")
})
