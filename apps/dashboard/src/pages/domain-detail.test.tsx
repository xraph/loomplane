import { ClientProvider } from "@forge-go/client-react"
import {
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
import {
  detailPath,
  detailWith,
  host,
  inTwelveDays,
  registration,
  stubFetch,
  zoneWith,
} from "@/test-fixtures/domains"
import { DomainDetailPage } from "./domain-detail"

const access = vi.hoisted(() => ({ allowed: true }))
const navigate = vi.hoisted(() => vi.fn())
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    session: { orgId: "org" },
    organizations: [{ id: "org", name: "Acme Inc", slug: "acme" }],
    switchOrganization: async () => {},
    can: (permission: string) =>
      permission === "organization_cloud:manage" ? access.allowed : true,
  }),
}))
// The crumbs after the organization, which the header puts first.
vi.mock("@/components/site-header", () => ({
  SiteHeader: ({ crumbs }: { crumbs: { label: string }[] }) => (
    <nav aria-label="Breadcrumbs">{crumbs.map((c) => c.label).join(" / ")}</nav>
  ),
}))
vi.mock("@/lib/router", () => ({
  usePath: () => "/domains",
  useNavigate: () => navigate,
  Link: ({
    children,
    to,
    params,
  }: {
    children?: ReactNode
    to: string
    params?: Record<string, string>
  }) => (
    <a href={to.replace(/\$(\w+)/g, (_, key: string) => params?.[key] ?? "")}>
      {children}
    </a>
  ),
}))

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  forgeClient.clear()
  access.allowed = true
  navigate.mockReset()
})

function open(zoneId: string) {
  return render(
    <ClientProvider client={forgeClient}>
      <DomainDetailPage zoneId={zoneId} />
    </ClientProvider>
  )
}

const section = (name: string) => screen.findByRole("region", { name })

it("shows a domain's status, hosting and hostnames", async () => {
  stubFetch({
    [detailPath("acme.dev")]: detailWith(
      zoneWith("acme.dev", [
        host("api.acme.dev"),
        host("*.acme.dev", {
          wildcard: true,
          certificate: "provisioning",
          route: "provisioning",
        }),
      ])
    ),
  })
  open("zone-acme.dev")
  expect(
    await screen.findByRole("heading", { level: 1, name: "acme.dev" })
  ).toBeTruthy()
  expect(screen.getByRole("navigation").textContent).toBe("Domains / acme.dev")
  expect(screen.getByText("Managed by Cloudflare")).toBeTruthy()
  // A verified domain has nothing to check.
  expect(screen.queryByRole("button", { name: "Check again" })).toBeNull()

  const status = await section("Status")
  expect(within(status).getByText(/^Verified\./)).toBeTruthy()
  expect(await within(status).findByText("Acme Cloudflare")).toBeTruthy()
  expect(within(status).getByText("acct")).toBeTruthy()
  // It is its own Cloudflare zone, so no hosting zone is named.
  expect(within(status).queryByText("Hosting zone")).toBeNull()

  const hostnames = await section("Hostnames")
  for (const h of ["Hostname", "Used by", "Runs on", "DNS", "Certificate"])
    expect(within(hostnames).getByText(h)).toBeTruthy()
  // Only a domain with subdomains says which one a hostname is under.
  expect(within(hostnames).queryByText("Subdomain")).toBeNull()
  const wildcard = within(hostnames).getByRole("row", { name: /\*\.acme\.dev/ })
  expect(within(wildcard).getByText("Issuing (DNS-01)")).toBeTruthy()
  expect(within(wildcard).getByText("Waiting for certificate")).toBeTruthy()
  expect(within(wildcard).getByText("Docker · web-1")).toBeTruthy()
  const usedBy = await within(wildcard).findByRole("link", {
    name: "acme-shop / production / edge",
  })
  expect(usedBy.getAttribute("href")).toBe("/projects/shop")
  const api = within(hostnames).getByRole("row", { name: /^api\.acme\.dev/ })
  expect(within(api).getByText("Active")).toBeTruthy()

  expect(await section("Access")).toBeTruthy()
  expect(screen.getByText("Subdomains have their own projects.")).toBeTruthy()
  expect(await section("DNS records")).toBeTruthy()
  // Not registered here, so there's no Registration section.
  expect(screen.queryByRole("region", { name: "Registration" })).toBeNull()
  expect(await section("Subdomains")).toBeTruthy()
})

