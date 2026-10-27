import { describe, expect, it } from "vitest"

import type {
  CloudinfraRouteStatus,
  DomainOffer,
  HostnameUse,
  Registration,
  ZoneOverview,
} from "@/client/forge/loomplane/types"
import {
  certificateLabel,
  dnsLabel,
  dnsTone,
  domainGroups,
  domainRollup,
  formatDay,
  formatPrice,
  grantsLine,
  groupNeedsAttention,
  hostnameInZone,
  isWildcardHostname,
  offerLabel,
  offerStatus,
  offerTone,
  ownedNames,
  parentZoneOf,
  proofPublishState,
  registrationLine,
  registrationStatusLine,
  routeLabel,
  runsOnLabel,
  sameHosting,
  stateTone,
  usedByLabel,
  zoneDNSLabel,
  zoneNeedsAttention,
} from "./domains"

function status(
  over: Partial<CloudinfraRouteStatus> = {}
): CloudinfraRouteStatus {
  return {
    backend: "server",
    certificate: "ready",
    certificateExpiresAt: "",
    checkedAt: "",
    dns: "ready",
    hostname: "api.acme.dev",
    retryable: false,
    route: "ready",
    routeId: "route",
    wildcard: false,
    ...over,
  }
}

function use(over: Partial<CloudinfraRouteStatus> = {}): HostnameUse {
  return {
    environmentId: "env-prod",
    hostname: "api.acme.dev",
    port: 8080,
    projectId: "shop",
    projectName: "acme-shop",
    routeId: "route",
    runsOn: "web-1",
    service: "edge",
    status: status(over),
  }
}

function zone(
  over: Partial<ZoneOverview> = {},
  verified = true,
  mode = "managed"
): ZoneOverview {
  return {
    hostnames: [],
    subdomains: [],
    state: verified ? "ready" : "awaiting_verification",
    wildcards: false,
    zone: {
      id: "zone",
      mode,
      name: "acme.dev",
      orgId: "org",
      projectIds: [],
      proofCheckedAt: "0001-01-01T00:00:00Z",
      proofExpiresAt: "2026-10-02T00:00:00Z",
      proofName: "_loomplane.acme.dev",
      proofPublishAttemptedAt: "0001-01-01T00:00:00Z",
      proofValue: "loomplane=zone-token",
      provider: mode === "managed" ? "cloudflare" : undefined,
      providerZone: {} as never,
      recordOwnership: true,
      revision: 1,
      verifiedAt: verified ? "2026-10-01T00:00:00Z" : "0001-01-01T00:00:00Z",
    },
    ...over,
  }
}

function registration(over: Partial<Registration> = {}): Registration {
  return {
    accountId: "acct",
    attempt: 1,
    attempted: true,
    autoRenew: true,
    connectionId: "cf",
    createdAt: "",
    customRegistrant: false,
    domainExpiresAt: "2026-11-19T10:00:00Z",
    firstUnconfirmedAt: "",
    id: "reg",
    message: "",
    name: "acme.shop",
    noticeSentFor: "",
    orgId: "org",
    privacyMode: "redaction",
    projectIds: [],
    provider: "cloudflare",
    quote: { currency: "USD", registrationCost: "10.11", renewalCost: "10.11" },
    refreshedAt: "",
    resultNotifiedFor: "",
    review: "",
    reviewExpiresAt: "",
    revision: 3,
    sandbox: false,
    status: "succeeded",
    updatedAt: "",
    zoneId: "zone",
    ...over,
  }
}

