import { ClientProvider } from "@forge-go/client-react"
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"
import {
  host,
  overviewWith,
  registration,
  stubFetch,
  zero,
  inTwelveDays,
  zoneWith,
} from "@/test-fixtures/domains"
import { Domains } from "./domains"

const access = vi.hoisted(() => ({ allowed: true }))
// One function, as the router's is, so effects that use it run once.
const navigation = vi.hoisted(() => {
  const calls: unknown[] = []
  return { calls, navigate: (to: unknown) => calls.push(to) }
})
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: (permission: string) =>
      permission === "organization_cloud:manage" ? access.allowed : true,
  }),
}))
vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))
vi.mock("@/lib/router", () => ({
  usePath: () => "/domains",
  useNavigate: () => navigation.navigate,
  Link: ({
    children,
    to,
    params,
    ...props
  }: {
    children?: ReactNode
    to: string
    params?: Record<string, string>
    "data-active"?: string
  }) => (
    <a
      href={to.replace(/\$(\w+)/g, (_, key: string) => params?.[key] ?? "")}
      data-active={props["data-active"]}
    >
      {children}
    </a>
  ),
}))
// What the sidebar reads besides the router and the organization.
vi.mock("@tanstack/react-router", () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string; search: object } }) => unknown
  }) => select({ location: { pathname: "/domains", search: {} } }),
}))
vi.mock("@authsome/ui-react", () => ({
  useAuth: () => ({ user: null, signOut: async () => {} }),
}))
vi.mock("@/components/shell", () => ({
  useShell: () => ({ openNewProject: () => {}, openShortcuts: () => {} }),
}))
vi.mock("@/lib/use-runs", () => ({ useRuns: () => ({ runs: [] }) }))

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.allowed = true
  navigation.calls.length = 0
})

function Wrapped({ children }: { children: ReactNode }) {
  return <ClientProvider client={forgeClient}>{children}</ClientProvider>
}

it("filters to domains that need attention", async () => {
  const ready = zoneWith("acme.dev", [
    {
      hostname: "api.acme.dev",
      runs_on: "web-1",
      backend: "server",
      dns: "ready",
      certificate: "ready",
      route: "ready",
    },
  ])
  stubFetch({
    "/api/v1/domains": overviewWith(
      ready,
      zoneWith("docs-example.org", [], { external: true })
    ),
  })
  const picks: unknown[] = []
  const { unmount } = render(
    <Wrapped>
      <Domains onSearch={(s) => picks.push(s)} />
    </Wrapped>
  )
  const filters = await screen.findByRole("radiogroup", { name: "Show" })
  await screen.findByRole("region", { name: "docs-example.org" })
  const attention = within(filters).getByRole("radio", {
    name: /Needs attention/,
  })
  expect(attention.textContent).toBe("Needs attention 1")
  expect(within(filters).getByRole("radio", { name: /All/ }).textContent).toBe(
    "All 2"
  )
  await userEvent.click(attention)
  expect(picks).toEqual([{ show: "attention" }])
  unmount()

  // The choice lives in the search params.
  render(
    <Wrapped>
      <Domains search={{ show: "attention" }} onSearch={() => {}} />
    </Wrapped>
  )
  expect(
    await screen.findByRole("region", { name: "docs-example.org" })
  ).toBeTruthy()
  expect(screen.queryByRole("region", { name: "acme.dev" })).toBeNull()
  // The card rolls the domain's state up; its own pill is on its page.
  expect(screen.getByText("Verifying")).toBeTruthy()
  cleanup()
  forgeClient.clear()

  stubFetch({ "/api/v1/domains": overviewWith(ready) })
  render(
    <Wrapped>
      <Domains search={{ show: "attention" }} onSearch={() => {}} />
    </Wrapped>
  )
  expect(await screen.findByText("Nothing needs attention")).toBeTruthy()
  expect(
    screen.getByText(
      "Every domain is verified and every certificate is current."
    )
  ).toBeTruthy()
})

it("turns auto-renew off from the renewal banner", async () => {
  const day = new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: "UTC",
  }).format(new Date(inTwelveDays))
  const writes = stubFetch({
    "/api/v1/domains": {
      zones: [zoneWith("acme.shop", [], { registration: "reg" })],
      registrations: [registration()],
    },
    "POST /api/v1/domain-registrations/reg/auto-renew": () =>
      Response.json(registration({ auto_renew: false, revision: 4 })),
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  const banner = await screen.findByText(
    `acme.shop renews on ${day} for USD 10.11, charged to your Cloudflare account. Auto-renew is on.`
  )
  expect(
    screen.getByText(`Registered through Loomplane · renews ${day}`)
  ).toBeTruthy()
  await userEvent.click(
    screen.getByRole("button", { name: "Turn off auto-renew" })
  )
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/domain-registrations/reg/auto-renew",
        body: { auto_renew: false, revision: 3 },
      },
    ])
  )
  expect(
    await screen.findByText(
      `Auto-renew is off for acme.shop. It expires on ${day} unless you renew it.`
    )
  ).toBeTruthy()
  expect(banner.isConnected).toBe(true)
  expect(
    screen.getByRole("button", { name: "Turn auto-renew back on" })
  ).toBeTruthy()
})