it("says a subdomain is verified through its domain", async () => {
  const domain = zoneWith("loomplane.com", [], {
    subdomains: ["api.loomplane.com"],
  })
  stubFetch({
    [detailPath("api.loomplane.com")]: detailWith(
      zoneWith("api.loomplane.com", [], {
        parent: "loomplane.com",
        proofZone: "loomplane.com",
      }),
      { parent: domain, proofZone: domain }
    ),
  })
  open("zone-api.loomplane.com")
  const status = await section("Status")
  const line = within(status).getByText(/verified through it/)
  expect(line.textContent).toBe("Part of loomplane.com · verified through it")
  expect(
    within(line)
      .getByRole("link", { name: "loomplane.com" })
      .getAttribute("href")
  ).toBe("/domains/zone-loomplane.com")
  expect(screen.queryByRole("button", { name: "Check again" })).toBeNull()
  // A subdomain with none under it has no Subdomains section.
  expect(screen.queryByRole("region", { name: "Subdomains" })).toBeNull()
})

it("names the zone that verifies a subdomain two levels down", async () => {
  const domain = zoneWith("loomplane.com", [], {
    subdomains: ["app.loomplane.com", "x.app.loomplane.com"],
  })
  // The parent has no proof of its own: the domain proves both.
  const parent = zoneWith("app.loomplane.com", [], {
    parent: "loomplane.com",
    proofZone: "loomplane.com",
    subdomains: ["x.app.loomplane.com"],
  })
  const nested = (verified: boolean) =>
    detailWith(
      zoneWith("x.app.loomplane.com", [], {
        verified,
        proof: false,
        parent: "app.loomplane.com",
        proofZone: "loomplane.com",
      }),
      { parent, proofZone: domain }
    )
  stubFetch({ [detailPath("x.app.loomplane.com")]: nested(true) })
  const view = open("zone-x.app.loomplane.com")
  let status = await section("Status")
  const line = within(status).getByText(/verified through/)
  expect(line.textContent).toBe(
    "Part of app.loomplane.com · verified through loomplane.com"
  )
  expect(
    within(line)
      .getByRole("link", { name: "loomplane.com" })
      .getAttribute("href")
  ).toBe("/domains/zone-loomplane.com")
  view.unmount()
  forgeClient.clear()

  stubFetch({ [detailPath("x.app.loomplane.com")]: nested(false) })
  open("zone-x.app.loomplane.com")
  status = await section("Status")
  expect(within(status).getByText(/waits for/).textContent).toBe(
    "Part of app.loomplane.com · waits for loomplane.com's verification"
  )
})

it("shows a subdomain waiting on its domain and checks the domain again", async () => {
  const domain = zoneWith("loomplane.com", [], {
    external: true,
    subdomains: ["api.loomplane.com"],
  })
  const writes = stubFetch({
    [detailPath("api.loomplane.com")]: detailWith(
      zoneWith("api.loomplane.com", [], {
        external: true,
        proof: false,
        verified: false,
        parent: "loomplane.com",
        proofZone: "loomplane.com",
      }),
      { parent: domain, proofZone: domain }
    ),
    "POST /api/v1/dns-zones/zone-loomplane.com/verify": () =>
      Response.json({ message: "not yet" }, { status: 409 }),
  })
  open("zone-api.loomplane.com")
  const status = await section("Status")
  expect(within(status).getByText(/waits for/).textContent).toBe(
    "Part of loomplane.com · waits for loomplane.com's verification"
  )
  expect(
    within(status).getByText("This is loomplane.com's record.")
  ).toBeTruthy()
  // The record to add is the domain's, not the subdomain's.
  expect(within(status).getByText("_loomplane.loomplane.com")).toBeTruthy()
  const actions = screen.getByRole("group", { name: "Domain actions" })
  await userEvent.click(
    within(actions).getByRole("button", { name: "Check again" })
  )
  await waitFor(() =>
    expect(writes).toEqual([
      {
        method: "POST",
        path: "/api/v1/dns-zones/zone-loomplane.com/verify",
        body: undefined,
      },
    ])
  )
})

