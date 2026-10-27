// A stub of the API the Octopus panels read, shared by their tests. The
// board is the one in the Routes mock, in the API's own JSON.
import { ClientProvider } from "@forge-go/client-react"
import { render } from "@testing-library/react"
import type { ReactElement } from "react"
import { vi } from "vitest"

import type { Binding } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import type { CatalogPanelProps, ResourceConfigView } from "@/lib/catalog-kinds"

export function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

export type Write = { method: string; path: string; body: unknown }

type Json = Record<string, unknown>

const minutesAgo = (n: number) =>
  new Date(Date.now() - n * 60_000).toISOString()

const ready = (hostname: string, certificate = "ready"): Json => ({
  route_id: `dr_${hostname}`,
  hostname,
  wildcard: hostname.startsWith("*."),
  backend: "edge--gateway",
  dns: "ready",
  certificate,
  route: certificate === "ready" ? "ready" : "provisioning",
  certificate_expires_at: "2027-01-20T00:00:00Z",
  checked_at: minutesAgo(1),
  retryable: false,
})

export const row = (over: Json): Json => ({
  key: `${over.source ?? "farp"}:${over.path}`,
  source: "farp",
  methods: [],
  service: "api",
  port: 8080,
  note: "",
  strip_prefix: false,
  overrides: false,
  ...over,
})

export const here = (over: Json): Json =>
  row({
    source: "here",
    route_id: `rt_${String(over.path)}`,
    revision: 3,
    sync: "applied",
    ...over,
  })

export const card = (hostname: string, routes: Json[], over: Json = {}) => ({
  hostname,
  attached: hostname !== "",
  ...(hostname ? { domain_route_id: `dr_${hostname}` } : {}),
  ...(hostname ? { status: ready(hostname) } : {}),
  farp_home: false,
  routes,
  ...over,
})

/** The board drawn in the Routes mock. */
export function mockBoard(): Json {
  return {
    gateway: {
      resource: "edge",
      version: "0.5.0",
      provider: "server",
      private_url: "http://edge--gateway:8080",
      farp_hostnames: ["api.acme.dev"],
      generation: 4,
      applied_generation: 4,
      revision: 2,
      synced_at: minutesAgo(2),
      attach: {
        environment_id: "env_prod",
        service: "edge--gateway",
        port: 8080,
        blocked_path_prefixes: ["/__farp", "/_farp", "/admin"],
      },
    },
    discovery: {
      mode: "push",
      last_change: minutesAgo(2),
      services: ["api", "billing", "auth"].map((service) => ({
        service,
        opted_in: true,
        state: "registered",
        routes: 1,
        updated_at: minutesAgo(2),
      })),
    },
    hosts: [
      card("acme.dev", [
        here({ path: "/", note: "Storefront", service: "web", port: 3000 }),
      ]),
      card(
        "api.acme.dev",
        [
          row({
            path: "/v1/orders/*",
            note: "From the OpenAPI schema",
            methods: ["GET", "POST"],
          }),
          row({
            path: "/v1/invoices/*",
            note: "From the OpenAPI schema",
            methods: ["GET", "POST"],
            service: "billing",
          }),
          row({
            path: "/v1/events",
            note: "From the AsyncAPI schema · WebSocket",
            methods: ["GET"],
            service: "billing",
          }),
          here({
            path: "/auth/*",
            note: "Strips /auth before forwarding",
            service: "auth",
            port: 9000,
            strip_prefix: true,
            overrides: true,
          }),
        ],
        { farp_home: true }
      ),
      card(
        "*.acme.dev",
        [
          here({
            path: "/",
            note: "Customer subdomains, like shop1.acme.dev",
            service: "web",
            port: 3000,
            key: "here:wild",
          }),
        ],
        { status: ready("*.acme.dev", "provisioning") }
      ),
    ],
    unrouted: [{ service: "admin", port: 4000 }],
    unroutable: [{ service: "worker", reason: "no-port" }],
    counts: { all: 6, farp: 3, here: 3 },
    live: true,
  }
}

/** A route the API stores, in its own JSON. */
export function savedRoute(over: Json = {}): Json {
  return {
    id: "rt_new",
    org_id: "org",
    project_id: "p",
    environment_id: "env_prod",
    resource: "edge",
    hostname: "api.acme.dev",
    path: "/auth/*",
    methods: [],
    service: "auth",
    port: 9000,
    strip_prefix: true,
    note: "",
    revision: 1,
    created_by: "user_1",
    created_at: "2026-10-26T20:00:00Z",
    updated_at: "2026-10-26T20:00:00Z",
    ...over,
  }
}

