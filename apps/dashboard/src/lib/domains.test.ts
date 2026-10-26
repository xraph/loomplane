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
  formatDay,
  formatPrice,
  hostnameInZone,
  isWildcardHostname,
  offerLabel,
  offerStatus,
  offerTone,
  ownedNames,
  registrationLine,
  registrationStatusLine,
  routeLabel,
  runsOnLabel,
  stateTone,
  usedByLabel,
  zoneDNSLabel,
  zoneNeedsAttention,
} from "./domains"

function status(over: Partial<CloudinfraRouteStatus> = {}): CloudinfraRouteStatus {
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
    // A managed zone waits on the same TXT proof.
    expect(zoneDNSLabel(zone({}, false))).toBe("Awaiting TXT record")
    const legacy = zone({}, false)
    legacy.zone = { ...legacy.zone!, proofName: undefined }
    expect(zoneDNSLabel(legacy)).toBe("Awaiting verification")
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