it("shows TXT instructions and checks again", async () => {
  const writes = stubFetch({
    [detailPath("docs-example.org")]: detailWith(
      zoneWith("docs-example.org", [], { external: true })
    ),
    "POST /api/v1/dns-zones/zone-docs-example.org/verify": () =>
      Response.json({ message: "the record isn't there" }, { status: 409 }),
  })
  open("zone-docs-example.org")
  const status = await section("Status")
  expect(screen.getByText("Awaiting TXT record")).toBeTruthy()
  expect(
    within(status).getByText(
      /then check again\. Projects can't use the domain until it's verified\./
    )
  ).toBeTruthy()
  expect(within(status).getByText("_loomplane.docs-example.org")).toBeTruthy()
  expect(within(status).getByText("lp-proof-123")).toBeTruthy()
  expect(within(status).getByText("TXT")).toBeTruthy()
  expect(within(status).queryByText(/This is .*'s record/)).toBeNull()
  await userEvent.click(
    within(status).getByRole("button", { name: "Check again" })
  )
  expect(
    await within(status).findByText(
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
    [detailPath("acme.dev")]: detailWith(
      zoneWith("acme.dev", [], { verified: false, proof: true })
    ),
  })
  open("zone-acme.dev")
  const status = await section("Status")
  expect(screen.getByText("Verifying")).toBeTruthy()
  expect(
    within(status).getByText("Publishing the TXT record through Cloudflare.")
  ).toBeTruthy()
  expect(within(status).queryByText(/Add this TXT record/)).toBeNull()
  expect(
    within(status).getByRole("button", { name: "Check again" })
  ).toBeTruthy()
})

it("waits for DNS once a managed zone's record is published", async () => {
  stubFetch({
    [detailPath("acme.dev")]: detailWith(
      zoneWith("acme.dev", [], {
        verified: false,
        proof: true,
        published: true,
      })
    ),
  })
  open("zone-acme.dev")
  const status = await section("Status")
  expect(
    within(status).getByText("Waiting for DNS to show the TXT record.")
  ).toBeTruthy()
  expect(within(status).queryByText(/Add this TXT record/)).toBeNull()
  // What was written stays visible, as secondary detail.
  await userEvent.click(within(status).getByText("Record details"))
  expect(within(status).getByText("_loomplane.acme.dev")).toBeTruthy()
  expect(within(status).getByText("lp-proof-123")).toBeTruthy()
})

it("shows why a managed zone's record didn't publish and checks again", async () => {
  const writes = stubFetch({
    [detailPath("acme.dev")]: detailWith(
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
  open("zone-acme.dev")
  const status = await section("Status")
  expect(
    within(status).getByText(
      "The Cloudflare connection can't write DNS records for acme.dev."
    )
  ).toBeTruthy()
  expect(within(status).queryByText(/Publishing the TXT record/)).toBeNull()
  await userEvent.click(
    within(status).getByRole("button", { name: "Check again" })
  )
  // The reason is above, so the note doesn't guess at visibility.
  expect(await within(status).findByText("Checked just now.")).toBeTruthy()
  expect(within(status).queryByText(/The record isn't visible yet/)).toBeNull()
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
    [detailPath("acme.dev")]: detailWith(zoneWith("acme.dev")),
    [detailPath("shop.acme.org")]: detailWith(
      zoneWith("shop.acme.org", [], { hostedIn: "acme.org" })
    ),
  })
  const setUp = { name: "Set up wildcard certificates" }
  const view = open("zone-acme.dev")
  const own = await section("Wildcards")
  expect(within(own).getByRole("button", setUp)).toBeTruthy()
  view.unmount()

  open("zone-shop.acme.org")
  const status = await section("Status")
  // Its DNS sits in a bigger Cloudflare zone, which the status names.
  expect(within(status).getByText("Hosting zone")).toBeTruthy()
  expect(within(status).getByText("acme.org")).toBeTruthy()
  expect(
    await screen.findByText(/No hostnames use this domain yet/)
  ).toBeTruthy()
  expect(screen.queryByRole("region", { name: "Wildcards" })).toBeNull()
  expect(screen.queryByRole("button", setUp)).toBeNull()
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
    [detailPath("acme.dev")]: () => Response.json(detailWith(awaiting())),
    "POST /api/v1/dns-zones/zone-acme.dev/update": () => {
      renewed = true
      return Response.json(awaiting().zone)
    },
  })
  open("zone-acme.dev")
  const status = await section("Status")
  expect(await within(status).findByText(/This value expired/)).toBeTruthy()
  // An expired value can't verify, so nothing offers to check it.
  expect(screen.queryByRole("button", { name: "Check again" })).toBeNull()
  await userEvent.click(
    within(status).getByRole("button", { name: "Renew proof" })
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/dns-zones/zone-acme.dev/update",
      body: { project_ids: ["shop"], renew_proof: true, revision: 2 },
    },
  ])
  expect(await within(status).findByText("lp-proof-456")).toBeTruthy()
  expect(
    within(status).getByRole("button", { name: "Check again" })
  ).toBeTruthy()
})

it("offers a proof to a zone verified before proofs were required", async () => {
  const writes = stubFetch({
    [detailPath("acme.dev")]: detailWith(
      // The API reads a verified zone with no proof as awaiting verification.
      { ...zoneWith("acme.dev"), state: "awaiting_verification" }
    ),
    "POST /api/v1/dns-zones/zone-acme.dev/update": () =>
      Response.json(zoneWith("acme.dev", [], { proof: true }).zone),
  })
  open("zone-acme.dev")
  const status = await section("Status")
  expect(screen.getByText("Awaiting verification")).toBeTruthy()
  expect(within(status).getByText(/doesn't have one yet/)).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Check again" })).toBeNull()
  await userEvent.click(
    within(status).getByRole("button", { name: "Renew proof" })
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
    [detailPath("docs-example.org")]: () =>
      Response.json(detailWith(external())),
    "POST /api/v1/dns-zones/zone-docs-example.org/verify": () => {
      // A failed check saves when it looked, which moves the revision.
      revision = 3
      return Response.json({ message: "not yet" }, { status: 409 })
    },
    "POST /api/v1/dns-zones/zone-docs-example.org/update": () =>
      Response.json(external().zone),
  })
  open("zone-docs-example.org")
  const status = await section("Status")
  await userEvent.click(
    within(status).getByRole("button", { name: "Check again" })
  )
  await within(status).findByText(
    "Checked just now. The record isn't visible yet."
  )
  await waitFor(() => expect(revision).toBe(3))
  const grants = await section("Access")
  expect(within(grants).getByText("Not granted to a project yet")).toBeTruthy()
  await userEvent.click(
    await within(grants).findByRole("checkbox", { name: "acme-shop" })
  )
  await userEvent.click(within(grants).getByRole("button", { name: "Save" }))
  await waitFor(() =>
    expect(writes.at(-1)).toEqual({
      method: "POST",
      path: "/api/v1/dns-zones/zone-docs-example.org/update",
      body: { project_ids: ["shop"], renew_proof: false, revision: 3 },
    })
  )
})

it("lists subdomains with their hosting, grants and hostnames", async () => {
  const api = zoneWith("api.loomplane.com", [host("v1.api.loomplane.com")], {
    parent: "loomplane.com",
  })
  const eu = zoneWith("eu.loomplane.com", [], {
    parent: "loomplane.com",
    provider: "digitalocean",
    connection: "do",
    verified: false,
    proof: true,
  })
  eu.zone.project_ids = []
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [host("www.loomplane.com")], {
        subdomains: ["api.loomplane.com", "eu.loomplane.com"],
      }),
      { subdomains: [api, eu] }
    ),
  })
  open("zone-loomplane.com")
  const subdomains = await section("Subdomains")
  const apiRow = within(subdomains).getByRole("row", {
    name: /^api\.loomplane\.com/,
  })
  // Its DNS is the domain's, so no hosting is named.
  expect(within(apiRow).queryByText("Cloudflare")).toBeNull()
  expect(await within(apiRow).findByText("Granted to acme-shop")).toBeTruthy()
  expect(within(apiRow).getByText("1 hostname")).toBeTruthy()
  expect(within(apiRow).getByText("Managed by Cloudflare")).toBeTruthy()
  expect(
    within(apiRow).getByRole("link", { name: "Open" }).getAttribute("href")
  ).toBe("/domains/zone-api.loomplane.com")
  const euRow = within(subdomains).getByRole("row", {
    name: /^eu\.loomplane\.com/,
  })
  expect(within(euRow).getByText("DigitalOcean")).toBeTruthy()
  expect(within(euRow).getByText("Not granted to a project yet")).toBeTruthy()
  expect(within(euRow).getByText("0 hostnames")).toBeTruthy()
  expect(within(euRow).getByText("Verifying")).toBeTruthy()

  // The domain's hostnames and its subdomains', with where each sits.
  const hostnames = await section("Hostnames")
  expect(
    within(hostnames).getByRole("columnheader", { name: "Subdomain" })
  ).toBeTruthy()
  const v1 = within(hostnames).getByRole("row", {
    name: /^v1\.api\.loomplane\.com/,
  })
  expect(within(v1).getByText("api.loomplane.com")).toBeTruthy()
  expect(
    within(hostnames).getByRole("row", { name: /^www\.loomplane\.com/ })
  ).toBeTruthy()
})

