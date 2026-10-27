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
import { Domains } from "./domains"

const access = vi.hoisted(() => ({ allowed: true }))
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
  useNavigate: () => () => {},
  Link: ({
    children,
    to,
    ...props
  }: {
    children?: ReactNode
    to: string
    "data-active"?: string
  }) => (
    <a href={to} data-active={props["data-active"]}>
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
})

const zero = "0001-01-01T00:00:00Z"

type Host = {
  hostname: string
  runs_on: string
  backend: string
  wildcard?: boolean
  dns: string
  certificate: string
  route: string
}

function zoneWith(
  name: string,
  hostnames: Host[] = [],
  options: {
    external?: boolean
    verified?: boolean
    registration?: string
    proof?: boolean
    hostedIn?: string
    published?: boolean
    publishError?: string
  } = {}
) {
  const proof = options.proof ?? options.external
  const verified = options.verified ?? !options.external
  const id = `zone-${name}`
  return {
    zone: {
      id,
      org_id: "org",
      name,
      mode: options.external ? "external" : "managed",
      provider: options.external ? "" : "cloudflare",
      connection_id: options.external ? "" : "cf",
      project_ids: ["shop"],
      proof_name: proof ? `_loomplane.${name}` : "",
      proof_value: proof ? "lp-proof-123" : "",
      proof_record: options.published
        ? {
            id: "rec",
            zone_id: `cf-${options.hostedIn ?? name}`,
            name: "_loomplane",
            type: "TXT",
            value: "lp-proof-123",
            ttl: 60,
            owner: `loomplane:zone-proof:${id}`,
          }
        : undefined,
      proof_publish_error: options.publishError ?? "",
      proof_checked_at: zero,
      proof_expires_at: zero,
      provider_zone: options.external
        ? {}
        : {
            id: `cf-${options.hostedIn ?? name}`,
            name: options.hostedIn ?? name,
          },
      record_ownership: true,
      registration_id: options.registration ?? "",
      revision: 2,
      verified_at: verified ? "2026-10-01T00:00:00Z" : zero,
    },
    state: verified ? "ready" : "awaiting_verification",
    wildcards: false,
    registration_id: options.registration ?? "",
    hostnames: hostnames.map((h, i) => ({
      route_id: `route-${i}`,
      hostname: h.hostname,
      project_id: "shop",
      project_name: "acme-shop",
      environment_id: "env-prod",
      service: "edge",
      port: 8080,
      runs_on: h.runs_on,
      status: {
        route_id: `route-${i}`,
        hostname: h.hostname,
        wildcard: !!h.wildcard,
        backend: h.backend,
        dns: h.dns,
        certificate: h.certificate,
        route: h.route,
        certificate_expires_at: zero,
        checked_at: zero,
        retryable: false,
      },
    })),
  }
}

function overviewWith(...zones: ReturnType<typeof zoneWith>[]): {
  zones: unknown[]
  registrations: unknown[]
} {
  return { zones, registrations: [] }
}

const inTwelveDays = new Date(Date.now() + 12 * 86400000).toISOString()

function registration(over: Record<string, unknown> = {}) {
  return {
    id: "reg",
    org_id: "org",
    name: "acme.shop",
    status: "succeeded",
    auto_renew: true,
    domain_expires_at: inTwelveDays,
    quote: {
      currency: "USD",
      registration_cost: "10.11",
      renewal_cost: "10.11",
    },
    revision: 3,
    account_id: "acct",
    connection_id: "cf",
    zone_id: "zone-acme.shop",
    message: "",
    project_ids: [],
    ...over,
  }
}

type Write = { method: string; path: string; body: unknown }