const applied = {
  state: "applied",
  generation: 5,
  restarted: false,
  restored: 0,
}

/** A domain route operation, as preview and apply answer. */
export function operation(id: string, status: string, message = ""): Json {
  const zero = "0001-01-01T00:00:00Z"
  return {
    id,
    kind: "apply",
    status,
    review: "review-token",
    attempt: 1,
    revision: 1,
    generation: 0,
    message,
    created_at: zero,
    expires_at: zero,
    not_before: zero,
    dns_attempted: false,
    desired_record: {
      id: "",
      zone_id: "zone_dev",
      type: "CNAME",
      name: "docs.acme.dev",
      value: "edge-1.loomplane.net",
      ttl: 300,
    },
    route: { id: "route_docs", revision: 1 },
  }
}

/** The gateway's status, as the Settings tab reads it. */
export function mockStatus(): Json {
  return {
    ...(mockBoard().gateway as Json),
    start_time: minutesAgo(180),
  }
}

/** What FARP found on Docker: three registered services and one waiting. */
export function mockDiscovery(over: Json = {}): Json {
  return {
    mode: "push",
    last_change: minutesAgo(2),
    services: [
      {
        service: "api",
        opted_in: true,
        metadata: {
          "farp.openapi.path": "/openapi.json",
          "farp.port": "8080",
        },
        state: "registered",
        routes: 1,
        updated_at: minutesAgo(2),
      },
      {
        service: "billing",
        opted_in: true,
        metadata: {
          "farp.openapi.path": "/openapi.json",
          "farp.asyncapi.path": "/asyncapi.json",
          "farp.port": "8080",
        },
        state: "registered",
        routes: 2,
        updated_at: minutesAgo(2),
      },
      {
        service: "auth",
        opted_in: true,
        metadata: { "farp.port": "9000" },
        state: "failed",
        error: "Octopus refused the registration: schema fetch timed out.",
        routes: 0,
        updated_at: minutesAgo(5),
      },
      {
        service: "search",
        opted_in: true,
        metadata: { "farp.graphql.path": "/graphql", "farp.port": "7700" },
        state: "waiting",
        routes: 0,
        updated_at: "0001-01-01T00:00:00Z",
      },
    ],
    ...over,
  }
}

/** The domain routes behind the board's attached cards, in the API's JSON. */
export function domainRoutes(): Json[] {
  return ["acme.dev", "api.acme.dev", "*.acme.dev"].map((hostname) => ({
    id: `dr_${hostname}`,
    org_id: "org",
    project_id: "p",
    operation_id: "",
    status: "ready",
    revision: 7,
    updated_at: "2026-10-26T20:00:00Z",
    input: {
      zone_id: "zone_dev",
      cloud_id: "",
      environment_id: "env_prod",
      service: "edge--gateway",
      hostname,
      port: 8080,
      blocked_path_prefixes: ["/__farp", "/_farp", "/admin"],
    },
  }))
}

/** The domains granted to the project, as the attach flow reads them. */
export const projectDomains: Json[] = [
  {
    zone_id: "zone_dev",
    name: "acme.dev",
    mode: "managed",
    provider: "cloudflare",
    verified: true,
    wildcards: true,
    hostnames: [],
  },
]

export type Fixture = {
  /** The board, or a function read on every board request. */
  board?: Json | (() => Json)
  /** The board read answers with this status instead. */
  boardStatus?: number
  /** The board read never answers. */
  boardPending?: boolean
  /** The status read, or the status it answers with, or never answering. */
  status?: Json
  statusStatus?: number
  statusPending?: boolean
  /** The discovery read, the same way. */
  discovery?: Json
  discoveryStatus?: number
  discoveryPending?: boolean
  /** The project's domain routes and the domains granted to it. */
  domainRoutes?: Json[]
  projectDomains?: Json[]
  adminUrl?: string
  adminStatus?: number
  /** Answers a write before the defaults do, when it returns a response. */
  write?: (w: Write) => Response | undefined
}