it("shows the registration with its renewal switch", async () => {
  const day = new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: "UTC",
  }).format(new Date(inTwelveDays))
  const writes = stubFetch({
    [detailPath("acme.shop")]: detailWith(
      zoneWith("acme.shop", [], { registration: "reg" }),
      { registration: registration() }
    ),
    "POST /api/v1/domain-registrations/reg/auto-renew": () =>
      Response.json(registration({ auto_renew: false, revision: 4 })),
  })
  open("zone-acme.shop")
  const reg = await section("Registration")
  expect(within(reg).getByText("Cloudflare")).toBeTruthy()
  expect(within(reg).getByText("acct")).toBeTruthy()
  expect(within(reg).getByText(day)).toBeTruthy()
  expect(within(reg).getByText("USD 10.11")).toBeTruthy()
  expect(within(reg).getByText("On")).toBeTruthy()
  expect(
    within(reg)
      .getByRole("link", { name: "Open in Cloudflare" })
      .getAttribute("href")
  ).toBe("https://dash.cloudflare.com/acct/domains/registrations")
  await userEvent.click(
    within(reg).getByRole("button", { name: "Turn off auto-renew" })
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
    await within(reg).findByRole("button", { name: "Turn auto-renew back on" })
  ).toBeTruthy()
  expect(within(reg).getByText("Off")).toBeTruthy()
})

