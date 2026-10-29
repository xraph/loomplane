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

import { useDomainsOverview } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { forgeClient } from "@/client/runtime"
import {
  detailPath,
  detailWith,
  host,
  inTwelveDays,
  overviewWith,
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
  // With no subdomains, there's nothing to say about theirs.
  expect(screen.queryByText("Subdomains have their own projects.")).toBeNull()
  expect(await section("DNS records Loomplane manages")).toBeTruthy()
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
  // Check again shows once, by the record it checks.
  const actions = screen.getByRole("group", { name: "Domain actions" })
  expect(
    within(actions).queryByRole("button", { name: "Check again" })
  ).toBeNull()
  expect(screen.getAllByRole("button", { name: "Check again" })).toHaveLength(1)
  await userEvent.click(
    within(status).getByRole("button", { name: "Check again" })
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

it("lets a subdomain waiting on its domain verify on its own", async () => {
  const domain = zoneWith("loomplane.com", [], {
    external: true,
    subdomains: ["api.loomplane.com"],
  })
  let own = false
  let reads = 0
  const writes = stubFetch({
    [detailPath("api.loomplane.com")]: () => {
      reads++
      return Response.json(
        own
          ? detailWith(
              zoneWith("api.loomplane.com", [], {
                external: true,
                verified: false,
                parent: "loomplane.com",
              }),
              { parent: domain }
            )
          : detailWith(
              zoneWith("api.loomplane.com", [], {
                external: true,
                proof: false,
                verified: false,
                parent: "loomplane.com",
                proofZone: "loomplane.com",
              }),
              { parent: domain, proofZone: domain }
            )
      )
    },
    "POST /api/v1/dns-zones/zone-api.loomplane.com/update": () => {
      own = true
      return Response.json(
        zoneWith("api.loomplane.com", [], { external: true, verified: false })
          .zone
      )
    },
  })
  open("zone-api.loomplane.com")
  const status = await section("Status")
  await userEvent.click(
    within(status).getByRole("button", { name: "Verify it on its own instead" })
  )
  expect(writes).toEqual([
    {
      method: "POST",
      path: "/api/v1/dns-zones/zone-api.loomplane.com/update",
      body: { project_ids: ["shop"], renew_proof: true, revision: 2 },
    },
  ])
  // Read again, it shows its own record to add.
  expect(
    await within(status).findByText("_loomplane.api.loomplane.com")
  ).toBeTruthy()
  expect(reads).toBe(2)
  expect(within(status).queryByText(/waits for/)).toBeNull()
  expect(
    within(status).queryByRole("button", {
      name: "Verify it on its own instead",
    })
  ).toBeNull()
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

it("checks a verified domain's TXT record again", async () => {
  const writes = stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [], { proof: true, published: true })
    ),
    "POST /api/v1/dns-zones/zone-loomplane.com/verify": () =>
      Response.json({ message: "the record is gone" }, { status: 409 }),
  })
  open("zone-loomplane.com")
  const status = await section("Status")
  expect(within(status).getByText(/^Verified\./)).toBeTruthy()
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
      path: "/api/v1/dns-zones/zone-loomplane.com/verify",
      body: undefined,
    },
  ])
})

it("says a verified domain's TXT record is gone and publishes it again", async () => {
  const gone = zoneWith("loomplane.com", [], {
    proof: true,
    published: true,
    missing: true,
  })
  // Long past the 24 hours a pending proof has, which doesn't matter here.
  gone.zone.proof_expires_at = "2026-10-02T00:00:00Z"
  const writes = stubFetch({
    [detailPath("loomplane.com")]: detailWith(gone),
    "POST /api/v1/dns-zones/zone-loomplane.com/verify": () =>
      Response.json({ message: "the record is gone" }, { status: 409 }),
  })
  open("zone-loomplane.com")
  const status = await section("Status")
  expect(screen.getByText("TXT record missing")).toBeTruthy()
  expect(
    within(status).getByText(
      /can't find _loomplane\.loomplane\.com in public DNS any more/
    )
  ).toBeTruthy()
  expect(within(status).queryByRole("button", { name: /Renew/ })).toBeNull()
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
      path: "/api/v1/dns-zones/zone-loomplane.com/verify",
      body: undefined,
    },
  ])
})