describe("state labels", () => {
  it("names the DNS column", () => {
    expect([dnsLabel(status()), dnsTone(status())]).toEqual(["Ready", "ok"])
    expect([
      dnsLabel(status({ dns: "provisioning" })),
      dnsTone(status({ dns: "provisioning" })),
    ]).toEqual(["Provisioning", "warn"])
    const record = status({
      dns: "awaiting_verification",
      certificate: "provisioning",
      route: "provisioning",
    })
    expect([dnsLabel(record), dnsTone(record)]).toEqual([
      "Awaiting record",
      "warn",
    ])
  })

  it("names the certificate column", () => {
    expect(certificateLabel(status())).toBe("Active")
    expect(certificateLabel(status({ certificate: "provisioning" }))).toBe(
      "Issuing (HTTP-01)"
    )
    expect(
      certificateLabel(status({ certificate: "provisioning", wildcard: true }))
    ).toBe("Issuing (DNS-01)")
    expect(certificateLabel(status({ certificate: "renewing" }))).toBe(
      "Renewing"
    )
    expect(stateTone("renewing")).toBe("warn")
    expect(stateTone("provisioning")).toBe("warn")
  })

  it("names the route column", () => {
    expect(routeLabel(status())).toBe("Ready")
    expect(
      routeLabel(status({ certificate: "provisioning", route: "provisioning" }))
    ).toBe("Waiting for certificate")
    expect(
      routeLabel(
        status({
          dns: "provisioning",
          certificate: "ready",
          route: "provisioning",
        })
      )
    ).toBe("Waiting for DNS")
    expect(routeLabel(status({ route: "provisioning" }))).toBe("Provisioning")
  })

  it("names the states every column shares", () => {
    const unverified = status({
      dns: "awaiting_verification",
      certificate: "awaiting_verification",
      route: "awaiting_verification",
    })
    for (const label of [dnsLabel, certificateLabel, routeLabel]) {
      expect(label(unverified)).toBe("Awaiting verification")
    }
    expect(dnsTone(unverified)).toBe("idle")
    expect(stateTone("awaiting_verification")).toBe("idle")

    const failed = status({
      dns: "failed",
      certificate: "failed",
      route: "failed",
    })
    for (const label of [dnsLabel, certificateLabel, routeLabel]) {
      expect(label(failed)).toBe("Failed")
    }
    expect(stateTone("failed")).toBe("failed")

    const removing = status({
      dns: "cleanup_pending",
      certificate: "cleanup_pending",
      route: "cleanup_pending",
    })
    for (const label of [dnsLabel, certificateLabel, routeLabel]) {
      expect(label(removing)).toBe("Removing")
    }
    expect(stateTone("cleanup_pending")).toBe("idle")
    expect(stateTone("ready")).toBe("ok")
  })
})

describe("zones", () => {
  it("needs attention unless everything is ready", () => {
    expect(zoneNeedsAttention(zone({ hostnames: [use()] }))).toBe(false)
    expect(zoneNeedsAttention(zone({}, false, "external"))).toBe(true)
    expect(
      zoneNeedsAttention(
        zone({ hostnames: [use(), use({ certificate: "renewing" })] })
      )
    ).toBe(true)
    expect(zoneNeedsAttention(zone(), registration())).toBe(false)
    expect(
      zoneNeedsAttention(zone(), registration({ status: "zone_pending" }))
    ).toBe(true)
  })

  it("labels the zone's DNS", () => {
    expect(zoneDNSLabel(zone())).toBe("Managed by Cloudflare")
    expect(zoneDNSLabel(zone({}, false, "external"))).toBe(
      "Awaiting TXT record"
    )
    // Loomplane publishes a managed zone's proof itself.
    expect(zoneDNSLabel(zone({}, false))).toBe("Verifying")
    const legacy = zone({}, false)
    legacy.zone = { ...legacy.zone!, proofName: undefined }
    expect(zoneDNSLabel(legacy)).toBe("Awaiting verification")
  })

  it("follows the TXT record Loomplane publishes for a managed zone", () => {
    const z = zone({}, false).zone!
    expect(proofPublishState(z)).toBe("publishing")
    expect(proofPublishState({ ...z, proofPublishError: "Denied." })).toBe(
      "failed"
    )
    const record = {
      id: "rec",
      zoneId: "cf",
      name: "_loomplane",
      type: "TXT",
      value: z.proofValue!,
      ttl: 60,
    }
    expect(proofPublishState({ ...z, proofRecord: record })).toBe("waiting")
    // A record Loomplane couldn't confirm is still there says why.
    expect(
      proofPublishState({
        ...z,
        proofRecord: record,
        proofPublishError: "Couldn't read the records.",
      })
    ).toBe("failed")
    // A write whose answer was lost isn't published until it reconciles.
    expect(
      proofPublishState({
        ...z,
        proofRecord: record,
        proofPublishAttempted: true,
      })
    ).toBe("publishing")
    // A renewed value replaces the published one.
    expect(
      proofPublishState({ ...z, proofRecord: { ...record, value: "old" } })
    ).toBe("publishing")
  })

  it("says where the domain was registered", () => {
    expect(registrationLine(zone())).toBe("Registered elsewhere")
    expect(registrationLine(zone(), registration())).toBe(
      "Registered through Loomplane · renews Nov 19, 2026"
    )
    expect(registrationLine(zone(), registration({ autoRenew: false }))).toBe(
      "Registered through Loomplane · expires Nov 19, 2026"
    )
  })

  it("words a registration still in flight or ended", () => {
    expect(
      registrationStatusLine(registration({ status: "registering" }))
    ).toBe("Cloudflare is registering this domain.")
    expect(
      registrationStatusLine(registration({ status: "zone_pending" }))
    ).toBe("Registered. Waiting for Cloudflare to activate the zone.")
    expect(
      registrationStatusLine(
        registration({ status: "failed", message: "card declined" })
      )
    ).toBe("Cloudflare declined the registration: card declined")
    expect(
      registrationStatusLine(registration({ status: "price_changed" }))
    ).toBe(
      "The price changed before purchase. Nothing was charged. Register it again to see the new price."
    )
  })
})