function stubFetch(
  routes: Record<string, unknown | ((body: unknown) => Response)>
) {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method !== "GET") writes.push({ method, path, body })
      const route = routes[`${method} ${path}`] ?? routes[path]
      if (typeof route === "function") return route(body)
      if (route !== undefined) return Response.json(route)
      if (path === "/api/v1/domain-registrations")
        return Response.json({
          registrations:
            (routes["/api/v1/domains"] as { registrations?: unknown[] })
              ?.registrations ?? [],
        })
      if (path === "/api/v1/connections")
        return Response.json({
          connections: [
            {
              id: "cf",
              cloud: "cloudflare",
              name: "Acme Cloudflare",
              account: "acct",
              status: "ready",
              details: {},
              missing: [],
            },
          ],
        })
      if (path === "/api/v1/projects")
        return Response.json({ projects: [{ id: "shop", name: "acme-shop" }] })
      if (path.endsWith("/environments"))
        return Response.json({
          environments: [{ id: "env-prod", name: "production" }],
        })
      return Response.json({ message: `unexpected ${path}` }, { status: 404 })
    }
  )
  return writes
}

function Wrapped({ children }: { children: ReactNode }) {
  return <ClientProvider client={forgeClient}>{children}</ClientProvider>
}

it("expands a domain into its hostname table", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("acme.dev", [
        {
          hostname: "api.acme.dev",
          runs_on: "web-1",
          backend: "server",
          dns: "ready",
          certificate: "ready",
          route: "ready",
        },
        {
          hostname: "*.acme.dev",
          runs_on: "web-1",
          backend: "server",
          wildcard: true,
          dns: "ready",
          certificate: "provisioning",
          route: "provisioning",
        },
      ])
    ),
  })
  render(
    <Wrapped>
      <Domains />
    </Wrapped>
  )
  const header = await screen.findByRole("button", { name: /acme\.dev/ })
  expect(header.getAttribute("aria-expanded")).toBe("false")
  await userEvent.click(header)
  expect(header.getAttribute("aria-expanded")).toBe("true")
  for (const h of [
    "Hostname",
    "Used by",
    "Runs on",
    "DNS",
    "Certificate",
    "Route",
  ])
    expect(screen.getByText(h)).toBeTruthy()
  const wildcard = screen.getByRole("row", { name: /\*\.acme\.dev/ })
  expect(within(wildcard).getByText("Issuing (DNS-01)")).toBeTruthy()
  expect(within(wildcard).getByText("Waiting for certificate")).toBeTruthy()
  expect(within(wildcard).getByText("Docker · web-1")).toBeTruthy()
  expect(
    await within(wildcard).findByText("acme-shop / production / edge")
  ).toBeTruthy()
  const api = screen.getByRole("row", { name: /^api\.acme\.dev/ })
  expect(within(api).getByText("Active")).toBeTruthy()
  expect(screen.getByText("Managed by Cloudflare")).toBeTruthy()
  expect(screen.getByText("Registered elsewhere")).toBeTruthy()
  expect(screen.getByText("1 domain · 2 hostnames in use")).toBeTruthy()
  expect(
    screen.getByText(
      "Domains you register here are bought with your own Cloudflare account and billed to it. Loomplane never pays for or holds a domain."
    )
  ).toBeTruthy()
})

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
  expect(screen.getByText("Awaiting TXT record")).toBeTruthy()
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
  const card = await screen.findByRole("region", { name: "acme.dev" })
  expect(screen.queryByRole("region", { name: "Bring a domain" })).toBeNull()
  expect(
    within(card)
      .getByRole("button", { name: /acme\.dev/ })
      .getAttribute("aria-expanded")
  ).toBe("true")
})

it("shows TXT instructions and checks again", async () => {
  const writes = stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("docs-example.org", [], { external: true })
    ),
    "POST /api/v1/dns-zones/zone-docs-example.org/verify": () =>
      Response.json({ message: "the record isn't there" }, { status: 409 }),
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "zone-docs-example.org" }} onSearch={() => {}} />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "docs-example.org" })
  expect(
    within(card).getByText(
      /then check again\. Projects can't use the domain until it's verified\./
    )
  ).toBeTruthy()
  expect(within(card).getByText("_loomplane.docs-example.org")).toBeTruthy()
  expect(within(card).getByText("lp-proof-123")).toBeTruthy()
  expect(within(card).getByText("TXT")).toBeTruthy()
  await userEvent.click(
    within(card).getByRole("button", { name: "Check again" })
  )
  expect(
    await within(card).findByText(
      "Checked just now. The record isn't visible yet."
    )
  ).toBeTruthy()
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/dns-zones/zone-docs-example.org/verify",
      body: undefined,
    },
  ])
})