it("checks the domain whose record a subdomain lost", async () => {
  const domain = zoneWith("loomplane.com", [], {
    proof: true,
    published: true,
    missing: true,
    subdomains: ["app.loomplane.com"],
  })
  const writes = stubFetch({
    [detailPath("app.loomplane.com")]: detailWith(
      zoneWith("app.loomplane.com", [], {
        parent: "loomplane.com",
        proofZone: "loomplane.com",
        missing: true,
      }),
      { parent: domain, proofZone: domain }
    ),
    "POST /api/v1/dns-zones/zone-loomplane.com/verify": () =>
      Response.json({ message: "the record is gone" }, { status: 409 }),
  })
  open("zone-app.loomplane.com")
  const status = await section("Status")
  expect(
    within(status).getByText(/can't find _loomplane\.loomplane\.com/)
  ).toBeTruthy()
  expect(
    within(status).getByText("This is loomplane.com's record.")
  ).toBeTruthy()
  // Its domain's record is what's gone, so a proof of its own isn't offered.
  expect(
    within(status).queryByRole("button", {
      name: "Verify it on its own instead",
    })
  ).toBeNull()
  await userEvent.click(
    within(status).getByRole("button", { name: "Check again" })
  )
  await within(status).findByText(
    "Checked just now. The record isn't visible yet."
  )
  expect(writes.map((w) => w.path)).toEqual([
    "/api/v1/dns-zones/zone-loomplane.com/verify",
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
  let reads = 0
  const writes = stubFetch({
    [detailPath("loomplane.com")]: () => {
      reads++
      return Response.json(detailWith(zoneWith("loomplane.com")))
    },
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
              // A route whose project is gone has nowhere to link to.
              {
                route_id: "",
                hostname: "old.loomplane.com",
                zone_id: "zone-loomplane.com",
                zone_name: "loomplane.com",
                project_id: "",
                project_name: "",
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
  expect(
    within(again.dialog).queryByRole("link", { name: "old.loomplane.com" })
  ).toBeNull()
  expect(within(again.dialog).getByText("old.loomplane.com")).toBeTruthy()
  expect(again.confirm).toHaveProperty("disabled", true)
  expect(writes).toHaveLength(1)
  expect(navigate).not.toHaveBeenCalled()
  // The page reads its hostnames again too.
  await waitFor(() => expect(reads).toBe(2))
})

// Reads the domains list alongside the page, as the app shell would.
function OverviewProbe() {
  useQuery(useDomainsOverview)
  return null
}

it("reads the page and the domains list again after a removal fails part way", async () => {
  let reads = 0
  let lists = 0
  const api = zoneWith("api.loomplane.com", [], { parent: "loomplane.com" })
  stubFetch({
    [detailPath("loomplane.com")]: () => {
      reads++
      return Response.json(
        detailWith(
          zoneWith("loomplane.com", [], { subdomains: ["api.loomplane.com"] }),
          { subdomains: [api] }
        )
      )
    },
    "/api/v1/domains": () => {
      lists++
      return Response.json(overviewWith())
    },
    // api.loomplane.com went, then cleaning loomplane.com failed.
    "POST /api/v1/dns-zones/zone-loomplane.com/remove": () =>
      Response.json({ message: "provider unavailable" }, { status: 502 }),
  })
  render(
    <ClientProvider client={forgeClient}>
      <OverviewProbe />
      <DomainDetailPage zoneId="zone-loomplane.com" />
    </ClientProvider>
  )
  await userEvent.click(
    within(await section("Remove domain")).getByRole("button", {
      name: "Remove domain",
    })
  )
  const { dialog, confirm, field } = await confirmRemoval("loomplane.com")
  await waitFor(() => expect(lists).toBe(1))
  await userEvent.type(field, "loomplane.com")
  await userEvent.click(confirm)
  expect(await within(dialog).findByText("Provider unavailable.")).toBeTruthy()
  await waitFor(() => expect(reads).toBe(2))
  await waitFor(() => expect(lists).toBe(2))
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

it("removes a subdomain from its own page and goes back to its domain", async () => {
  let reads = 0
  const domain = zoneWith("loomplane.com", [], {
    subdomains: ["api.loomplane.com"],
  })
  const writes = stubFetch({
    [detailPath("api.loomplane.com")]: () => {
      reads++
      return Response.json(
        detailWith(
          zoneWith("api.loomplane.com", [], {
            parent: "loomplane.com",
            proofZone: "loomplane.com",
          }),
          { parent: domain, proofZone: domain }
        )
      )
    },
    "POST /api/v1/dns-zones/zone-api.loomplane.com/remove": () =>
      new Response(null, { status: 204 }),
  })
  // The page reads nothing again until it has left the removed zone.
  let arrived = () => {}
  navigate.mockImplementation(
    () => new Promise<void>((resolve) => (arrived = resolve))
  )
  open("zone-api.loomplane.com")
  // A subdomain can have zones under it too.
  const actions = await screen.findByRole("group", { name: "Domain actions" })
  expect(
    within(actions).getByRole("button", { name: "Add subdomain" })
  ).toBeTruthy()
  await userEvent.click(
    within(await section("Remove subdomain")).getByRole("button", {
      name: "Remove subdomain",
    })
  )
  const { confirm, field } = await confirmRemoval("api.loomplane.com")
  await userEvent.type(field, "api.loomplane.com")
  await userEvent.click(confirm)
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/domains/$zoneId",
      params: { zoneId: "zone-loomplane.com" },
      replace: true,
    })
  )
  expect(reads).toBe(1)
  arrived()
  await waitFor(() => expect(reads).toBe(2))
  expect(writes).toHaveLength(1)
})

it("counts a removal done when leaving the page fails", async () => {
  let reads = 0
  stubFetch({
    [detailPath("loomplane.com")]: () => {
      reads++
      return Response.json(detailWith(zoneWith("loomplane.com")))
    },
    "POST /api/v1/dns-zones/zone-loomplane.com/remove": () =>
      new Response(null, { status: 204 }),
  })
  navigate.mockImplementation(() =>
    Promise.reject(new Error("navigation failed"))
  )
  open("zone-loomplane.com")
  await userEvent.click(
    within(await section("Remove domain")).getByRole("button", {
      name: "Remove domain",
    })
  )
  const { confirm, field } = await confirmRemoval("loomplane.com")
  await userEvent.type(field, "loomplane.com")
  await userEvent.click(confirm)
  await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1))
  // The page still reads again, and nothing says the removal failed.
  await waitFor(() => expect(reads).toBe(2))
  await userEvent.click(
    within(await section("Remove domain")).getByRole("button", {
      name: "Remove domain",
    })
  )
  const again = await screen.findByRole("alertdialog")
  expect(within(again).queryByRole("alert")).toBeNull()
})

it("sends one removal for a double click", async () => {
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(zoneWith("loomplane.com")),
  })
  let removals = 0
  let answer = () => {}
  vi.stubGlobal(
    "fetch",
    (
      (next: typeof fetch) =>
      async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).endsWith("/remove")) {
          removals++
          return new Promise<Response>(
            (resolve) =>
              (answer = () => resolve(new Response(null, { status: 204 })))
          )
        }
        return next(input, init)
      }
    )(fetch)
  )
  open("zone-loomplane.com")
  await userEvent.click(
    within(await section("Remove domain")).getByRole("button", {
      name: "Remove domain",
    })
  )
  const { confirm, field } = await confirmRemoval("loomplane.com")
  await userEvent.type(field, "loomplane.com")
  await userEvent.dblClick(confirm)
  expect(removals).toBe(1)
  answer()
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({ to: "/domains", replace: true })
  )
  expect(removals).toBe(1)
})