/** serve stubs the API and returns what was written. */
export function serve(fixture: Fixture = {}) {
  const writes: Write[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET"
      const path = new URL(String(input), "http://localhost").pathname
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method !== "GET") {
        const w = { method, path, body }
        writes.push(w)
        const answer = fixture.write?.(w)
        if (answer) return answer
        if (path.endsWith("/octopus/edge/routes"))
          return json({ route: savedRoute(body as Json), sync: applied }, 201)
        if (/\/octopus\/edge\/routes\/[^/]+\/update$/.test(path))
          return json({ route: savedRoute(body as Json), sync: applied })
        if (/\/octopus\/edge\/routes\/[^/]+\/remove$/.test(path))
          return json(applied)
        if (path.endsWith("/domain-routes/preview"))
          return json(operation("routeop_1", "preview"))
        if (/\/route-operations\/[^/]+\/apply$/.test(path))
          return json(operation(path.split("/")[5], "pending"))
        if (/\/domain-routes\/[^/]+\/remove$/.test(path))
          return json(operation("routeop_remove", "pending"))
        if (path.endsWith("/octopus/edge/farp-hostnames"))
          return json({
            ...mockStatus(),
            farp_hostnames: (body as { hostnames: string[] }).hostnames,
          })
        if (path.endsWith("/octopus/edge/sync")) return json(applied)
      }
      if (path.endsWith("/catalog/edge/admin-sessions")) {
        if (fixture.adminStatus)
          return json(
            { message: "Octopus isn't running yet." },
            fixture.adminStatus
          )
        return json(
          {
            url: fixture.adminUrl ?? "/catalog-admin/tok/admin",
            expires_at: "2026-10-26T20:00:00Z",
          },
          201
        )
      }
      if (path.endsWith("/octopus/edge/board")) {
        if (fixture.boardPending) return new Promise<Response>(() => {})
        if (fixture.boardStatus)
          return json({ message: "Something went wrong." }, fixture.boardStatus)
        const board =
          typeof fixture.board === "function" ? fixture.board() : fixture.board
        return json(board ?? mockBoard())
      }
      if (path.endsWith("/octopus/edge/status")) {
        if (fixture.statusPending) return new Promise<Response>(() => {})
        if (fixture.statusStatus)
          return json(
            { message: "Something went wrong." },
            fixture.statusStatus
          )
        return json(fixture.status ?? mockStatus())
      }
      if (path.endsWith("/octopus/edge/discovery")) {
        if (fixture.discoveryPending) return new Promise<Response>(() => {})
        if (fixture.discoveryStatus)
          return json(
            { message: "Something went wrong." },
            fixture.discoveryStatus
          )
        return json(fixture.discovery ?? mockDiscovery())
      }
      if (path.endsWith("/connections"))
        return json({
          connections: [
            { id: "conn_1", name: "edge-1", cloud: "server", status: "ready" },
          ],
        })
      if (path.endsWith("/projects/p/domains"))
        return json({ domains: fixture.projectDomains ?? projectDomains })
      if (path.endsWith("/projects/p/domain-routes"))
        return json({ routes: fixture.domainRoutes ?? [] })
      if (path === "/api/v1/domains")
        return json({ zones: [], registrations: [] })
      if (path.endsWith("/projects/p/environments"))
        return json({ environments: [{ id: "env_prod", name: "production" }] })
      if (path.endsWith("/projects/p"))
        return json({ id: "p", name: "acme-shop" })
      throw new Error(`Unexpected request ${method} ${path}`)
    }
  )
  return writes
}

/** The props a catalog panel is given for an Octopus on Docker. */
export function panelProps(
  overrides: Partial<CatalogPanelProps> = {}
): CatalogPanelProps {
  const config: ResourceConfigView = { kind: "octopus", provider: "server" }
  const binding = {
    id: "b_edge",
    name: "edge",
    subject: "resource",
    provider: "server",
    engine: "octopus",
    environmentId: "env_prod",
    connectionId: "conn_1",
    config: JSON.stringify(config),
    projectId: "p",
  } as Binding
  return {
    projectId: "p",
    environmentId: "env_prod",
    resource: "edge",
    kind: "octopus",
    binding,
    config,
    components: [],
    ...overrides,
  }
}

export function mount(ui: ReactElement) {
  return render(<ClientProvider client={forgeClient}>{ui}</ClientProvider>)
}
