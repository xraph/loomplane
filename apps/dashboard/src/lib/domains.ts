// What the Domains page shows, worked out from what the API returns: the
// words each route state reads as, which domains need a look, and the
// lines under each domain's name.
import { useEffect, useState } from "react"

import type {
  CloudinfraRouteStatus,
  DomainOffer,
  HostnameUse,
  Registration,
  Zone,
  ZoneOverview,
} from "@/client/forge/loomplane/types"
import { cloudLabel } from "@/lib/clouds"

export type DomainState =
  | "awaiting_verification"
  | "provisioning"
  | "ready"
  | "failed"
  | "renewing"
  | "cleanup_pending"

/** The pill colors from the mock. None of them follow the accent. */
export type PillTone = "ok" | "warn" | "idle" | "failed" | "own"

export function stateTone(state: DomainState): PillTone {
  switch (state) {
    case "ready":
      return "ok"
    case "provisioning":
    case "renewing":
      return "warn"
    case "failed":
      return "failed"
    default:
      return "idle"
  }
}

// What every column says for the states they share.
function shared(state: string): string {
  switch (state) {
    case "awaiting_verification":
      return "Awaiting verification"
    case "failed":
      return "Failed"
    case "cleanup_pending":
      return "Removing"
  }
  return state
}

// Until its zone is verified, every column of a route waits on it.
function zoneUnverified(s: CloudinfraRouteStatus): boolean {
  return (
    s.dns === "awaiting_verification" &&
    s.certificate === "awaiting_verification" &&
    s.route === "awaiting_verification"
  )
}

// A verified external zone whose record isn't there yet: the owner adds it.
function awaitingRecord(s: CloudinfraRouteStatus): boolean {
  return s.dns === "awaiting_verification" && !zoneUnverified(s)
}

export function dnsLabel(s: CloudinfraRouteStatus): string {
  if (awaitingRecord(s)) return "Awaiting record"
  switch (s.dns) {
    case "ready":
      return "Ready"
    case "provisioning":
      return "Provisioning"
  }
  return shared(s.dns)
}

export function dnsTone(s: CloudinfraRouteStatus): PillTone {
  return awaitingRecord(s) ? "warn" : stateTone(s.dns as DomainState)
}

export function certificateLabel(s: CloudinfraRouteStatus): string {
  switch (s.certificate) {
    case "ready":
      return "Active"
    case "provisioning":
      // A wildcard certificate can only be proved through DNS.
      return s.wildcard ? "Issuing (DNS-01)" : "Issuing (HTTP-01)"
    case "renewing":
      return "Renewing"
  }
  return shared(s.certificate)
}

export function routeLabel(s: CloudinfraRouteStatus): string {
  switch (s.route) {
    case "ready":
      return "Ready"
    case "provisioning":
      // DNS first: without the record no certificate can be issued over HTTP.
      if (s.dns !== "ready") return "Waiting for DNS"
      if (s.certificate !== "ready" && s.certificate !== "renewing")
        return "Waiting for certificate"
      return "Provisioning"
  }
  return shared(s.route)
}

/** zoneNeedsAttention: the zone, a hostname or its registration isn't done. */
export function zoneNeedsAttention(z: ZoneOverview, r?: Registration): boolean {
  if (z.state !== "ready") return true
  const settled = (h: HostnameUse) =>
    h.status.dns === "ready" &&
    h.status.certificate === "ready" &&
    h.status.route === "ready"
  if (!z.hostnames.every(settled)) return true
  return !!r && r.status !== "succeeded"
}

/** A domain and every zone under it, at any depth, sorted by name. */
export type DomainGroup = { domain: ZoneOverview; subdomains: ZoneOverview[] }

// By name, byte by byte, as the API sorts them.
function byName(a: ZoneOverview, b: ZoneOverview): number {
  const x = a.zone?.name ?? ""
  const y = b.zone?.name ?? ""
  return x < y ? -1 : x > y ? 1 : 0
}

/**
 * domainGroups puts each zone under the domain its parent chain reaches.
 * A domain is a zone with no parent; one whose parent isn't in the list
 * counts as a domain too, so no zone is lost.
 */