it("names the zone a new subdomain is verified through", async () => {
  const domain = zoneWith("loomplane.com", [], {
    subdomains: ["app.loomplane.com"],
  })
  stubFetch({
    [detailPath("app.loomplane.com")]: detailWith(
      zoneWith("app.loomplane.com", [], {
        parent: "loomplane.com",
        proofZone: "loomplane.com",
      }),
      { parent: domain, proofZone: domain }
    ),
  })
  open("zone-app.loomplane.com")
  const dialog = await addSubdomain("x")
  expect(
    within(dialog).getByText(/^It's verified through loomplane\.com,/)
  ).toBeTruthy()
})

it("sends one new subdomain for a double click", async () => {
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(zoneWith("loomplane.com")),
  })
  let creates = 0
  let answer = () => {}
  vi.stubGlobal(
    "fetch",
    (
      (next: typeof fetch) =>
      async (input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === "POST" && String(input).endsWith("/dns-zones")) {
          creates++
          return new Promise<Response>(
            (resolve) =>
              (answer = () =>
                resolve(
                  Response.json(
                    zoneWith("app.loomplane.com", [], {
                      parent: "loomplane.com",
                    }).zone
                  )
                ))
          )
        }
        return next(input, init)
      }
    )(fetch)
  )
  open("zone-loomplane.com")
  const dialog = await addSubdomain("app")
  await userEvent.dblClick(
    within(dialog).getByRole("button", { name: "Add subdomain" })
  )
  expect(creates).toBe(1)
  answer()
  await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1))
  expect(creates).toBe(1)
})