describe("hostnames", () => {
  it("says where a hostname runs and what uses it", () => {
    expect(runsOnLabel(use())).toBe("Docker · web-1")
    expect(runsOnLabel(use({ backend: "kubernetes" }))).toBe(
      "Kubernetes · web-1"
    )
    expect(usedByLabel(use(), "production")).toBe(
      "acme-shop / production / edge"
    )
  })

  it("keeps hostnames inside their zone", () => {
    expect(hostnameInZone("api.acme.dev", "acme.dev")).toBe(true)
    expect(hostnameInZone("acme.dev", "acme.dev")).toBe(true)
    expect(hostnameInZone("acme.dev.evil", "acme.dev")).toBe(false)
    expect(hostnameInZone("notacme.dev", "acme.dev")).toBe(false)
    expect(isWildcardHostname("*.acme.dev")).toBe(true)
    expect(isWildcardHostname("api.acme.dev")).toBe(false)
  })
})

describe("formatting", () => {
  it("formats prices and days like the mock", () => {
    expect(formatPrice("USD", "10.11")).toBe("USD 10.11")
    expect(formatPrice("USD", "")).toBe("")
    expect(formatDay("2026-11-19T10:00:00Z")).toBe("Nov 19, 2026")
  })
})

describe("offers", () => {
  const extensions = ["com", "app", "dev", "shop", "ai", "co.uk"]
  const offer = (over: Partial<DomainOffer> = {}): DomainOffer => ({
    name: "acmeshop.app",
    registrable: true,
    price: { currency: "USD", registrationCost: "10.11", renewalCost: "10.11" },
    ...over,
  })

  it("labels each offer like the mock", () => {
    const owned = new Set(["acmeshop.shop"])
    const cases: [DomainOffer, string][] = [
      [offer(), "available"],
      [
        offer({
          name: "acmeshop.dev",
          registrable: false,
          reason: "domain_unavailable",
          price: undefined,
        }),
        "taken",
      ],
      [
        offer({
          name: "acmeshop.shop",
          registrable: false,
          reason: "domain_unavailable",
        }),
        "owned",
      ],
      [
        offer({
          name: "acmeshop.ai",
          registrable: false,
          reason: "extension_not_supported_via_api",
        }),
        "unsupported",
      ],
      [
        offer({
          name: "acmeshop.ai",
          registrable: false,
          reason: "extension_not_supported",
        }),
        "unsupported",
      ],
      [
        offer({
          name: "acmeshop.ai",
          registrable: false,
          reason: "extension_disallows_registration",
        }),
        "unsupported",
      ],
      [offer({ name: "acmeshop.co.uk" }), "available"],
    ]
    for (const [o, want] of cases)
      expect([o.name, o.reason, offerStatus(o, owned, extensions)]).toEqual([
        o.name,
        o.reason,
        want,
      ])
    expect(offerLabel("available")).toBe("Available")
    expect(offerLabel("taken")).toBe("Taken")
    expect(offerLabel("owned")).toBe("You own this")
    expect(offerLabel("unsupported")).toBe("Not offered through the API yet")
    expect(offerTone("available")).toBe("ok")
    expect(offerTone("owned")).toBe("own")
    expect(offerTone("taken")).toBe("idle")
    expect(offerTone("unsupported")).toBe("idle")
  })

  it("takes the extensions from the API, not a list of its own", () => {
    const none = new Set<string>()
    expect(offerStatus(offer({ name: "acmeshop.io" }), none, extensions)).toBe(
      "unsupported"
    )
    expect(offerStatus(offer({ name: "acmeshop.io" }), none, ["io"])).toBe(
      "available"
    )
    expect(offerStatus(offer({ name: "ACMESHOP.APP" }), none, [".app"])).toBe(
      "available"
    )
  })

  it("owns the names that are zones or registrations still held", () => {
    const reg = (name: string, status: string) =>
      ({ name, status }) as Registration
    const owned = ownedNames(
      [zone({}), zone({ zone: undefined })],
      [
        reg("Bought.app", "succeeded"),
        reg("buying.app", "registering"),
        reg("unsure.app", "unconfirmed"),
        reg("pending.app", "zone_pending"),
        reg("stuck.app", "action_required"),
        reg("held.app", "blocked"),
        reg("ordered.app", "approved"),
        reg("looked.app", "preview"),
        reg("lapsed.app", "expired"),
        reg("future.app", "some_new_status"),
        reg("declined.app", "failed"),
        reg("moved.app", "price_changed"),
      ]
    )
    expect([...owned].sort()).toEqual([
      "acme.dev",
      "bought.app",
      "buying.app",
      "held.app",
      "ordered.app",
      "pending.app",
      "stuck.app",
      "unsure.app",
    ])
  })
})