it("focuses the banner from the notice link without changing anything", async () => {
  const writes = stubFetch({
    "/api/v1/domains": {
      zones: [zoneWith("acme.shop", [], { registration: "reg" })],
      // Renews later than the 30-day window: the link alone shows it.
      registrations: [
        registration({
          domain_expires_at: new Date(Date.now() + 90 * 86400000).toISOString(),
        }),
      ],
    },
  })
  const scroll = vi.fn()
  Element.prototype.scrollIntoView = scroll
  render(
    <Wrapped>
      <Domains
        search={{ registration: "reg", action: "auto-renew-off" }}
        onSearch={() => {}}
      />
    </Wrapped>
  )
  const button = await screen.findByRole("button", {
    name: "Turn off auto-renew",
  })
  await waitFor(() => expect(document.activeElement).toBe(button))
  expect(scroll).toHaveBeenCalled()
  expect(screen.getByRole("status").textContent).toMatch(/Auto-renew is on\./)
  expect(writes).toEqual([])
  delete (Element.prototype as Partial<Element>).scrollIntoView
})

it("brings a Cloudflare zone through the inline panel", async () => {
  let zones: unknown[] = []
  const writes = stubFetch({
    "/api/v1/domains": () => Response.json({ zones, registrations: [] }),
    "POST /api/v1/dns-zones": () => {
      zones = [zoneWith("acme.dev")]
      return Response.json((zones[0] as { zone: unknown }).zone, {
        status: 201,
      })
    },
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  expect(await screen.findByText("No domains yet")).toBeTruthy()
  const toggle = screen.getByRole("button", { name: "Bring a domain" })
  expect(toggle.getAttribute("aria-expanded")).toBe("false")
  await userEvent.click(toggle)
  expect(toggle.getAttribute("aria-expanded")).toBe("true")
  const panel = screen.getByRole("region", { name: "Bring a domain" })
  await userEvent.type(
    within(panel).getByRole("textbox", { name: "Domain you own" }),
    "acme.dev"
  )
  await userEvent.click(
    within(panel).getByRole("button", { name: /Cloudflare/ })
  )
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/dns-zones",
        body: {
          name: "acme.dev",
          mode: "managed",
          connection_id: "cf",
          project_ids: [],
        },
      },
    ])
  )
  expect(await screen.findByRole("region", { name: "acme.dev" })).toBeTruthy()
  expect(screen.queryByRole("region", { name: "Bring a domain" })).toBeNull()
})