/** record is a DNS record Loomplane wrote, as the API lists it. */
function record(
  name: string,
  over: Record<string, unknown> = {}
): Record<string, unknown> {
  return {
    kind: "route",
    zone_id: "zone-acme.dev",
    type: "CNAME",
    name,
    value: "edge.acme-hosting.net",
    ttl: 60,
    ...over,
  }
}

const proofRecord = (zone: string) =>
  record(`_loomplane.${zone}`, {
    kind: "proof",
    zone_id: `zone-${zone}`,
    type: "TXT",
    value: "lp-proof-123",
  })

const records = () => section("DNS records Loomplane manages")

it("lists the TXT proof and each route's record Loomplane wrote", async () => {
  stubFetch({
    [detailPath("acme.dev")]: detailWith(
      zoneWith("acme.dev", [host("api.acme.dev")], { published: true }),
      {
        records: [
          record("api.acme.dev", {
            type: "A",
            value: "203.0.113.10",
            route_id: "route-acme.dev-0",
            hostname: "api.acme.dev",
            project_id: "shop",
            project_name: "acme-shop",
          }),
          // A route whose project is gone.
          record("old.acme.dev", {
            route_id: "route-old",
            hostname: "old.acme.dev",
          }),
          proofRecord("acme.dev"),
        ],
      }
    ),
  })
  open("zone-acme.dev")
  const dns = await records()
  const headers = within(dns)
    .getAllByRole("columnheader")
    .map((h) => h.textContent)
  expect(headers).toEqual(["Type", "Name", "Value", "TTL", "Owned by"])
  const rows = within(dns).getAllByRole("row").slice(1)
  // The proof first, then by name.
  expect(
    rows.map((r) =>
      within(r)
        .getAllByRole("cell")
        .map((c) => c.textContent)
    )
  ).toEqual([
    ["TXT", "_loomplane.acme.dev", "lp-proof-123", "60", "Domain proof"],
    ["A", "api.acme.dev", "203.0.113.10", "60", "api.acme.dev"],
    ["CNAME", "old.acme.dev", "edge.acme-hosting.net", "60", "old.acme.dev"],
  ])
  expect(
    within(rows[1])
      .getByRole("link", { name: "api.acme.dev" })
      .getAttribute("href")
  ).toBe("/projects/shop")
  // With no project, there's nowhere to link to.
  expect(within(rows[2]).queryByRole("link")).toBeNull()
})

it("shows a Subdomain column when records span zones", async () => {
  const api = zoneWith("api.loomplane.com", [host("v1.api.loomplane.com")], {
    parent: "loomplane.com",
  })
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [], { subdomains: ["api.loomplane.com"] }),
      {
        subdomains: [api],
        records: [
          record("v1.api.loomplane.com", {
            zone_id: "zone-api.loomplane.com",
            route_id: "route-api.loomplane.com-0",
            hostname: "v1.api.loomplane.com",
            project_id: "shop",
            project_name: "acme-shop",
          }),
          proofRecord("loomplane.com"),
        ],
      }
    ),
  })
  open("zone-loomplane.com")
  const dns = await records()
  expect(
    within(dns)
      .getAllByRole("columnheader")
      .map((h) => h.textContent)
  ).toEqual(["Type", "Name", "Value", "TTL", "Subdomain", "Owned by"])
  const v1 = within(dns).getByRole("row", { name: /v1\.api\.loomplane\.com/ })
  expect(within(v1).getByText("api.loomplane.com")).toBeTruthy()
  const proof = within(dns).getByRole("row", { name: /^TXT/ })
  expect(within(proof).getByText("loomplane.com")).toBeTruthy()
})

