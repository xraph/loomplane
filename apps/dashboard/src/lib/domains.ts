// What the Domains page shows, worked out from what the API returns: the
// words each route state reads as, which domains need a look, and the
// lines under each domain's name.
import { useEffect, useState } from "react"

import type {
  HostnameUse,
  Registration,
  RouteStatus,
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
export type PillTone = "ok" | "warn" | "idle" | "failed"

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
function zoneUnverified(s: RouteStatus): boolean {
  return (
    s.dns === "awaiting_verification" &&
    s.certificate === "awaiting_verification" &&
    s.route === "awaiting_verification"
  )
}

// A verified external zone whose record isn't there yet: the owner adds it.
function awaitingRecord(s: RouteStatus): boolean {
  return s.dns === "awaiting_verification" && !zoneUnverified(s)
}

export function dnsLabel(s: RouteStatus): string {
  if (awaitingRecord(s)) return "Awaiting record"
  switch (s.dns) {
    case "ready":
      return "Ready"
    case "provisioning":
      return "Provisioning"
  }
  return shared(s.dns)
}

export function dnsTone(s: RouteStatus): PillTone {
  return awaitingRecord(s) ? "warn" : stateTone(s.dns as DomainState)
}

export function certificateLabel(s: RouteStatus): string {
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

export function routeLabel(s: RouteStatus): string {
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

/** providerLabel names who hosts the zone's DNS. */
export function providerLabel(z: ZoneOverview): string {
  if (z.zone?.mode === "external") return "External DNS"
  return cloudLabel(z.zone?.provider ?? "")
}

export function zoneDNSLabel(z: ZoneOverview): string {
  if (z.state !== "ready") {
    return z.zone?.mode === "external"
      ? "Awaiting TXT record"
      : "Awaiting verification"
  }
  // Not in the mock: an external zone after its TXT record was found.
  if (z.zone?.mode === "external") return "Verified by TXT record"
  return `Managed by ${providerLabel(z)}`
}

export function zoneDNSTone(z: ZoneOverview): PillTone {
  return z.state === "ready" ? "ok" : "warn"
}

export function registrationLine(z: ZoneOverview, r?: Registration): string {
  if (!r) {
    // The zone names a registration the list doesn't hold yet.
    return z.registrationId || z.zone?.registrationId
      ? "Registered through Loomplane"
      : "Registered elsewhere"
  }
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