export function domainGroups(zones: ZoneOverview[]): DomainGroup[] {
  const byID = new Map(zones.map((z) => [z.zone?.id ?? "", z]))
  const rootOf = (z: ZoneOverview) => {
    const seen = new Set<ZoneOverview>()
    let at = z
    while (at.parentZoneId && !seen.has(at)) {
      seen.add(at)
      const parent = byID.get(at.parentZoneId)
      if (!parent) break
      at = parent
    }
    return at
  }
  const groups = new Map<ZoneOverview, DomainGroup>()
  for (const z of zones) {
    const root = rootOf(z)
    const group = groups.get(root) ?? { domain: root, subdomains: [] }
    groups.set(root, group)
    if (root !== z) group.subdomains.push(z)
  }
  return [...groups.values()]
    .sort((a, b) => byName(a.domain, b.domain))
    .map((g) => ({ ...g, subdomains: g.subdomains.sort(byName) }))
}

/**
 * parentZoneOf is the zone a new name would sit under: the one with the
 * longest name it ends in, on whole labels, as the API decides. A bare
 * top-level label is never one.
 */
export function parentZoneOf(
  name: string,
  zones: ZoneOverview[]
): ZoneOverview | undefined {
  let rest = name.trim().toLowerCase()
  for (;;) {
    const dot = rest.indexOf(".")
    if (dot < 0) return undefined
    rest = rest.slice(dot + 1)
    if (!rest.includes(".")) return undefined
    const parent = zones.find((z) => z.zone?.name.toLowerCase() === rest)
    if (parent) return parent
  }
}

/** groupNeedsAttention: the domain or any of its subdomains needs a look. */
export function groupNeedsAttention(g: DomainGroup, r?: Registration): boolean {
  return [g.domain, ...g.subdomains].some((z) => zoneNeedsAttention(z, r))
}

/** Rollup is a domain card's one pill for itself and its subdomains. */
export type Rollup = {
  label: "Ready" | "Verifying" | "Needs attention"
  tone: PillTone
  /** What needs you: zones, hostnames and the registration. */
  count: number
}

// A zone that won't get verified without you: its record didn't publish,
// its value expired, or it has no proof of its own or through a parent.
function zoneStuck(z: ZoneOverview, now: number): boolean {
  if (z.state === "ready" || !z.zone) return false
  const expired =
    isSet(z.zone.proofExpiresAt) &&
    new Date(z.zone.proofExpiresAt).getTime() <= now
  const unproven = !z.zone.proofName && !z.proofZoneId
  return !!z.zone.proofPublishError || expired || unproven
}

const hostnameFailed = (h: HostnameUse) =>
  h.status.dns === "failed" ||
  h.status.certificate === "failed" ||
  h.status.route === "failed"

// Ready on every column. Anything else is on its way, waiting on a
// record you add, or being removed.
const hostnameSettled = (h: HostnameUse) =>
  h.status.dns === "ready" &&
  h.status.certificate === "ready" &&
  h.status.route === "ready"

/**
 * domainRollup is Needs attention when anything in the group needs you,
 * else Verifying while a zone or hostname is on its way, else Ready.
 */
export function domainRollup(
  g: DomainGroup,
  r: Registration | undefined,
  now: number
): Rollup {
  const zones = [g.domain, ...g.subdomains]
  let count = 0
  for (const z of zones) {
    if (zoneStuck(z, now)) count++
    count += z.hostnames.filter(hostnameFailed).length
  }
  if (r && r.status !== "succeeded") count++
  if (count > 0) return { label: "Needs attention", tone: "failed", count }
  // Ready only when nothing would put the group under Needs attention.
  const moving = zones.some(
    (z) => z.state !== "ready" || !z.hostnames.every(hostnameSettled)
  )
  return moving
    ? { label: "Verifying", tone: "warn", count: 0 }
    : { label: "Ready", tone: "ok", count: 0 }
}

/** sameHosting: both zones' DNS sits with the same provider and account. */
export function sameHosting(a: Zone, b: Zone): boolean {
  return (
    a.mode === b.mode &&
    (a.provider ?? "") === (b.provider ?? "") &&
    (a.connectionId ?? "") === (b.connectionId ?? "")
  )
}

/** grantsLine names the projects a zone is granted to, by name if known. */
export function grantsLine(
  projectIds: string[],
  names: Map<string, string>
): string {
  return projectIds.length
    ? `Granted to ${projectIds.map((id) => names.get(id) ?? id).join(", ")}`
    : "Not granted to a project yet"
}