it("keeps loading, denied, not found and failed apart", async () => {
  access.allowed = false
  const writes = stubFetch({})
  const restricted = open("zone-acme.dev")
  expect(screen.getByText("Domain access is restricted")).toBeTruthy()
  restricted.unmount()
  expect(writes).toEqual([])
  access.allowed = true

  stubFetch({ [detailPath("acme.dev")]: () => new Promise<Response>(() => {}) })
  const loading = open("zone-acme.dev")
  expect(document.querySelector("[aria-busy='true']")).toBeTruthy()
  expect(
    screen.queryByText("This domain isn't in your organization")
  ).toBeNull()
  loading.unmount()
  forgeClient.clear()

  // The server can still refuse.
  stubFetch({
    [detailPath("acme.dev")]: () =>
      Response.json({ message: "forbidden" }, { status: 403 }),
  })
  const denied = open("zone-acme.dev")
  expect(await screen.findByText("Domain access is restricted")).toBeTruthy()
  denied.unmount()
  forgeClient.clear()

  stubFetch({
    [detailPath("acme.dev")]: () =>
      Response.json({ message: "zone not found" }, { status: 404 }),
  })
  const missing = open("zone-acme.dev")
  expect(
    await screen.findByText("This domain isn't in your organization")
  ).toBeTruthy()
  expect(
    screen.getByRole("link", { name: "All domains" }).getAttribute("href")
  ).toBe("/domains")
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  missing.unmount()
  forgeClient.clear()

  stubFetch({
    [detailPath("acme.dev")]: () =>
      Response.json({ message: "Domains are unavailable" }, { status: 503 }),
  })
  open("zone-acme.dev")
  expect(await screen.findByText("Domains are unavailable.")).toBeTruthy()
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy()
  expect(
    screen.queryByText("This domain isn't in your organization")
  ).toBeNull()
  expect(screen.queryByText("Domain access is restricted")).toBeNull()
})