it("publishes a managed zone's TXT record instead of asking for it", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("acme.dev", [], { verified: false, proof: true })
    ),
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "zone-acme.dev" }} onSearch={() => {}} />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "acme.dev" })
  expect(within(card).getByText("Verifying")).toBeTruthy()
  expect(
    within(card).getByText("Publishing the TXT record through Cloudflare.")
  ).toBeTruthy()
  expect(within(card).queryByText(/Add this TXT record/)).toBeNull()
  expect(within(card).getByRole("button", { name: "Check again" })).toBeTruthy()
})

it("waits for DNS once a managed zone's record is published", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("acme.dev", [], {
        verified: false,
        proof: true,
        published: true,
      })
    ),
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "zone-acme.dev" }} onSearch={() => {}} />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "acme.dev" })
  expect(
    within(card).getByText("Waiting for DNS to show the TXT record.")
  ).toBeTruthy()
  expect(within(card).queryByText(/Add this TXT record/)).toBeNull()
  // What was written stays visible, as secondary detail.
  const details = within(card).getByText("Record details")
  await userEvent.click(details)
  expect(within(card).getByText("_loomplane.acme.dev")).toBeTruthy()
  expect(within(card).getByText("lp-proof-123")).toBeTruthy()
})

it("shows why a managed zone's record didn't publish and checks again", async () => {
  const writes = stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("acme.dev", [], {
        verified: false,
        proof: true,
        publishError:
          "The Cloudflare connection can't write DNS records for acme.dev.",
      })
    ),
    "POST /api/v1/dns-zones/zone-acme.dev/verify": () =>
      Response.json({ message: "not yet" }, { status: 409 }),
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "zone-acme.dev" }} onSearch={() => {}} />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "acme.dev" })
  expect(
    within(card).getByText(
      "The Cloudflare connection can't write DNS records for acme.dev."
    )
  ).toBeTruthy()
  expect(within(card).queryByText(/Publishing the TXT record/)).toBeNull()
  await userEvent.click(
    within(card).getByRole("button", { name: "Check again" })
  )
  // The reason is above, so the note doesn't guess at visibility.
  expect(await within(card).findByText("Checked just now.")).toBeTruthy()
  expect(within(card).queryByText(/The record isn't visible yet/)).toBeNull()
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/dns-zones/zone-acme.dev/verify",
        body: undefined,
      },
    ])
  )
})

it("offers wildcard certificates only on a domain that is its own Cloudflare zone", async () => {
  stubFetch({
    "/api/v1/domains": overviewWith(
      zoneWith("acme.dev"),
      zoneWith("shop.acme.org", [], { hostedIn: "acme.org" })
    ),
  })
  const setUp = { name: "Set up wildcard certificates" }
  const view = render(
    <Wrapped>
      <Domains search={{ zone: "zone-acme.dev" }} onSearch={() => {}} />
    </Wrapped>
  )
  const own = await screen.findByRole("region", { name: "acme.dev" })
  expect(await within(own).findByRole("button", setUp)).toBeTruthy()
  view.unmount()
  render(
    <Wrapped>
      <Domains search={{ zone: "zone-shop.acme.org" }} onSearch={() => {}} />
    </Wrapped>
  )
  const inside = await screen.findByRole("region", { name: "shop.acme.org" })
  expect(
    await within(inside).findByText(/No hostnames use this domain yet/)
  ).toBeTruthy()
  expect(within(inside).queryByRole("button", setUp)).toBeNull()
})