it("opens the new domain's page after bringing it", async () => {
  let zones: unknown[] = []
  stubFetch({
    "/api/v1/domains": () => Response.json({ zones, registrations: [] }),
    "POST /api/v1/dns-zones": () => {
      zones = [zoneWith("docs-example.org", [], { external: true })]
      return Response.json((zones[0] as { zone: unknown }).zone, {
        status: 201,
      })
    },
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  await screen.findByText("No domains yet")
  await userEvent.click(screen.getByRole("button", { name: "Bring a domain" }))
  const panel = screen.getByRole("region", { name: "Bring a domain" })
  await userEvent.type(
    within(panel).getByRole("textbox", { name: "Domain you own" }),
    "docs-example.org"
  )
  await userEvent.click(
    within(panel).getByRole("button", { name: "Verify with TXT" })
  )
  // Its TXT instructions live on its page.
  await waitFor(() =>
    expect(navigation.calls).toEqual([
      { to: "/domains/$zoneId", params: { zoneId: "zone-docs-example.org" } },
    ])
  )
})

it("says a name under a domain is its subdomain before adding it", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(zoneWith("loomplane.com")),
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  await screen.findByRole("region", { name: "loomplane.com" })
  await userEvent.click(screen.getByRole("button", { name: "Bring a domain" }))
  const panel = screen.getByRole("region", { name: "Bring a domain" })
  const field = within(panel).getByRole("textbox", { name: "Domain you own" })
  const note =
    "app.loomplane.com is a subdomain of loomplane.com, so it uses loomplane.com's verification."
  await userEvent.type(field, "app.loomplane.com")
  expect(within(panel).getByText(note)).toBeTruthy()
  // Whole labels only, and never the domain itself.
  for (const name of ["notloomplane.com", "loomplane.com"]) {
    await userEvent.clear(field)
    await userEvent.type(field, name)
    expect(within(panel).queryByText(/is a subdomain of/)).toBeNull()
  }
})

it("groups subdomains inside their domain", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("x.app.loomplane.com", [], {
        parent: "app.loomplane.com",
        proofZone: "loomplane.com",
      }),
      zoneWith(
        "app.loomplane.com",
        [host("app.loomplane.com"), host("api.app.loomplane.com")],
        {
          parent: "loomplane.com",
          proofZone: "loomplane.com",
          subdomains: ["x.app.loomplane.com"],
        }
      ),
      zoneWith("loomplane.com", [host("loomplane.com")], {
        subdomains: ["app.loomplane.com", "x.app.loomplane.com"],
      })
    ),
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "loomplane.com" })
  expect(screen.getAllByRole("region")).toHaveLength(1)
  expect(screen.getByText("1 domain · 3 hostnames in use")).toBeTruthy()
  expect(
    within(screen.getByRole("radiogroup", { name: "Show" })).getByRole(
      "radio",
      { name: /All/ }
    ).textContent
  ).toBe("All 1")

  const header = within(card).getByRole("link", { name: /^loomplane\.com/ })
  expect(header.getAttribute("href")).toBe("/domains/zone-loomplane.com")
  expect(within(header).getByText("Registered elsewhere")).toBeTruthy()
  expect(within(header).getByText("Cloudflare")).toBeTruthy()
  expect(within(header).getByText("Ready")).toBeTruthy()
  expect(await within(header).findByText("Granted to acme-shop")).toBeTruthy()

  const subdomains = within(
    within(card).getByRole("list", { name: "Subdomains" })
  ).getAllByRole("link")
  expect(subdomains.map((a) => a.getAttribute("href"))).toEqual([
    "/domains/zone-app.loomplane.com",
    "/domains/zone-x.app.loomplane.com",
  ])
  const [app, x] = subdomains
  expect(within(app).getByText("app.loomplane.com")).toBeTruthy()
  expect(within(app).getByText("2 hostnames")).toBeTruthy()
  expect(within(app).getByText("Granted to acme-shop")).toBeTruthy()
  expect(within(app).getByText("Managed by Cloudflare")).toBeTruthy()
  expect(within(x).getByText("x.app.loomplane.com")).toBeTruthy()
  expect(within(x).getByText("0 hostnames")).toBeTruthy()
  // No hostname table on the list: it lives on the domain's page.
  expect(screen.queryByRole("table")).toBeNull()
  expect(
    screen.getByText(
      "Domains you register here are bought with your own Cloudflare account and billed to it. Loomplane never pays for or holds a domain."
    )
  ).toBeTruthy()
})

it("shows a subdomain's own hosting only when it differs", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("loomplane.com", [], {
        subdomains: ["api.loomplane.com", "app.loomplane.com"],
      }),
      zoneWith("app.loomplane.com", [], {
        parent: "loomplane.com",
        proofZone: "loomplane.com",
        provider: "vercel",
        connection: "vc",
      }),
      zoneWith("api.loomplane.com", [], {
        parent: "loomplane.com",
        proofZone: "loomplane.com",
      })
    ),
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "loomplane.com" })
  const app = within(card).getByRole("link", { name: /^app\.loomplane\.com/ })
  const api = within(card).getByRole("link", { name: /^api\.loomplane\.com/ })
  expect(within(app).getByText("Vercel")).toBeTruthy()
  expect(within(api).queryByText("Cloudflare")).toBeNull()
})

it("rolls a subdomain that needs attention into its domain", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("acme.dev"),
      zoneWith("loomplane.com", [host("loomplane.com")], {
        subdomains: ["app.loomplane.com"],
      }),
      zoneWith(
        "app.loomplane.com",
        [host("app.loomplane.com", { dns: "failed", route: "failed" })],
        { parent: "loomplane.com", proofZone: "loomplane.com" }
      )
    ),
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "loomplane.com" })
  const header = within(card).getByRole("link", { name: /^loomplane\.com/ })
  expect(within(header).getByText("Needs attention").textContent).toBe(
    "Needs attention 1"
  )
  const other = screen.getByRole("region", { name: "acme.dev" })
  expect(within(other).getByText("Ready")).toBeTruthy()
  const filters = screen.getByRole("radiogroup", { name: "Show" })
  expect(
    within(filters).getByRole("radio", { name: /Needs attention/ }).textContent
  ).toBe("Needs attention 1")
  expect(within(filters).getByRole("radio", { name: /All/ }).textContent).toBe(
    "All 2"
  )
})