// Connected accounts a subdomain can be hosted on, and two it can't.
const accounts = {
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
    {
      id: "vc",
      cloud: "vercel",
      name: "Acme Vercel",
      account: "acme-team",
      status: "ready",
      details: {},
      missing: [],
    },
    {
      id: "do",
      cloud: "digitalocean",
      name: "Pending DigitalOcean",
      account: "",
      status: "pending",
      details: {},
      missing: [],
    },
    {
      id: "gh",
      cloud: "github",
      name: "Acme GitHub",
      account: "acme",
      status: "ready",
      details: {},
      missing: [],
    },
  ],
}

async function addSubdomain(label: string) {
  const actions = await screen.findByRole("group", { name: "Domain actions" })
  await userEvent.click(
    within(actions).getByRole("button", { name: "Add subdomain" })
  )
  const dialog = await screen.findByRole("dialog")
  await userEvent.type(
    within(dialog).getByRole("textbox", { name: "Subdomain" }),
    label
  )
  return dialog
}

it("adds a subdomain with the domain's hosting", async () => {
  const writes = stubFetch({
    [detailPath("loomplane.com")]: detailWith(zoneWith("loomplane.com")),
    "POST /api/v1/dns-zones": () =>
      Response.json(
        zoneWith("api.eu.loomplane.com", [], { parent: "loomplane.com" }).zone
      ),
  })
  open("zone-loomplane.com")
  const dialog = await addSubdomain("api.eu")
  // The full name follows what you type.
  expect(within(dialog).getByText("api.eu.loomplane.com")).toBeTruthy()
  // Off by default: the domain's own hosting.
  expect(
    within(dialog)
      .getByRole("switch", { name: "Hosted elsewhere" })
      .getAttribute("aria-checked")
  ).toBe("false")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add subdomain" })
  )
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/domains/$zoneId",
      params: { zoneId: "zone-api.eu.loomplane.com" },
    })
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/dns-zones",
      body: {
        name: "api.eu.loomplane.com",
        mode: "managed",
        connection_id: "cf",
        project_ids: [],
      },
    },
  ])
})

it("adds a subdomain hosted on another connection", async () => {
  let tries = 0
  const writes = stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [], { external: true })
    ),
    "/api/v1/connections": accounts,
    "POST /api/v1/dns-zones": () =>
      ++tries === 1
        ? Response.json({ message: "zone not found" }, { status: 404 })
        : Response.json(
            zoneWith("app.loomplane.com", [], {
              parent: "loomplane.com",
              provider: "vercel",
              connection: "vc",
            }).zone
          ),
  })
  open("zone-loomplane.com")
  const dialog = await addSubdomain("app")
  await userEvent.click(
    within(dialog).getByRole("switch", { name: "Hosted elsewhere" })
  )
  const picks = within(dialog).getByRole("radiogroup", {
    name: "Connected account",
  })
  // Ready Cloudflare, DigitalOcean and Vercel accounts only.
  expect(within(picks).getAllByRole("radio")).toHaveLength(2)
  expect(
    within(picks).getByRole("radio", { name: /Acme Cloudflare/ })
  ).toBeTruthy()
  expect(within(picks).queryByRole("radio", { name: /Pending/ })).toBeNull()
  expect(within(picks).queryByRole("radio", { name: /GitHub/ })).toBeNull()
  const add = within(dialog).getByRole("button", { name: "Add subdomain" })
  // Nothing is picked yet.
  expect(add).toHaveProperty("disabled", true)
  await userEvent.click(
    within(picks).getByRole("radio", { name: /Acme Vercel/ })
  )
  await userEvent.click(add)
  expect(
    await within(dialog).findByText(
      "app.loomplane.com isn't a zone in that Vercel account."
    )
  ).toBeTruthy()
  expect(navigate).not.toHaveBeenCalled()
  await userEvent.click(add)
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/domains/$zoneId",
      params: { zoneId: "zone-app.loomplane.com" },
    })
  )
  const sent = {
    method: "POST",
    path: "/api/v1/dns-zones",
    body: {
      name: "app.loomplane.com",
      mode: "managed",
      connection_id: "vc",
      project_ids: [],
    },
  }
  expect(writes).toEqual([sent, sent])
})