it("renews a managed zone's proof once it expires", async () => {
  // Not in the mock: proofs there never expire.
  let renewed = false
  const awaiting = () => {
    const z = zoneWith("acme.dev", [], { verified: false, proof: true })
    z.zone.proof_expires_at = renewed
      ? new Date(Date.now() + 86400000).toISOString()
      : "2026-01-01T00:00:00Z"
    z.zone.proof_value = renewed ? "lp-proof-456" : "lp-proof-123"
    return z
  }
  const writes = stubFetch({
    "/api/v1/domains": () =>
      Response.json({ zones: [awaiting()], registrations: [] }),
    "POST /api/v1/dns-zones/zone-acme.dev/update": () => {
      renewed = true
      return Response.json(awaiting().zone)
    },
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "zone-acme.dev" }} onSearch={() => {}} />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "acme.dev" })
  expect(await within(card).findByText(/This value expired/)).toBeTruthy()
  expect(within(card).queryByRole("button", { name: "Check again" })).toBeNull()
  await userEvent.click(
    within(card).getByRole("button", { name: "Renew proof" })
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/dns-zones/zone-acme.dev/update",
      body: { project_ids: ["shop"], renew_proof: true, revision: 2 },
    },
  ])
  expect(await within(card).findByText("lp-proof-456")).toBeTruthy()
  expect(within(card).getByRole("button", { name: "Check again" })).toBeTruthy()
})

it("offers a proof to a zone verified before proofs were required", async () => {
  const writes = stubFetch({
    "/api/v1/domains": overviewWith(
      // The API reads a verified zone with no proof as awaiting verification.
      { ...zoneWith("acme.dev"), state: "awaiting_verification" }
    ),
    "POST /api/v1/dns-zones/zone-acme.dev/update": () =>
      Response.json(zoneWith("acme.dev", [], { proof: true }).zone),
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "zone-acme.dev" }} onSearch={() => {}} />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "acme.dev" })
  expect(within(card).getByText("Awaiting verification")).toBeTruthy()
  expect(await within(card).findByText(/doesn't have one yet/)).toBeTruthy()
  await userEvent.click(
    within(card).getByRole("button", { name: "Renew proof" })
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/dns-zones/zone-acme.dev/update",
      body: { project_ids: ["shop"], renew_proof: true, revision: 2 },
    },
  ])
})

it("saves grants with the zone's revision after a failed check", async () => {
  let revision = 2
  const external = () => {
    const z = zoneWith("docs-example.org", [], { external: true })
    z.zone.revision = revision
    z.zone.project_ids = []
    return z
  }
  const writes = stubFetch({
    "/api/v1/domains": () =>
      Response.json({ zones: [external()], registrations: [] }),
    "POST /api/v1/dns-zones/zone-docs-example.org/verify": () => {
      // A failed check saves when it looked, which moves the revision.
      revision = 3
      return Response.json({ message: "not yet" }, { status: 409 })
    },
    "POST /api/v1/dns-zones/zone-docs-example.org/update": () =>
      Response.json(external().zone),
  })
  render(
    <Wrapped>
      <Domains search={{ zone: "docs-example.org" }} onSearch={() => {}} />
    </Wrapped>
  )
  const card = await screen.findByRole("region", { name: "docs-example.org" })
  await userEvent.click(
    within(card).getByRole("button", { name: "Check again" })
  )
  await within(card).findByText(
    "Checked just now. The record isn't visible yet."
  )
  await waitFor(() => expect(revision).toBe(3))
  await userEvent.click(
    within(card).getByRole("button", { name: "Not granted to a project yet" })
  )
  await userEvent.click(
    await screen.findByRole("checkbox", { name: "acme-shop" })
  )
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(writes.at(-1)).toEqual({
      method: "POST",
      path: "/api/v1/dns-zones/zone-docs-example.org/update",
      body: { project_ids: ["shop"], renew_proof: false, revision: 3 },
    })
  )
})

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