it("opens the domain an old link names once the list loads", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("docs-example.org", [], { external: true })
    ),
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "docs-example.org" }} onSearch={() => {}} />
    </Wrapped>
  )
  await waitFor(() =>
    expect(navigation.calls).toEqual([
      {
        to: "/domains/$zoneId",
        params: { zoneId: "zone-docs-example.org" },
        replace: true,
      },
    ])
  )
})

// These open a domain's own page now, where they move with it.
it.todo("shows TXT instructions and checks again")
it.todo("publishes a managed zone's TXT record instead of asking for it")
it.todo("waits for DNS once a managed zone's record is published")
it.todo("shows why a managed zone's record didn't publish and checks again")
it.todo(
  "offers wildcard certificates only on a domain that is its own Cloudflare zone"
)
it.todo("renews a managed zone's proof once it expires")
it.todo("offers a proof to a zone verified before proofs were required")
it.todo("saves grants with the zone's revision after a failed check")

it("shows a registration that has no zone yet", async () => {
  stubFetch({
    "/api/v1/domains": {
      zones: [],
      registrations: [
        registration({
          name: "acme.app",
          status: "registering",
          zone_id: "",
          domain_expires_at: zero,
        }),
      ],
    },
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "acme.app" })
  expect(
    within(card).getByText("Cloudflare is registering this domain.")
  ).toBeTruthy()
  expect(
    screen.getByRole("radio", { name: /Needs attention/ }).textContent
  ).toBe("Needs attention 1")
  expect(screen.queryByRole("status")).toBeNull()
})

it("keeps denied, failed and empty apart", async () => {
  access.allowed = false
  const writes = stubFetch({})
  const { unmount } = render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  expect(screen.getByText("Domain access is restricted")).toBeTruthy()
  expect(
    screen.getByText(
      "Ask your organization owner for permission to manage Cloud infrastructure."
    )
  ).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Bring a domain" })).toBeNull()
  unmount()
  expect(writes).toEqual([])

  // The server can still refuse.
  access.allowed = true
  stubFetch({
    "/api/v1/domains": () =>
      Response.json({ message: "forbidden" }, { status: 403 }),
  })
  const denied = render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  expect(await screen.findByText("Domain access is restricted")).toBeTruthy()
  denied.unmount()
  forgeClient.clear()

  stubFetch({
    "/api/v1/domains": () =>
      Response.json({ message: "Domains are unavailable" }, { status: 503 }),
  })
  const failed = render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  expect(await screen.findByText("Domains are unavailable.")).toBeTruthy()
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy()
  expect(screen.queryByText("No domains yet")).toBeNull()
  expect(screen.queryByText("Domain access is restricted")).toBeNull()
  failed.unmount()
  forgeClient.clear()

  stubFetch({ "/api/v1/domains": overviewWith() })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  expect(await screen.findByText("No domains yet")).toBeTruthy()
  expect(
    screen.getByText(
      "Bring a domain you own, or register a new one in your Cloudflare account."
    )
  ).toBeTruthy()
})

it("keeps the list through a failed refresh", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const pending = registration({
    name: "acme.app",
    status: "registering",
    zone_id: "",
    domain_expires_at: zero,
  })
  let reads = 0
  stubFetch({
    "/api/v1/domains": () => {
      reads++
      return reads === 1
        ? Response.json({ zones: [], registrations: [pending] })
        : Response.json({ message: "Domains are unavailable" }, { status: 503 })
    },
    "/api/v1/domain-registrations": { registrations: [pending] },
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "acme.app" })
  // The 5-second refresh of a registration in flight fails.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000)
  })
  expect(await screen.findByText("Domains are unavailable.")).toBeTruthy()
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy()
  expect(screen.getByRole("region", { name: "acme.app" })).toBe(card)
  expect(screen.queryByText("No domains yet")).toBeNull()
  expect(screen.queryByText("Domain access is restricted")).toBeNull()
})

it("puts Domains between Services and Clouds in the sidebar", async () => {
  stubFetch({})
  render(
    <Wrapped>
      <SidebarProvider>
        <AppSidebar />
      </SidebarProvider>
    </Wrapped>
  )
  const links = screen
    .getAllByRole("link")
    .map((a) => a.textContent?.trim())
    .filter((t) => ["Services", "Domains", "Clouds"].includes(t ?? ""))
  expect(links).toEqual(["Services", "Domains", "Clouds"])
  const domains = screen.getByRole("link", { name: "Domains" })
  expect(domains.getAttribute("href")).toBe("/domains")
  expect(domains.hasAttribute("data-active")).toBe(true)
  expect(
    screen.getByRole("link", { name: "Services" }).hasAttribute("data-active")
  ).toBe(false)
})