it("names a record's zone by its ID when the page doesn't list it", async () => {
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(zoneWith("loomplane.com"), {
      records: [
        record("v1.gone.loomplane.com", {
          zone_id: "zone-gone",
          route_id: "route-gone",
          hostname: "v1.gone.loomplane.com",
        }),
        proofRecord("loomplane.com"),
      ],
    }),
  })
  open("zone-loomplane.com")
  const dns = await records()
  const v1 = within(dns).getByRole("row", { name: /v1\.gone\.loomplane\.com/ })
  expect(
    within(v1)
      .getAllByRole("cell")
      .map((c) => c.textContent)[4]
  ).toBe("zone-gone")
})

it("shows no Subdomain column for records in one zone", async () => {
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(
      zoneWith("loomplane.com", [], { subdomains: ["api.loomplane.com"] }),
      {
        subdomains: [
          zoneWith("api.loomplane.com", [], { parent: "loomplane.com" }),
        ],
        records: [proofRecord("loomplane.com")],
      }
    ),
  })
  open("zone-loomplane.com")
  const dns = await records()
  expect(
    within(dns).queryByRole("columnheader", { name: "Subdomain" })
  ).toBeNull()
})

it("says Loomplane writes no records for a domain whose DNS stays elsewhere", async () => {
  stubFetch({
    [detailPath("docs-example.org")]: detailWith(
      zoneWith("docs-example.org", [], { external: true })
    ),
  })
  open("zone-docs-example.org")
  const dns = await records()
  expect(
    within(dns).getByText(
      "Loomplane writes no records here. You add each hostname's record where the domain is hosted."
    )
  ).toBeTruthy()
  expect(within(dns).queryByRole("table")).toBeNull()
})

it("says whose projects a domain's and a subdomain's grants reach", async () => {
  const api = zoneWith("api.loomplane.com", [], { parent: "loomplane.com" })
  const domain = zoneWith("loomplane.com", [], {
    subdomains: ["api.loomplane.com"],
  })
  stubFetch({
    [detailPath("loomplane.com")]: detailWith(domain, { subdomains: [api] }),
    [detailPath("api.loomplane.com")]: detailWith(api, { parent: domain }),
  })
  const view = open("zone-loomplane.com")
  let access = await section("Access")
  expect(
    within(access).getByText("Subdomains have their own projects.")
  ).toBeTruthy()
  view.unmount()

  open("zone-api.loomplane.com")
  access = await section("Access")
  expect(
    within(access).getByText(
      "Projects granted loomplane.com don't get this subdomain."
    )
  ).toBeTruthy()
  expect(
    within(access).queryByText("Subdomains have their own projects.")
  ).toBeNull()
})

it("says Loomplane writes records when a subdomain's DNS is managed", async () => {
  // The domain's DNS stays elsewhere, but Loomplane manages its subdomain's.
  const app = zoneWith("app.docs-example.org", [], {
    parent: "docs-example.org",
  })
  stubFetch({
    [detailPath("docs-example.org")]: detailWith(
      zoneWith("docs-example.org", [], {
        external: true,
        subdomains: ["app.docs-example.org"],
      }),
      { subdomains: [app] }
    ),
  })
  open("zone-docs-example.org")
  const dns = await records()
  expect(
    within(dns).getByText("Loomplane hasn't written any records here yet.")
  ).toBeTruthy()
})

it("says Loomplane hasn't written records to a managed domain yet", async () => {
  stubFetch({
    [detailPath("acme.dev")]: detailWith(zoneWith("acme.dev")),
  })
  open("zone-acme.dev")
  const dns = await records()
  expect(
    within(dns).getByText("Loomplane hasn't written any records here yet.")
  ).toBeTruthy()
  expect(within(dns).queryByRole("table")).toBeNull()
})