/** plural is "1 hostname", "2 hostnames". */
export function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`
}

/** providerLabel names who hosts the zone's DNS. */
export function providerLabel(z: ZoneOverview): string {
  if (z.zone?.mode === "external") return "External DNS"
  return cloudLabel(z.zone?.provider ?? "")
}

export function zoneDNSLabel(z: ZoneOverview): string {
  if (z.state !== "ready") {
    // Not in the mock: "Awaiting verification" for a zone with no TXT proof
    // yet, such as one verified before proofs were required, and
    // "Verifying" for a managed zone, whose record Loomplane publishes.
    if (!z.zone?.proofName) return "Awaiting verification"
    return z.zone.mode === "managed" ? "Verifying" : "Awaiting TXT record"
  }
  // Not in the mock: an external zone after its TXT record was found.
  if (z.zone?.mode === "external") return "Verified by TXT record"
  return `Managed by ${providerLabel(z)}`
}

export function zoneDNSTone(z: ZoneOverview): PillTone {
  return z.state === "ready" ? "ok" : "warn"
}

/**
 * Where the TXT record Loomplane publishes for a managed zone stands:
 * still being written, written and waiting for public DNS to show it, or
 * not written, with the reason on the zone. Not in the mock.
 */
export type ProofPublishState = "publishing" | "waiting" | "failed"

export function proofPublishState(z: Zone): ProofPublishState {
  // A publish clears the error, so one next to a receipt is a record
  // Loomplane couldn't confirm is still there.
  if (z.proofPublishError) return "failed"
  // A write whose answer was lost, or a renewed value, isn't published yet.
  const published =
    !!z.proofRecord &&
    !z.proofPublishAttempted &&
    z.proofRecord.value === z.proofValue
  return published ? "waiting" : "publishing"
}

export function registrationLine(z: ZoneOverview, r?: Registration): string {
  if (!r) {
    // Not in the mock, nor the dateless line below: the zone names a
    // registration the list doesn't hold yet.
    return z.registrationId || z.zone?.registrationId
      ? "Registered through Loomplane"
      : "Registered elsewhere"
  }
  // Not in the mock: Cloudflare hasn't reported the expiry yet.
  if (!isSet(r.domainExpiresAt)) return "Registered through Loomplane"
  const day = formatDay(r.domainExpiresAt)
  return `Registered through Loomplane · ${r.autoRenew ? "renews" : "expires"} ${day}`
}

/** Registrations still being bought, before their zone exists. */
export const inFlight = ["approved", "registering", "zone_pending"]

/** Registrations that ended without a zone. */
export const endedWithoutZone = [
  "failed",
  "action_required",
  "blocked",
  "price_changed",
  "unconfirmed",
]

/** registrationStatusLine is a registration card's line, by its status. */
export function registrationStatusLine(r: Registration): string {
  switch (r.status) {
    case "approved":
    case "registering":
      return "Cloudflare is registering this domain."
    case "zone_pending":
      return "Registered. Waiting for Cloudflare to activate the zone."
    case "price_changed":
      return "The price changed before purchase. Nothing was charged. Register it again to see the new price."
    case "failed":
      return `Cloudflare declined the registration: ${r.message}`
    case "action_required":
    case "blocked":
      return "Finish this in your Cloudflare dashboard."
  }
  return r.message
}

/** cloudflareRegistrations is where a registration is finished by hand. */
export function cloudflareRegistrations(accountID: string): string {
  return `https://dash.cloudflare.com/${encodeURIComponent(accountID)}/domains/registrations`
}

export function runsOnLabel(h: HostnameUse): string {
  return h.status.backend === "kubernetes"
    ? `Kubernetes · ${h.runsOn}`
    : `Docker · ${h.runsOn}`
}

/**
 * usedByLabel is "{project} / {environment} / {service}". The overview
 * carries the environment's ID only, so the caller passes its name when it
 * has it.
 */
export function usedByLabel(h: HostnameUse, environment?: string): string {
  return `${h.projectName || h.projectId} / ${environment || h.environmentId} / ${h.service}`
}

export function isWildcardHostname(h: string): boolean {
  return h.startsWith("*.")
}

/** hostnameInZone: h is the zone's apex or a name under it. */
export function hostnameInZone(h: string, zone: string): boolean {
  const name = h.toLowerCase()
  const z = zone.toLowerCase()
  return name === z || name.endsWith(`.${z}`)
}

export type OfferStatus = "available" | "taken" | "owned" | "unsupported"

