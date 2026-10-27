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

export type Fixture = {
  board?: Json
  /** The board read answers with this status instead. */
  boardStatus?: number
  /** The board read never answers. */
  boardPending?: boolean
  adminUrl?: string
  adminStatus?: number
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
      if (method !== "GET") writes.push({ method, path, body })
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
        return json(fixture.board ?? mockBoard())
      }
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