it("refuses an invalid subdomain label before sending", async () => {
  const writes = stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [], { external: true })
    ),
  })
  open("zone-loomplane.com")
  const dialog = await addSubdomain("*.app")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Add subdomain" })
  )
  expect(within(dialog).getByRole("alert").textContent).toMatch(
    /Enter a label like app or api\.eu/
  )
  expect(writes).toEqual([])
})

async function confirmRemoval(name: string) {
  const dialog = await screen.findByRole("alertdialog")
  const confirm = within(dialog).getByRole("button", { name: /^Remove/ })
  const field = within(dialog).getByRole("textbox", {
    name: `Type ${name} to confirm`,
  })
  return { dialog, confirm, field }
}

it("removes a domain once its name is typed", async () => {
  let revision = 2
  let reads = 0
  const writes = stubFetch({
    [detailPath("loomplane.com")]: () => {
      reads++
      const z = zoneWith("loomplane.com")
      z.zone.revision = revision
      return Response.json(detailWith(z))
    },
    "POST /api/v1/dns-zones/zone-loomplane.com/remove": (body: unknown) => {
      if ((body as { revision: number }).revision === 2) {
        // Someone changed it since the page read it.
        revision = 3
        return Response.json(
          { message: "the zone changed, reload it" },
          { status: 409 }
        )
      }
      return new Response(null, { status: 204 })
    },
  })
  open("zone-loomplane.com")
  const danger = await section("Remove domain")
  await userEvent.click(
    within(danger).getByRole("button", { name: "Remove domain" })
  )
  const { dialog, confirm, field } = await confirmRemoval("loomplane.com")
  expect(confirm).toHaveProperty("disabled", true)
  await userEvent.type(field, "loomplane.co")
  expect(confirm).toHaveProperty("disabled", true)
  await userEvent.type(field, "m")
  expect(confirm).toHaveProperty("disabled", false)
  await userEvent.click(confirm)
  expect(
    await within(dialog).findByText("The zone changed, reload it.")
  ).toBeTruthy()
  expect(navigate).not.toHaveBeenCalled()
  // The page reads the zone again, so the next try has its revision.
  await waitFor(() => expect(reads).toBe(2))
  await userEvent.click(confirm)
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({ to: "/domains", replace: true })
  )
  expect(writes.map((w) => w.body)).toEqual([{ revision: 2 }, { revision: 3 }])
  expect(
    writes.every((w) => w.path.endsWith("/zone-loomplane.com/remove"))
  ).toBe(true)
})

it("lists the hostnames that block removal", async () => {
  const api = zoneWith("api.loomplane.com", [host("v1.api.loomplane.com")], {
    parent: "loomplane.com",
  })
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [host("www.loomplane.com")], {
        subdomains: ["api.loomplane.com"],
      }),
      { subdomains: [api] }
    ),
  })
  const view = open("zone-loomplane.com")
  const danger = await section("Remove domain")
  await userEvent.click(
    within(danger).getByRole("button", { name: "Remove domain" })
  )
  const { dialog, confirm, field } = await confirmRemoval("loomplane.com")
  expect(within(dialog).getByText("Remove these hostnames first.")).toBeTruthy()
  for (const h of ["www.loomplane.com", "v1.api.loomplane.com"])
    expect(
      within(dialog).getByRole("link", { name: h }).getAttribute("href")
    ).toBe("/projects/shop")
  await userEvent.type(field, "loomplane.com")
  expect(confirm).toHaveProperty("disabled", true)
  view.unmount()
  forgeClient.clear()

  // One the page didn't know about yet: the API refuses with it.
  const writes = stubFetch({
    [detailPath("loomplane.com")]: detailWith(zoneWith("loomplane.com")),
    "POST /api/v1/dns-zones/zone-loomplane.com/remove": () =>
      Response.json(
        {
          code: "zone_in_use",
          message: "Hostnames still use loomplane.com.",
          details: {
            hostnames: [
              {
                route_id: "r9",
                hostname: "new.loomplane.com",
                zone_id: "zone-loomplane.com",
                zone_name: "loomplane.com",
                project_id: "blog",
                project_name: "acme-blog",
              },
            ],
          },
        },
        { status: 409 }
      ),
  })
  open("zone-loomplane.com")
  await userEvent.click(
    within(await section("Remove domain")).getByRole("button", {
      name: "Remove domain",
    })
  )
  const again = await confirmRemoval("loomplane.com")
  await userEvent.type(again.field, "loomplane.com")
  await userEvent.click(again.confirm)
  expect(
    (
      await within(again.dialog).findByRole("link", {
        name: "new.loomplane.com",
      })
    ).getAttribute("href")
  ).toBe("/projects/blog")
  expect(
    within(again.dialog).getByText("Remove these hostnames first.")
  ).toBeTruthy()
  expect(again.confirm).toHaveProperty("disabled", true)
  expect(writes).toHaveLength(1)
  expect(navigate).not.toHaveBeenCalled()
})