// Reasons Cloudflare gives for a name its API can't register.
const notOffered = [
  "extension_not_supported_via_api",
  "extension_not_supported",
  "extension_disallows_registration",
]

/**
 * offerStatus is what a search result reads as. The extensions are the
 * ones the registrar's API lists; a name under any other is not offered,
 * whatever the search said about it.
 */
export function offerStatus(
  offer: DomainOffer,
  ownedNames: Set<string>,
  extensions: string[]
): OfferStatus {
  const name = offer.name.toLowerCase()
  if (ownedNames.has(name)) return "owned"
  const listed = extensions.some((e) =>
    name.endsWith(`.${e.toLowerCase().replace(/^\./, "")}`)
  )
  if (!listed || notOffered.includes(offer.reason ?? "")) return "unsupported"
  return offer.registrable ? "available" : "taken"
}

export function offerLabel(s: OfferStatus): string {
  switch (s) {
    case "available":
      return "Available"
    case "owned":
      return "You own this"
    case "unsupported":
      return "Not offered through the API yet"
  }
  return "Taken"
}

export function offerTone(s: OfferStatus): PillTone {
  return s === "available" ? "ok" : s === "owned" ? "own" : "idle"
}

// Registrations that bought the name, are buying it, or may have. Listed
// rather than excluded, so a status added later doesn't block a name.
const held = [
  "approved",
  "registering",
  "zone_pending",
  "succeeded",
  "unconfirmed",
  "action_required",
  "blocked",
]

/**
 * ownedNames is every name already a zone of the organization or a
 * registration that may hold it, lowercased.
 */
export function ownedNames(
  zones: ZoneOverview[],
  registrations: Registration[]
): Set<string> {
  const names = new Set<string>()
  for (const z of zones) if (z.zone?.name) names.add(z.zone.name.toLowerCase())
  for (const r of registrations)
    if (held.includes(r.status)) names.add(r.name.toLowerCase())
  return names
}

/** formatPrice is "USD 10.11", or empty when the provider gave no price. */
export function formatPrice(currency: string, cost: string): string {
  return cost ? `${currency} ${cost}`.trim() : ""
}

const dayFormat = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  year: "numeric",
  timeZone: "UTC",
})

/** formatDay is "Nov 19, 2026". Registrations expire on a calendar day. */
export function formatDay(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : dayFormat.format(d)
}

/** isSet: the API sends Go's zero time for a moment that hasn't happened. */
export function isSet(iso: string | undefined): iso is string {
  if (!iso) return false
  const d = new Date(iso)
  return !Number.isNaN(d.getTime()) && d.getUTCFullYear() > 1
}

const relativeFormat = new Intl.RelativeTimeFormat("en-US", {
  numeric: "auto",
})

/** relativeTime is "just now", "4 minutes ago", "yesterday". */
export function relativeTime(iso: string, now: number): string {
  const seconds = (new Date(iso).getTime() - now) / 1000
  if (Math.abs(seconds) < 45) return "just now"
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["minute", 60],
    ["hour", 3600],
    ["day", 86400],
  ]
  let unit = units[0]
  for (const u of units) if (Math.abs(seconds) >= u[1]) unit = u
  return relativeFormat.format(Math.round(seconds / unit[1]), unit[0])
}

/** The renewal notice goes out this long before a domain renews. */
export const renewalWindow = 30 * 24 * 60 * 60 * 1000

/** renewsSoon: a registered domain renews, or lapses, within the window. */
export function renewsSoon(r: Registration, now: number): boolean {
  return (
    r.status === "succeeded" &&
    isSet(r.domainExpiresAt) &&
    new Date(r.domainExpiresAt).getTime() - now <= renewalWindow
  )
}

/** settling: something on the page is still changing on its own. */
export function settling(
  zones: ZoneOverview[],
  registrations: Registration[]
): boolean {
  const moving = ["provisioning", "renewing", "cleanup_pending"]
  return (
    zones.some((z) =>
      z.hostnames.some(
        (h) =>
          moving.includes(h.status.dns) ||
          moving.includes(h.status.certificate) ||
          moving.includes(h.status.route)
      )
    ) || registrations.some((r) => inFlight.includes(r.status))
  )
}

/** useClock is the time now, a minute at a time, for relative times. */
export function useClock(interval = 60000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), interval)
    return () => window.clearInterval(id)
  }, [interval])
  return now
}
