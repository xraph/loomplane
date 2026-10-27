// What the Domains API answers in page tests, in its own snake_case.
import { vi } from "vitest"

export const zero = "0001-01-01T00:00:00Z"

export const inTwelveDays = new Date(Date.now() + 12 * 86400000).toISOString()

export type Host = {
  hostname: string
  runs_on: string
  backend: string
  wildcard?: boolean
  dns: string
  certificate: string
  route: string
}

/** host is a hostname that is ready on every column. */
export function host(hostname: string, over: Partial<Host> = {}): Host {
  return {
    hostname,
    runs_on: "web-1",
    backend: "server",
    dns: "ready",
    certificate: "ready",
    route: "ready",
    ...over,
  }
}

/** zoneID is the ID zoneWith gives the zone named name. */
export function zoneID(name: string): string {
  return `zone-${name}`
}

export function zoneWith(
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
    /** Who hosts a managed zone's DNS. Cloudflare unless set. */
    provider?: string
    connection?: string
    /** The name of the zone this one sits under. */
    parent?: string
    /** The names of every zone under this one. */
    subdomains?: string[]
    /** The name of the zone whose TXT record proves this one. */
    proofZone?: string
  } = {}
) {
  const proof = options.proof ?? options.external
  const verified = options.verified ?? !options.external
  const id = zoneID(name)
  const provider = options.provider ?? "cloudflare"
  const hosting = provider === "cloudflare" ? "cf" : provider
  return {
    zone: {
      id,
      org_id: "org",
      name,
      mode: options.external ? "external" : "managed",
      provider: options.external ? "" : provider,
      connection_id: options.external ? "" : (options.connection ?? "cf"),
      project_ids: ["shop"],
      proof_name: proof ? `_loomplane.${name}` : "",
      proof_value: proof ? "lp-proof-123" : "",
      proof_record: options.published
        ? {
            id: "rec",
            zone_id: `${hosting}-${options.hostedIn ?? name}`,
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
            id: `${hosting}-${options.hostedIn ?? name}`,
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
    parent_zone_id: options.parent ? zoneID(options.parent) : undefined,
    subdomains: (options.subdomains ?? []).map(zoneID),
    proof_zone_id: options.proofZone ? zoneID(options.proofZone) : undefined,
    hostnames: hostnames.map((h, i) => ({
      route_id: `route-${name}-${i}`,
      hostname: h.hostname,
      project_id: "shop",
      project_name: "acme-shop",
      environment_id: "env-prod",
      service: "edge",
      port: 8080,
      runs_on: h.runs_on,
      status: {
        route_id: `route-${name}-${i}`,
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

export function overviewWith(...zones: ReturnType<typeof zoneWith>[]): {
  zones: unknown[]
  registrations: unknown[]
} {
  return { zones, registrations: [] }
}

export function registration(over: Record<string, unknown> = {}) {
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

export type Write = { method: string; path: string; body: unknown }

/**
 * stubFetch answers the page's reads from routes, keyed by path or by
 * "METHOD path", and records every write. Connections, projects and
 * environments have defaults.
 */
export function stubFetch(
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