it("lists the subdomains that go with the domain", async () => {
  const api = zoneWith("api.loomplane.com", [], { parent: "loomplane.com" })
  const eu = zoneWith("eu.loomplane.com", [], { parent: "loomplane.com" })
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [], {
        subdomains: ["api.loomplane.com", "eu.loomplane.com"],
      }),
      { subdomains: [api, eu] }
    ),
  })
  open("zone-loomplane.com")
  // The header's menu offers it too.
  const actions = await screen.findByRole("group", { name: "Domain actions" })
  await userEvent.click(
    within(actions).getByRole("button", { name: "More domain actions" })
  )
  await userEvent.click(
    await screen.findByRole("menuitem", { name: "Remove domain" })
  )
  const { dialog, confirm, field } = await confirmRemoval("loomplane.com")
  const goes = within(dialog).getByRole("list", {
    name: "Subdomains removed with it",
  })
  expect(
    within(goes)
      .getAllByRole("listitem")
      .map((li) => li.textContent)
  ).toEqual(["api.loomplane.com", "eu.loomplane.com"])
  await userEvent.type(field, "loomplane.com")
  expect(confirm).toHaveProperty("disabled", false)
})

it("says a registered domain stays registered at Cloudflare", async () => {
  stubFetch({
    [detailPath("acme.shop")]: detailWith(
      zoneWith("acme.shop", [], { registration: "reg" }),
      { registration: registration() }
    ),
  })
  open("zone-acme.shop")
  await userEvent.click(
    within(await section("Remove domain")).getByRole("button", {
      name: "Remove domain",
    })
  )
  const { dialog, confirm, field } = await confirmRemoval("acme.shop")
  expect(
    within(dialog).getByText(
      /The registration stays in your Cloudflare account and auto-renew is unchanged\./
    )
  ).toBeTruthy()
  expect(
    within(dialog)
      .getByRole("link", { name: "Open in Cloudflare" })
      .getAttribute("href")
  ).toBe("https://dash.cloudflare.com/acct/domains/registrations")
  // It still asks for the name.
  expect(confirm).toHaveProperty("disabled", true)
  await userEvent.type(field, "acme.shop")
  expect(confirm).toHaveProperty("disabled", false)
})

it("removes a subdomain from its domain's page", async () => {
  let reads = 0
  let removed = false
  const api = zoneWith("api.loomplane.com", [], { parent: "loomplane.com" })
  const writes = stubFetch({
    [detailPath("loomplane.com")]: () => {
      reads++
      return Response.json(
        detailWith(
          zoneWith("loomplane.com", [], {
            subdomains: removed ? [] : ["api.loomplane.com"],
          }),
          { subdomains: removed ? [] : [api] }
        )
      )
    },
    "POST /api/v1/dns-zones/zone-api.loomplane.com/remove": () => {
      removed = true
      return new Response(null, { status: 204 })
    },
  })
  open("zone-loomplane.com")
  const subdomains = await section("Subdomains")
  const row = within(subdomains).getByRole("row", {
    name: /^api\.loomplane\.com/,
  })
  await userEvent.click(within(row).getByRole("button", { name: "Remove" }))
  const { confirm, field } = await confirmRemoval("api.loomplane.com")
  await userEvent.type(field, "api.loomplane.com")
  await userEvent.click(confirm)
  await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/dns-zones/zone-api.loomplane.com/remove",
      body: { revision: 2 },
    },
  ])
  // It stays on the domain's page, read again without the subdomain.
  expect(navigate).not.toHaveBeenCalled()
  await waitFor(() => expect(reads).toBeGreaterThan(1))
  expect(await screen.findByText("No subdomains yet.")).toBeTruthy()
})