describe("domains and subdomains", () => {
  // A zone named name, with parent and subdomains given by name too.
  function named(
    name: string,
    over: Partial<ZoneOverview> = {},
    verified = true
  ): ZoneOverview {
    const z = zone(over, verified)
    z.zone = { ...z.zone!, id: `zone-${name}`, name }
    return z
  }
  const ids = (zs: ZoneOverview[]) => zs.map((z) => z.zone?.id)
  // Before the 2026-10-02 expiry the zone() proofs carry.
  const now = Date.parse("2026-10-01T12:00:00Z")

  it("groups zones under the root their parents reach", () => {
    const groups = domainGroups([
      named("x.app.loomplane.com", { parentZoneId: "zone-app.loomplane.com" }),
      named("b.dev"),
      named("app.loomplane.com", { parentZoneId: "zone-loomplane.com" }),
      named("loomplane.com"),
      // A parent the list doesn't hold can't swallow its child.
      named("lost.gone.dev", { parentZoneId: "zone-gone.dev" }),
    ])
    expect(groups.map((g) => g.domain.zone?.name)).toEqual([
      "b.dev",
      "loomplane.com",
      "lost.gone.dev",
    ])
    expect(ids(groups[1].subdomains)).toEqual([
      "zone-app.loomplane.com",
      "zone-x.app.loomplane.com",
    ])
    expect(groups[0].subdomains).toEqual([])
  })

  it("finds the parent on whole labels", () => {
    const zones = [named("loomplane.com"), named("app.loomplane.com")]
    expect(parentZoneOf("api.loomplane.com", zones)?.zone?.name).toBe(
      "loomplane.com"
    )
    // The longest suffix wins.
    expect(parentZoneOf("x.app.loomplane.com", zones)?.zone?.name).toBe(
      "app.loomplane.com"
    )
    expect(parentZoneOf("API.Loomplane.com", zones)?.zone?.name).toBe(
      "loomplane.com"
    )
    expect(parentZoneOf("notloomplane.com", zones)).toBeUndefined()
    expect(parentZoneOf("loomplane.com", zones)).toBeUndefined()
    expect(parentZoneOf("", zones)).toBeUndefined()
  })

  it("rolls up Ready, Verifying and Needs attention with a count", () => {
    const domain = named("loomplane.com", { hostnames: [use()] })
    const sub = (over: Partial<ZoneOverview> = {}, verified = true) =>
      named(
        "app.loomplane.com",
        {
          parentZoneId: "zone-loomplane.com",
          proofZoneId: "zone-loomplane.com",
          ...over,
        },
        verified
      )

    expect(
      domainRollup({ domain, subdomains: [sub()] }, registration(), now)
    ).toEqual({ label: "Ready", tone: "ok", count: 0 })

    // Waiting on the domain's record, or on a certificate.
    expect(
      domainRollup({ domain, subdomains: [sub({}, false)] }, undefined, now)
    ).toEqual({ label: "Verifying", tone: "warn", count: 0 })
    expect(
      domainRollup(
        {
          domain,
          subdomains: [sub({ hostnames: [use({ certificate: "renewing" })] })],
        },
        undefined,
        now
      ).label
    ).toBe("Verifying")

    // A failed hostname, an unpublished record and an unfinished
    // registration each count.
    const unpublished = named("loomplane.com", {}, false)
    unpublished.zone = {
      ...unpublished.zone!,
      proofPublishError: "The connection can't write DNS records.",
    }
    expect(
      domainRollup(
        {
          domain: unpublished,
          subdomains: [
            sub({
              hostnames: [
                use({ dns: "failed" }),
                use(),
                use({ route: "failed" }),
              ],
            }),
          ],
        },
        registration({ status: "action_required" }),
        now
      )
    ).toEqual({ label: "Needs attention", tone: "failed", count: 4 })

    // A proof that expired, and a zone with no proof to check at all.
    const expired = named("loomplane.com", {}, false)
    expect(
      domainRollup({ domain: expired, subdomains: [] }, undefined, now).label
    ).toBe("Verifying")
    expect(
      domainRollup(
        { domain: expired, subdomains: [] },
        undefined,
        Date.parse("2026-10-03T00:00:00Z")
      )
    ).toEqual({ label: "Needs attention", tone: "failed", count: 1 })
    const unproven = sub({ proofZoneId: undefined }, false)
    unproven.zone = { ...unproven.zone!, proofName: undefined }
    expect(
      domainRollup({ domain, subdomains: [unproven] }, undefined, now)
    ).toEqual({ label: "Needs attention", tone: "failed", count: 1 })
  })

  it("needs attention when the domain or any subdomain does", () => {
    const domain = named("loomplane.com")
    const ready = named("app.loomplane.com")
    const failing = named("api.loomplane.com", {
      hostnames: [use({ dns: "failed" })],
    })
    expect(groupNeedsAttention({ domain, subdomains: [ready] })).toBe(false)
    expect(groupNeedsAttention({ domain, subdomains: [ready, failing] })).toBe(
      true
    )
    expect(
      groupNeedsAttention(
        { domain, subdomains: [] },
        registration({ status: "zone_pending" })
      )
    ).toBe(true)
  })

  it("compares hosting by mode, provider and connection", () => {
    const a = named("loomplane.com").zone!
    expect(sameHosting(a, { ...a, name: "app.loomplane.com" })).toBe(true)
    expect(sameHosting(a, { ...a, provider: "vercel" })).toBe(false)
    expect(sameHosting(a, { ...a, connectionId: "other" })).toBe(false)
    expect(sameHosting(a, { ...a, mode: "external" })).toBe(false)
  })

  it("names the projects a zone is granted to", () => {
    const names = new Map([["shop", "acme-shop"]])
    expect(grantsLine(["shop", "blog"], names)).toBe(
      "Granted to acme-shop, blog"
    )
    expect(grantsLine([], names)).toBe("Not granted to a project yet")
  })
})
